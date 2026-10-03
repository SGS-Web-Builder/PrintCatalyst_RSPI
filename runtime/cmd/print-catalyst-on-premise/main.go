// Command print-catalyst-on-premise is the Windows-only merchant daemon.
// It hosts a local HTTP server (dashboard + customer portal + owner API),
// discovers the merchant's printers via the Win32 print spooler, accepts
// orders that the customer pays for through the merchant's Razorpay
// account, and dispatches each paid order to the assigned printer.
//
// The runtime is intentionally platform-locked to Windows. The merchant's
// PC is the only supported host; no Linux/macOS executable is shipped.
//
// Endpoints exposed on the configured bind address:
//
//	/                                          — customer portal
//	/setup                                     — owner setup wizard
//	/dashboard                                 — owner dashboard (single SPA)
//	/api/v1/owner/...                          — owner REST API (RBAC)
//	/api/v1/orders/...                         — customer REST API
//	/api/v1/payments/webhook?provider=<id>     — Razorpay webhook receiver
//
// All file system state lives under PC_DATA_DIR (default
// %ProgramData%\PrintCatalyst\OnPremise). The local SQLite database is the
// single source of truth; the dashboard and customer portal talk to the
// API directly, never to the database.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/autodelete"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/config"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/configparser"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/eventlog"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/kiosk"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pairing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments/razorpay"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/discover"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/reports"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/supervisor"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// runtimeState holds the mutable configuration the
// /api/v1/owner/config/reload endpoint can swap. The state is
// guarded by a mutex so a concurrent reload and a config.View()
// read see a consistent snapshot.
type runtimeState struct {
	mu             sync.RWMutex
	installationID string
	dataDir        string
	databasePath   string
	bindHost       string
	port           int
	publicOrigin   string
	controlOrigin  string
	bootstrap      bool
}

func (s *runtimeState) view() localserver.ConfigView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return localserver.ConfigView{
		InstallationID:     s.installationID,
		DataDirectory:      s.dataDir,
		BindHost:           s.bindHost,
		Port:               s.port,
		PublicOrigin:       s.publicOrigin,
		ControlPlaneOrigin: s.controlOrigin,
		DatabasePath:       s.databasePath,
		Bootstrap:          s.bootstrap,
	}
}

func (s *runtimeState) reload(ctx context.Context, server *localserver.Server) (localserver.ConfigView, localserver.ConfigView, error) {
	previous := s.view()
	settings, err := config.Validate(os.Getenv, s.dataDir)
	if err != nil {
		return previous, previous, err
	}
	s.mu.Lock()
	s.installationID = settings.InstallationID
	s.bindHost = settings.BindHost
	s.port = settings.Port
	s.publicOrigin = settings.PublicOrigin
	s.controlOrigin = settings.ControlPlaneOrigin
	s.bootstrap = settings.Bootstrap
	// DatabasePath is intentionally NOT updated at runtime — moving
	// an active sqlite file while the runtime holds open
	// connections would silently corrupt the database. A change of
	// PC_DATABASE_PATH therefore requires a service restart, which
	// is surfaced through Config.DatabasePath vs the running state.
	s.mu.Unlock()
	server.SetRuntimeConfig(settings.InstallationID, settings.PublicOrigin, settings.Bootstrap)
	next := s.view()
	return previous, next, nil
}

