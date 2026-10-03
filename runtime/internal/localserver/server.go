package localserver

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/eventlog"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pairing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/reports"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// dashboardAssets is replaced with the production Vite build in GitHub Actions.
//
//go:embed web/*
var dashboardAssets embed.FS

// Server exposes the loopback-only local API and dashboard shell.
type Server struct {
	pickup         *pickup.Service
	address        string
	installationID string
	httpServer     *http.Server
	listener       net.Listener
	mu             sync.Mutex
	owner          *owner.Service
	pricing        *pricing.Service
	orders         *orders.Service
	reports        *reports.Service
	notifications  *notifications.Service
	printers       *printers.Service
	dispatcher     *dispatch.Dispatcher
	idcards        *idcards.Service
	passports      *passport.Service
	tunnel         *tunnel.Service
	licensing      *licensing.Service
	licenseGate    *licensegate.Client
	payments       *payments.Service
	pairing        *pairing.Service
	business       *business.Service
	setupToken     string
	publicOrigin   string
	bootstrap      bool
	db             *sql.DB
	files          *localfiles.Files
	diagnostic     eventlog.Writer
	configView     ConfigViewFunc
	configReload   ConfigReloadFunc
	storeHealth    interface{ Ready(context.Context) error }
	provisioning   interface {
		Status(context.Context) (provisioning.StatusResult, error)
		ProductionReady(context.Context) (bool, error)
	}
}

type Option func(*Server)

func WithStoreHealth(health interface{ Ready(context.Context) error }) Option {
	return func(server *Server) { server.storeHealth = health }
}

func WithProvisioning(state interface {
	Status(context.Context) (provisioning.StatusResult, error)
	ProductionReady(context.Context) (bool, error)
}) Option {
	return func(server *Server) { server.provisioning = state }
}

func WithDB(db *sql.DB) Option {
	return func(server *Server) { server.db = db }
}

func WithFiles(files *localfiles.Files) Option {
	return func(server *Server) { server.files = files }
}

func WithOrders(ordersSvc *orders.Service) Option {
	return func(server *Server) { server.orders = ordersSvc }
}

func WithReports(reportsSvc *reports.Service) Option {
	return func(server *Server) { server.reports = reportsSvc }
}

func WithNotifications(notifSvc *notifications.Service) Option {
	return func(server *Server) { server.notifications = notifSvc }
}

func WithPrinters(printersSvc *printers.Service) Option {
	return func(server *Server) { server.printers = printersSvc }
}

// WithDispatcher wires the print dispatcher. Used by the dashboard to
// surface dispatcher health (LastError, queue resolution status). The
// dispatcher's run loop is owned by main.go, not the server.
func WithDispatcher(d *dispatch.Dispatcher) Option {
	return func(server *Server) { server.dispatcher = d }
}

// WithIDCards wires the ID Card Studio service. Required for the studio's
// owner endpoints; safe to omit in builds that disable the studio.
func WithIDCards(svc *idcards.Service) Option {
	return func(server *Server) { server.idcards = svc }
}

// WithPassports wires the Passport Photo Studio service. Required for the
// studio's owner endpoints; safe to omit in builds that disable the studio.
func WithPassports(svc *passport.Service) Option {
	return func(server *Server) { server.passports = svc }
}

// WithTunnel wires the outbound tunnel service. Required for the Phase 7
// dashboard surface; safe to omit in builds that disable the public tunnel.
func WithTunnel(svc *tunnel.Service) Option {
	return func(server *Server) { server.tunnel = svc }
}

func New(address, installationID string, options ...Option) *Server {
	server := &Server{address: address, installationID: installationID}
	for _, option := range options {
		option(server)
	}
	server.httpServer = &http.Server{
		Addr:              address,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server
}

// WithPublicOrigin records the canonical public origin the tunnel exposes
// the portal at (for example "https://shop.example.com"). When set, the
// portal guard accepts X-Forwarded-* headers from loopback connections
// and validates same-origin requests against the configured public
// origin rather than the local HTTP listener. When unset the portal
// guard behaves exactly like the loopback-only guard it was before the
// tunnel existed.
func WithPublicOrigin(origin string) Option {
	return func(server *Server) {
		trimmed := strings.TrimRight(strings.TrimSpace(origin), "/")
		server.publicOrigin = trimmed
	}
}

// WithBootstrap flips the server into bootstrap mode. In bootstrap
// mode every public-facing endpoint returns 503 until the operator
// completes the local setup wizard (the install id is still the
// MSI placeholder, the public origin still points at *.invalid, or
// any provisioning gate is incomplete). The setup, health and
// ready endpoints stay reachable so the operator can read the
// runtime state without a circular "service won't start" failure.
func WithBootstrap(bootstrap bool) Option {
	return func(server *Server) { server.bootstrap = bootstrap }
}

// WithEventLog wires a diagnostic writer so the server can report
// configuration reload outcomes, denied requests, and other events
// through the same diagnostic channel the main entry point already
// uses. The writer is optional; a nil value disables diagnostic
// logging and the server continues to function normally.
func WithEventLog(writer eventlog.Writer) Option {
	return func(server *Server) {
		if writer != nil {
			server.diagnostic = writer
		}
	}
}

func (s *Server) Name() string { return "local-http" }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerOwner(mux)
	s.registerMonitor(mux)
	s.registerPricing(mux)
	s.registerOperators(mux)
	s.registerOwnerOrders(mux)
	s.registerOwnerStatusAndInvoices(mux)
	s.registerOwnerReports(mux)
	s.registerOwnerNotifications(mux)
	s.registerOwnerPrinters(mux)
	s.registerOwnerIDCards(mux)
	s.registerOwnerPassports(mux)
	s.registerOwnerTunnel(mux)
	s.registerOwnerLicence(mux)
	s.registerOwnerPayments(mux)
	s.registerOwnerPairing(mux)
	s.registerOwnerConfig(mux)
	s.registerOwnerBusiness(mux)
	s.registerShopSettings(mux)
	s.registerPortal(mux)
	s.registerPortalPickup(mux)
	s.registerPublicPairing(mux)
	s.registerTrayLocal(mux)
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(response).Encode(map[string]string{
			"status": "ok", "service": "print-catalyst-on-premise",
			"installationId": s.installationID,
		})
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		if reason := s.notReadyReason(request.Context()); reason != "" {
			writeUnavailable(response, reason)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /api/v1/orders", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		if reason := s.notReadyReason(request.Context()); reason != "" {
			response.Header().Set("Retry-After", "30")
			writeUnavailable(response, reason)
			return
		}
		response.WriteHeader(http.StatusNotImplemented)
		_, _ = response.Write([]byte(`{"error":"order intake is not implemented in this phase"}`))
	})
	mux.HandleFunc("GET /api/v1/setup/status", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		if !isLoopbackClient(request.RemoteAddr) {
			http.Error(response, `{"error":"local setup access required"}`, http.StatusForbidden)
			return
		}
		if s.owner != nil {
			if !s.localOwnerRequest(response, request) {
				return
			}
			exists, err := s.owner.Exists(request.Context())
			if err != nil {
				ownerError(response, err)
				return
			}
			if exists {
				if _, _, ok := s.ownerSession(response, request); !ok {
					return
				}
			}
		}
		if s.provisioning == nil {
			http.Error(response, `{"error":"setup state unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		status, err := s.provisioning.Status(request.Context())
		if err != nil {
			http.Error(response, `{"error":"setup state unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(response).Encode(status)
	})
	assets, err := fs.Sub(dashboardAssets, "web")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(assets))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		static.ServeHTTP(w, r)
	}))
	return s.enforceSoftwareLicense(mux)
}

func (s *Server) notReadyReason(ctx context.Context) string {
	if s.licenseGate != nil {
		if err := s.licenseGate.Check(ctx); err != nil {
			return err.Error()
		}
	}
	s.mu.Lock()
	bootstrap := s.bootstrap
	s.mu.Unlock()
	if bootstrap {
		return "installation is unconfigured; complete the local setup wizard before public endpoints will accept traffic"
	}
	if s.storeHealth == nil || s.provisioning == nil {
		return "runtime dependencies unavailable"
	}
	if err := s.storeHealth.Ready(ctx); err != nil {
		return "local database unavailable"
	}
	ready, err := s.provisioning.ProductionReady(ctx)
	if err != nil {
		return "setup state unavailable"
	}
	if !ready {
		status, err := s.provisioning.Status(ctx)
		if err == nil && status.Next != "" {
			return "setup incomplete: finish " + string(status.Next) + " setup in the shop dashboard"
		}
		return "setup incomplete"
	}
	return ""
}

func writeUnavailable(response http.ResponseWriter, reason string) {
	response.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(response).Encode(map[string]string{"status": "not_ready", "reason": reason})
}

func isLoopbackClient(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *Server) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		if s.diagnostic != nil {
			_ = s.diagnostic.Error(fmt.Sprintf("local server failed to bind %s: %v", s.address, err))
		}
		return err
	}
	s.listener = listener
	if s.diagnostic != nil {
		// Surface the listener address through the diagnostic
		// channel so the operator can confirm the service is
		// accepting connections from the Event Log or the
		// protected runtime.log file. Without this, a successful
		// Start would still leave the operator guessing whether
		// the dashboard URL they have is the correct one.
		_ = s.diagnostic.Info(fmt.Sprintf("local server listening on http://%s/", listener.Addr().String()))
	}
	go func() {
		// httpServer.Serve returns ErrServerClosed when Shutdown
		// is called normally; every other error means the
		// listener died unexpectedly and the dashboard is no
		// longer reachable. Log the unexpected case so the
		// operator can find it in the Event Log instead of
		// discovering the dashboard is dead by clicking the
		// Start-menu shortcut.
		if serveErr := s.httpServer.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			if s.diagnostic != nil {
				_ = s.diagnostic.Error(fmt.Sprintf("local server stopped accepting connections: %v", serveErr))
			}
		}
	}()
	return nil
}

// SetRuntimeConfig swaps the mutable fields /config/reload may
// change. The bind host / port and listener are intentionally NOT
// rotated here; those require a service restart and the
// /config/reload endpoint returns the unchanged values so the
// dashboard can render the discrepancy.
func (s *Server) SetRuntimeConfig(installationID, publicOrigin string, bootstrap bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if installationID != "" {
		s.installationID = installationID
	}
	s.publicOrigin = strings.TrimRight(strings.TrimSpace(publicOrigin), "/")
	s.bootstrap = bootstrap
}

// PublicOrigin returns the configured public HTTPS origin the tunnel
// exposes (e.g. "https://shop-randomid.trycloudflare.com"). Returns
// the empty string when no tunnel has been configured yet.
func (s *Server) PublicOrigin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publicOrigin
}

func (s *Server) Ready(ctx context.Context) error {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return fmt.Errorf("listener is not open")
	}
	address := listener.Addr().String()
	// TCP-loop the listener from inside the same process so a
	// "Running" report means the kernel actually accepted a
	// connection — a bound-but-broken socket would otherwise be
	// reported as ready and the dashboard would silently 503.
	conn, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return fmt.Errorf("listener %s is not accepting connections: %w", address, err)
	}
	return conn.Close()
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	listener := s.listener
	s.listener = nil
	s.mu.Unlock()
	if listener == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}