func main() {
	// Parse --console before any other initialization. When set, the
	// Windows build runs in a plain console window instead of requiring
	// installation as a Windows Service. This is the intended mode for
	// local development and manual testing on Windows.
	consoleMode := flag.Bool("console", false, "run in console mode without requiring Windows Service installation")
	flag.Parse()

	// The runtime reads its configuration from
	// `[data directory]/config.env` plus process environment overrides.
	// We first resolve the data directory without depending on the full
	// config package so a fresh checkout still knows where to look.
	dataDir := config.ResolveDataDirectory(os.Getenv)
	log.Printf("dataDir resolved to: %s", dataDir)
	log.Printf("PC_DATA_DIR env=%q PC_PRINT_CATALYST_DATA_DIR env=%q",
		os.Getenv("PC_DATA_DIR"), os.Getenv("PRINT_CATALYST_DATA_DIR"))

	// Compute dbPath early so ensureBootstrapConfig can write it into
	// config.env before store.Open requires the directory to exist.
	fileValues, err := configparser.Load(dataDir, os.Getenv)
	if err != nil {
		// Cannot use diagnostic yet; fall back to stderr until the
		// log directory is confirmed to exist.
		log.Fatalf("read config file: %v", err)
	}
	dbPath := strings.TrimSpace(fileValues["PC_DATABASE_PATH"])
	if dbPath == "" {
		dbPath = configparser.DefaultDBPath(dataDir)
	}

	// On a completely fresh install the data directory does not exist yet.
	// Create it now so that eventlog.Open, store.Open, and ensureBootstrapConfig
	// all have a valid target. os.MkdirAll is safe to call on an existing dir.
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data directory %s: %v", dataDir, err)
	}

	// Ensure config.env exists with the required keys so a completely
	// fresh install — one where the operator has never run the setup
	// wizard and config.env was never written — can still reach
	// config.Load with placeholder values that produce Bootstrap=true.
	// We write the resolved dbPath so the strict path-in-data-dir check
	// passes without the file having been created yet.
	if err := ensureBootstrapConfig(dataDir, dbPath); err != nil {
		log.Fatalf("write bootstrap config: %v", err)
	}

	// Open the diagnostic writer before any other step so service
	// startup failures and configuration errors land in the same
	// log that operators are told to read. The writer falls back to
	// a protected rotating file under dataDir/logs when the Event
	// Log source is not yet registered (for example during the very
	// first start before the MSI step has run).
	diagnostic, err := eventlog.Open(dataDir)
	if err != nil {
		// Fall back to stderr-only diagnostics when the writer
		// cannot be opened at all. The runtime must not refuse to
		// start just because the diagnostic surface is missing.
		log.Printf("eventlog: open diagnostic writer failed: %v", err)
		diagnostic = stderrOnlyWriter{}
	}
	defer func() { _ = diagnostic.Close() }()

	log.Printf("step: eventlog.Open succeeded; opening database at %s", dbPath)
	// Open the resolved database path first. We must do this BEFORE
	// strict configuration validation because the bootstrap helper
	// persists the device key pair into the same database; opening
	// the wrong path here would silently corrupt a future install.
	database, err := store.Open(context.Background(), dbPath)
	if err != nil {
		log.Printf("step: store.Open FAILED: %v", err)
		fatalf(diagnostic, "local database startup failed at %s: %v", dbPath, err)
	}
	log.Printf("step: store.Open succeeded")
	files, err := localfiles.New(dataDir)
	if err != nil {
		log.Printf("step: localfiles.New FAILED: %v", err)
		fatalf(diagnostic, "protected local storage failed: %v", err)
	}
	log.Printf("step: localfiles.New succeeded")

	// Bootstrap identity before strict validation. The helper
	// refuses to silently overwrite a real installation id with one
	// derived from a freshly generated key pair, so a config.env
	// that drifts from the encrypted key stored in the database
	// produces an explicit error rather than silently rotating the
	// device identity.
	if err := bootstrapInstallationIdentity(context.Background(), dataDir, database.DB(), files); err != nil {
		log.Printf("step: bootstrapInstallationIdentity FAILED: %v", err)
		fatalf(diagnostic, "installation identity bootstrap failed: %v", err)
	}
	log.Printf("step: bootstrapInstallationIdentity succeeded")

	// Strict validation now that bootstrap has produced a real id.
	// The Bootstrap flag is true when placeholders are still
	// present; the rest of the runtime uses it to refuse every
	// public-facing endpoint until the operator runs the local
	// setup wizard.
	log.Printf("step: calling config.Load...")
	settings, err := config.Load(os.Getenv, dataDir)
	if err != nil {
		fatalf(diagnostic, "invalid configuration: %v", err)
	}
	_ = diagnostic.Info(fmt.Sprintf("runtime starting; data_dir=%s database_path=%s bootstrap=%t", dataDir, settings.DatabasePath, settings.Bootstrap))
	_ = diagnostic.Info(fmt.Sprintf("installation identity resolved; id_prefix=%s", settings.InstallationID[:min(8, len(settings.InstallationID))]))

	runCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	accounts := owner.New(database.DB())
	// DEV_UNLICENSED: create a default admin/admin owner so the setup
	// wizard is skipped and the browser goes straight to the dashboard.
	devAutoCreateOwner(context.Background(), accounts)
	setupToken, err := accounts.EnsureBootstrapToken(context.Background(), files)
	if err != nil {
		fatalf(diagnostic, "owner setup initialization failed: %v", err)
	}
	pricingSvc := pricing.New(database.DB())
	ordersSvc := orders.New(database.DB(), pricingSvc)
	reportsSvc := reports.New(database.DB())
	notifSvc := notifications.New(database.DB(), nil)
	printersSvc := printers.New(database.DB())
	// Windows-only: register the Win32 spooler discoverer and the
	// cross-platform IPP discoverer so a merchant can attach a
	// network printer that is not visible to the local spooler
	// (e.g. a printer in another LAN segment exposed over IPP).
	registerPrinterDiscoverers(printersSvc)
	// Start the discovery loop so printers discovered by any backend
	// (Win32 spooler, IPP, Bluetooth) are persisted to the database
	// and visible in the dashboard. Without this call the registered
	// discoverers exist but never emit records.
	if err := printersSvc.Start(runCtx); err != nil {
		fatalf(diagnostic, "printer discovery service start failed: %v", err)
	}
	log.Printf("step: printersSvc.Start succeeded; discovery loop running")
	idcardsSvc := idcards.New(database.DB(), files)
	passportsSvc := passport.New(database.DB(), files, passport.GeometricDetector{})
	tunnelSvc := tunnel.New(database.DB())
	pairingSvc, err := pairing.New(database.DB())
	if err != nil {
		fatalf(diagnostic, "pairing service startup failed: %v", err)
	}
	licensingSvc, err := licensing.New(database.DB(), settings.DataDirectory)
	if err != nil {
		fatalf(diagnostic, "licence service startup failed: %v", err)
	}
	if err := licensegate.ValidateReleaseConfig(); err != nil {
		fatalf(diagnostic, "software licence configuration: %v", err)
	}
	softwareLicense, err := licensegate.New(database.DB(), settings.InstallationID, licensegate.ReleaseURL, licensegate.ReleasePublicKey)
	if err != nil {
		fatalf(diagnostic, "software licence startup failed: %v", err)
	}
	paymentsSvc, err := payments.New(database.DB(), settings.DataDirectory,
		payments.WithProviderAdapter(payments.KindRazorpayMerchant, razorpayAdapter()),
		payments.WithOrders(ordersSvc),
	)
	if err != nil {
		fatalf(diagnostic, "payments service startup failed: %v", err)
	}
	businessSvc := business.New(database.DB())
	pickupSvc, err := pickup.NewPlatform(database.DB())
	if err != nil {
		fatalf(diagnostic, "pickup service startup failed: %v", err)
	}
	go func() { _ = pickupSvc.Run(runCtx) }()
	go func() {
		if err := paymentsSvc.RunReconciliation(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("payment reconciliation stopped: %v", err)
		}
	}()

	// Print dispatcher: polls the orders table every 20 ms for orders that
	// have transitioned to "paid" and submits them to the configured printer
	// backend. The resolver wraps the printers service to map printer IDs
	// (set by the customer via primaryPrinterId) to OS-level queue names.
	// Started here so the dispatcher runs for the lifetime of the service.
	// On non-Windows (dev machines) the winspool backend is unavailable;
	// a nil backend causes Run to return early so the server starts normally
	// for UI development. The dispatcher IS compiled on Windows.
	docs := documents.New(files, database.DB())
	// On non-Windows dev machines, winspool is unavailable so we pass nil.
	// Run() returns ErrNoBackend immediately in this case — this lets
	// the HTTP server start normally for UI development without requiring
	// a Windows VM.
	var backend dispatch.PrinterBackend
	if dispatch.WinspoolAvailable() {
		backend = dispatch.NewWinspoolBackend()
	}
	dispatchConfig := dispatch.DefaultDispatcherConfig()
	dispatchConfig.LicenseCheck = softwareLicense.Check
	dispatcher := dispatch.New(database.DB(), docs, backend,
		dispatch.NewDBQueueResolver(database.DB()),
		dispatchConfig,
	)
	go func() {
		log.Printf("step: dispatcher.Run starting (winspool=%t)", backend != nil)
		if err := dispatcher.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, dispatch.ErrNoBackend) {
			log.Printf("dispatcher.Run error: %v", err)
		}
	}()
	sweeper := autodelete.New(database.DB(), docs, autodelete.DefaultSweeperConfig())
	go func() {
		if err := sweeper.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("document cleanup: %v", err)
		}
	}()
	log.Printf("step: dispatcher started; poll_interval=%s", dispatch.DefaultDispatcherConfig().PollInterval)
	// Construct the runtime state container the /config/reload
	// endpoint mutates. Holding it at function scope keeps the
	// goroutine-safe locking close to the wiring so reloads and
	// /healthz reads see a consistent snapshot.
	state := &runtimeState{
		installationID: settings.InstallationID,
		dataDir:        dataDir,
		databasePath:   settings.DatabasePath,
		bindHost:       settings.BindHost,
		port:           settings.Port,
		publicOrigin:   settings.PublicOrigin,
		controlOrigin:  settings.ControlPlaneOrigin,
		bootstrap:      settings.Bootstrap,
	}

	var server *localserver.Server
	server = localserver.New(
		net.JoinHostPort(settings.BindHost, strconv.Itoa(settings.Port)), settings.InstallationID,
		localserver.WithStoreHealth(database),
		localserver.WithProvisioning(provisioning.LocalReadiness{DB: database.DB()}),
		localserver.WithOwner(accounts, setupToken),
		localserver.WithPricing(pricingSvc),
		localserver.WithOrders(ordersSvc),
		localserver.WithPickup(pickupSvc),
		localserver.WithReports(reportsSvc),
		localserver.WithNotifications(notifSvc),
		localserver.WithPrinters(printersSvc),
		localserver.WithIDCards(idcardsSvc),
		localserver.WithPassports(passportsSvc),
		localserver.WithTunnel(tunnelSvc),
		localserver.WithLicensing(licensingSvc),
		localserver.WithSoftwareLicense(softwareLicense),
		localserver.WithPayments(paymentsSvc),
		localserver.WithPairing(pairingSvc),
		localserver.WithBusiness(businessSvc),
		localserver.WithDispatcher(dispatcher),
		localserver.WithDB(database.DB()),
		localserver.WithFiles(files),
		localserver.WithPublicOrigin(settings.PublicOrigin),
		localserver.WithBootstrap(settings.Bootstrap),
		localserver.WithEventLog(diagnostic),
		localserver.WithConfigView(state.view),
		localserver.WithConfigReload(func(ctx context.Context) (localserver.ConfigView, localserver.ConfigView, error) {
			return state.reload(ctx, server)
		}),
	)
	kioskServer, err := kiosk.NewPlatform(database.DB(), pickupSvc, softwareLicense.Check)
	if err != nil {
		fatalf(diagnostic, "physical kiosk startup failed: %v", err)
	}
	service := supervisor.New(database, server, kioskServer)
	log.Printf("step: service created; bind=%s:%d console=%t", settings.BindHost, settings.Port, *consoleMode)
	if err := runPlatform(service, net.JoinHostPort(settings.BindHost, strconv.Itoa(settings.Port)), *consoleMode); err != nil {
		log.Printf("step: runPlatform FAILED: %v", err)
		fatalf(diagnostic, "service failed: %v", err)
	}
	log.Printf("step: runPlatform returned normally")
}

// razorpayAdapter returns the live Razorpay payment adapter
// configured for the merchant-supplied credentials. The adapter
// is intentionally stateless: the merchant's key id, key secret and
// webhook secret travel inside the encrypted provider row, not in
// the adapter. Constructing the adapter once at startup keeps the
// hot path free of allocation; per-call configuration (Basic auth,
// signature verification) reads the secret from the row.
func razorpayAdapter() *razorpay.Adapter {
	return razorpay.New()
}

// platformDiscovererAdapter wraps the SourceFunc signature with a backend
// identifier. Centralised so main.go does not need to import the printers
// adapter helpers directly.
func platformDiscovererAdapter(backend printers.Backend, source printers.SourceFunc) printers.Discoverer {
	return printers.NewRecordAdapter(backend, source)
}

// bridgeToRecordChannel translates a discover.Discovered channel into a
// printers.DiscoveredRecord channel. The translation is lossless for the
// fields both shapes share; the printers.Discovered.Error field (raw
// error) becomes printers.DiscoveredRecord.Error directly. The bridge
// goroutine exits the moment the upstream channel closes, so ctx
// cancellation only matters for the platform discoverer that feeds it.
func bridgeToRecordChannel(out chan<- printers.DiscoveredRecord) chan<- discover.Discovered {
	bridge := make(chan discover.Discovered, 16)
	go func() {
		defer close(out)
		for d := range bridge {
			rec := printers.DiscoveredRecord{
				Backend:      d.Backend,
				QueueName:    d.QueueName,
				DisplayName:  d.DisplayName,
				DriverName:   d.DriverName,
				URI:          d.URI,
				Location:     d.Location,
				IsDefault:    d.IsDefault,
				Capabilities: d.Capabilities,
				Status:       d.Status,
				LastSeenAt:   d.LastSeenAt.Unix(),
				Error:        d.Error,
			}
			select {
			case out <- rec:
			default:
				// Downstream is stalled; drop the bridge entry so
				// the platform discoverer is not held up. The
				// printers service back-pressures via its own
				// internal queue.
				return
			}
		}
	}()
	return bridge
}

// registerPrinterDiscoverers wires the Windows-only printer discoverers
// (Win32 EnumPrinters on the local spooler + IPP for any network endpoint
// + Bluetooth for paired BPP devices) into the printer service so the
// dashboard learns about every printer the merchant's PC exposes without
// any manual configuration.
//
// Every preference the dashboard displays — paper size, tray, duplex,
// colour, copies, media type, orientation — is read from the driver's
// capability snapshot (Win32 DeviceCapabilitiesW or IPP attributes), not
// from any hard-coded list in the runtime. New printers join the
// dashboard within one poll cycle (5 s on Windows, 8 s for IPP/Bluetooth).
// Failed discoverers are silently retried on the next tick; the registration
// loop never blocks waiting on a slow backend, so a degraded printer
// does not stop the rest of the UI.
func registerPrinterDiscoverers(svc *printers.Service) {
	// Win32 EnumPrinters + DeviceCapabilities. Local spooler and
	// any USB, network-shared, or Bluetooth-over-spooler printers.
	svc.RegisterDiscoverer(platformDiscovererAdapter(printers.BackendWindows, func(ctx context.Context, out chan<- printers.DiscoveredRecord) {
		discover.NewWindows().Watch(ctx, bridgeToRecordChannel(out))
	}))
	// IPP discoverer probes configured URIs and browses the local
	// network via DNS-SD for _ipp._tcp.local. The dashboard exposes
	// it via the "Add printer" form where the merchant types
	// ipp://printer/ipp/print.
	svc.RegisterDiscoverer(platformDiscovererAdapter(printers.BackendIPP, func(ctx context.Context, out chan<- printers.DiscoveredRecord) {
		discover.NewIPP().Watch(ctx, bridgeToRecordChannel(out))
	}))
	// Bluetooth discoverer enumerates paired Bluetooth devices that
	// advertise the Bluetooth Print Service (BPP, UUID 0x1122). Returns
	// nil when no Bluetooth adapter is present, so we guard with a nil
	// check before registering.
	if bt := discover.NewBluetooth(); bt != nil {
		svc.RegisterDiscoverer(platformDiscovererAdapter(printers.BackendBluetooth, func(ctx context.Context, out chan<- printers.DiscoveredRecord) {
			bt.Watch(ctx, bridgeToRecordChannel(out))
		}))
	}
}

// ippDiscoverer returns an IPP discoverer. Indirected through a small
// helper so a future per-platform tweak (e.g. WinHTTP defaults on
// Windows) only touches this one site.
func ippDiscoverer() discover.Discoverer {
	return discover.NewIPP()
}

// fatalf writes a fatal-level message to the supplied eventlog
// writer, then exits the process with status 1. The writer is
// always closed before the process exits so the operator can read
// the last message via Get-EventLog on Windows or by tailing
// dataDir/logs/runtime.log on every other platform.
func fatalf(diagnostic eventlog.Writer, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	_ = diagnostic.Error(message)
	log.Print(message)
	if diagnostic != nil {
		_ = diagnostic.Close()
	}
	os.Exit(1)
}

// stderrOnlyWriter is the fallback used when the diagnostic writer
// could not be opened at all. It records nothing; the regular log
// package already streams to stderr.
type stderrOnlyWriter struct{}

func (stderrOnlyWriter) Info(string) error    { return nil }
func (stderrOnlyWriter) Warning(string) error { return nil }
func (stderrOnlyWriter) Error(string) error   { return nil }
func (stderrOnlyWriter) Close() error         { return nil }

// min returns the smaller of two ints without importing the builtin
// (which would clash with the existing usage in this file).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// bootstrapInstallationIdentity makes sure the installation has a
// stable, persisted installation id before the rest of the runtime
// runs. The id is derived from the public key of the Ed25519 device
// key pair (see licensing.InstallationID). On a fresh installation
// we generate and persist the key pair on the spot; on an upgrade we
// honour the existing row so the identifier survives a MajorUpgrade.
//
// The helper is conservative: if config.env already carries a
// non-placeholder installation id, we compare it to the one derived
// from the persisted key pair and refuse to start when they
// disagree. Silently rewriting a mismatch would let a config drift
// rotate the device identity behind the operator's back, which is
// the exact failure mode the encrypted-on-disk key pair exists to
// prevent.
//
// After we know the id we write it into config.env if (and only if)
// the file currently contains the installer's placeholder. The
// update uses an atomic write that preserves every other line in the
// file so an operator's hand-tuned values survive an upgrade intact.
func bootstrapInstallationIdentity(ctx context.Context, dataDirectory string, db *sql.DB, files *localfiles.Files) error {
	if files == nil {
		return fmt.Errorf("files handle is required")
	}
	licensingSvc, err := licensing.New(db, dataDirectory)
	if err != nil {
		return fmt.Errorf("init licensing service: %w", err)
	}
	publicKey, err := licensingSvc.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("ensure installation key: %w", err)
	}
	installationID := licensing.InstallationID(publicKey)

	existing, err := readInstallationIDFromConfig(dataDirectory)
	if err != nil {
		return fmt.Errorf("read installation id from config: %w", err)
	}
	switch existing {
	case "":
		// File absent or PC_INSTALLATION_ID missing entirely —
		// rewrite with the real id so the runtime can find it.
		if err := replaceEnvValue(dataDirectory, configparser.ConfigFileName,
			"PC_INSTALLATION_ID", configparser.PlaceholderInstallationID, installationID); err != nil {
			return fmt.Errorf("persist installation id: %w", err)
		}
	case configparser.PlaceholderInstallationID:
		// Fresh install or an upgrade that re-shipped the placeholder.
		// Write the real id back without touching any other line.
		if err := replaceEnvValue(dataDirectory, configparser.ConfigFileName,
			"PC_INSTALLATION_ID", configparser.PlaceholderInstallationID, installationID); err != nil {
			return fmt.Errorf("persist installation id: %w", err)
		}
	default:
		if existing != installationID {
			// The encrypted key pair on disk disagrees with the
			// value in config.env. Refuse to start so the operator
			// can inspect the drift instead of silently rotating
			// the device identity.
			return fmt.Errorf("config installation id %q does not match the persisted key pair %q; restore config.env from backup or contact support", existing, installationID)
		}
	}
	return nil
}

// readInstallationIDFromConfig returns the value of the
// PC_INSTALLATION_ID line in config.env, or "" when the file does
// not exist or the line is missing. The function never errors on a
// missing file because the bootstrap helper treats a missing file as
// a first-start scenario.
func readInstallationIDFromConfig(dataDirectory string) (string, error) {
	path := filepath.Join(dataDirectory, configparser.ConfigFileName)
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read config file: %w", err)
	}
	for _, line := range splitConfigLines(string(contents)) {
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			continue
		}
		if strings.TrimSpace(line[:separator]) != "PC_INSTALLATION_ID" {
			continue
		}
		return strings.TrimSpace(line[separator+1:]), nil
	}
	return "", nil
}

// replaceEnvValue rewrites a single KEY=VALUE line in `file`, only
// when the existing value matches `previous`. Every other line is
// preserved. The function tolerates a missing file (it writes a
// minimal one) and never overwrites an unrecognised existing value,
// so a MajorUpgrade that lands on a merchant's tuned file does not
// silently overwrite the merchant's value.
//
// The write is atomic: the new contents are first written to a
// sibling temp file, fsync'd, then renamed over the original so a
// crash mid-write cannot leave config.env half-written. The mode is
// 0o600 because the file contains the installation id.
func replaceEnvValue(dataDirectory, file, key, previous, next string) error {
	path := filepath.Join(dataDirectory, file)
	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config file: %w", err)
	}
	lines := splitConfigLines(string(contents))
	written := false
	found := false
	for i, line := range lines {
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			continue
		}
		if strings.TrimSpace(line[:separator]) != key {
			continue
		}
		found = true
		value := strings.TrimSpace(line[separator+1:])
		// Only rewrite the line when the operator left it at the
		// installer's placeholder (or never set it at all). Any
		// other value belongs to the merchant and must survive an
		// upgrade untouched.
		if value == previous || value == "" {
			lines[i] = key + "=" + next
			written = true
		}
	}
	if !found {
		// File had no PC_INSTALLATION_ID entry at all (e.g. an
		// operator stripped the file). Add it at the end so the
		// runtime can find it.
		lines = append(lines, key+"="+next)
		written = true
	}
	if !written {
		// Real value was present; nothing to do.
		return nil
	}
	serialized := strings.Join(lines, "\n") + "\n"
	return atomicWriteFile(path, []byte(serialized), 0o600)
}

// atomicWriteFile writes the supplied bytes to path via a sibling
// temp file so an interrupted write cannot leave the destination in
// a half-written state. The temp file lives in the same directory
// as the destination so os.Rename remains atomic on both POSIX and
// Windows; the renamed file is fsync'd before the rename so the new
// contents survive a power loss before the directory entry is
// committed.
func atomicWriteFile(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".config.env.*")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	temporaryName := temporary.Name()
	success := false
	defer func() {
		_ = temporary.Close()
		if !success {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(temporaryName, mode); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("rename temp config: %w", err)
	}
	success = true
	return nil
}

// splitConfigLines splits a config file's contents into a slice of
// lines while preserving empty lines and trailing comments. The
// function does not interpret the file; it merely returns the raw
// lines so replaceEnvValue can re-serialize them after editing the
// single KEY=VALUE it owns.
func splitConfigLines(raw string) []string {
	if raw == "" {
		return []string{}
	}
	// Normalise line endings — Windows tools sometimes write CRLF.
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimSuffix(raw, "\n")
	return strings.Split(raw, "\n")
}

// ensureBootstrapConfig creates or updates config.env inside dataDir
// so that config.Load can find every required key on a completely
// fresh install where the operator has never run the setup wizard.
//
// The function is idempotent: if config.env already exists it leaves
// it untouched. If the file does not exist it writes a minimal one
// containing only the keys that config.Load requires, all set to
// values that make Bootstrap=true so the server refuses public
// traffic until the wizard completes.
func ensureBootstrapConfig(dataDir, dbPath string) error {
	configPath := filepath.Join(dataDir, configparser.ConfigFileName)

	// Log the path we're about to write so we can diagnose silent failures.
	log.Printf("ensureBootstrapConfig: dataDir=%s configPath=%s", dataDir, configPath)

	// If the operator has already written a complete config.env with all
	// required keys, leave it exactly as-is so the bootstrap flow does not
	// silently overwrite operator-supplied values.
	if existing, err := configparser.Load(dataDir, os.Getenv); err == nil {
		if strings.TrimSpace(existing["PC_DATABASE_PATH"]) != "" {
			log.Printf("ensureBootstrapConfig: config.env already has PC_DATABASE_PATH — nothing to do")
			return nil // file exists and is complete — nothing to do
		}
		log.Printf("ensureBootstrapConfig: config.env exists but PC_DATABASE_PATH is empty — will overwrite with bootstrap values")
	} else if !os.IsNotExist(err) {
		// Some other stat error (permission, corruption, etc.) — log it
		// and continue: the WriteFile below will surface a write error if
		// the file is genuinely inaccessible.
		log.Printf("ensureBootstrapConfig: configparser.Load returned non-NotExist error: %v", err)
	}

	// Fresh install: write a minimal config.env that satisfies
	// config.Load's required keys. The placeholder values below
	// are intentionally the same strings configparser uses so that
	// config.Load produces Bootstrap=true and the server refuses
	// public traffic until the setup wizard runs.
	lines := []string{
		"# Bootstrap configuration — replace values via the local setup wizard",
		"# or by editing this file. The runtime accepts placeholder values",
		"# at startup and will refuse public traffic until the wizard completes.",
		fmt.Sprintf("PC_INSTALLATION_ID=%s", configparser.PlaceholderInstallationID),
		fmt.Sprintf("PC_DATABASE_PATH=%s", dbPath),
		fmt.Sprintf("PC_PUBLIC_ORIGIN=%s", configparser.PlaceholderPublicOrigin),
		fmt.Sprintf("PC_CONTROL_PLANE_ORIGIN=https://replace-me.invalid"),
	}
	content := strings.Join(lines, "\n") + "\n"
	log.Printf("ensureBootstrapConfig: writing config.env:\n%s", content)
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		log.Printf("ensureBootstrapConfig: WriteFile FAILED: %v", err)
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	log.Printf("ensureBootstrapConfig: WriteFile succeeded")
	return nil
}
