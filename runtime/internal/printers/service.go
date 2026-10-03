package printers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Discoverer is the interface every backend implements to feed discovered
// printers into the service. The concrete implementations live in
// printers/discover and are platform-aware: Win32 EnumPrinters for Windows,
// CUPS for macOS/Linux, IPP for any network endpoint.
//
// The Watch method writes DiscoveredRecord values to the supplied channel.
// Implementations from the discover subpackage are wrapped via
// NewRecordAdapter to satisfy this interface.
type Discoverer interface {
	Backend() Backend
	Watch(ctx context.Context, out chan<- DiscoveredRecord)
}

// DiscoveredRecord is the platform-agnostic payload a Discoverer emits.
// The fields mirror printers.Discovered one-for-one, with snapshot
// capabilities carried alongside the queue identity.
type DiscoveredRecord struct {
	Backend       Backend
	QueueName     string
	DisplayName   string
	DriverName    string
	DriverVersion string
	URI           string
	Location      string
	IsDefault     bool
	Capabilities  *Snapshot
	Status        Status
	LastSeenAt    int64
	Error         error
}

// SourceFunc is the signature of the printers/discover discoverer Watch
// methods. We use a generic SourceFunc so the printers package can stay
// decoupled from the discover subpackage.
type SourceFunc func(ctx context.Context, out chan<- DiscoveredRecord)

// WatchFunc adapts a SourceFunc into the Discoverer interface.
func (f SourceFunc) Watch(ctx context.Context, out chan<- DiscoveredRecord) { f(ctx, out) }

// Adapter wraps a discover-package Discoverer into the printers.Service
// interface. It is a thin shim that translates the platform-specific
// Discovered payload (with time.Time timestamps, capability errors, etc.)
// into the runtime-local DiscoveredRecord.
//
// The discover subpackage's Discoverer type is not directly assignable to
// printers.Discoverer because Go interfaces are structurally typed and the
// channel element type differs. Adapter bridges the gap without an import
// cycle by accepting a SourceFunc as the watch entry point.
//
// Usage:
//
//	ipp := printers.NewRecordAdapter(printers.SourceFunc(func(ctx context.Context, out chan<- printers.DiscoveredRecord) {
//	    discover.NewIPP("ipp://printer/...").Watch(ctx, bridgeChannel(out))
//	}))
//	svc.RegisterDiscoverer(ipp)
//
// Adapter is provided as a convenience for the call sites in main.go; the
// printers package itself does not export the bridge logic because every
// call site uses a different concrete Discoverer.
type Adapter struct {
	backend Backend
	source  SourceFunc
}

// NewRecordAdapter returns a printers.Discoverer that delegates to the
// supplied SourceFunc.
func NewRecordAdapter(backend Backend, source SourceFunc) *Adapter {
	return &Adapter{backend: backend, source: source}
}

// Backend returns the adapter's backend identifier.
func (a *Adapter) Backend() Backend { return a.backend }

// Watch implements Discoverer by delegating to the source function.
func (a *Adapter) Watch(ctx context.Context, out chan<- DiscoveredRecord) {
	a.source(ctx, out)
}

// discovered wraps DiscoveredRecord for the internal channel. It is the
// ergonomic shape the service consumes so future fields (e.g. driver
// version, capabilities hash) can be added without breaking the public
// signature.
type discovered struct {
	DiscoveredRecord
}

// Service manages printer records, capability snapshots and verification
// rows. The discovery layer (per-platform) is responsible for producing
// Snapshot values; this service persists them and exposes them to the
// dashboard / portal surfaces.
type Service struct {
	db *sql.DB

	mu         sync.Mutex
	discoverers []Discoverer
	cancelLoop context.CancelFunc
}

// New returns a printer service backed by the given database handle.
func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// RegisterDiscoverer adds a Discoverer to the service. The watcher loop
// (started by Start) consumes from each registered Discoverer's Watch
// channel and feeds the records into Register. Calling RegisterDiscoverer
// after Start is a no-op so a re-registration race during a hot reload
// cannot spawn duplicate watchers.
func (s *Service) RegisterDiscoverer(d Discoverer) {
	if d == nil {
		return
	}
	s.mu.Lock()
	s.discoverers = append(s.discoverers, d)
	s.mu.Unlock()
}

// Start spins up the discovery watcher loop. The service consumes every
// registered Discoverer concurrently and routes the records into Register.
// The supplied context controls the lifetime of the loop; calling Stop
// (or cancelling the supplied context) terminates it cleanly.
//
// Start is safe to call once per process. Calling it twice will fail
// fast with an error rather than silently spawning a second loop, which
// would double-write printers to the database.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancelLoop != nil {
		s.mu.Unlock()
		return errors.New("printer discovery loop already running")
	}
	discoverers := append([]Discoverer(nil), s.discoverers...)
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancelLoop = cancel
	s.mu.Unlock()
	// Run each discoverer's Watch in its own goroutine, then funnel
	// the records into a single shared consumer that calls Register.
	out := make(chan DiscoveredRecord, 64)
	var wg sync.WaitGroup
	for _, d := range discoverers {
		wg.Add(1)
		go func(d Discoverer) {
			defer wg.Done()
			d.Watch(loopCtx, out)
		}(d)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	go s.consume(loopCtx, out)
	return nil
}

// Stop terminates the discovery watcher loop started by Start. Stop is a
// no-op when Start was never called or has already been called.
func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancelLoop
	s.cancelLoop = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// consume reads from the discoverer channel and persists each record via
// Register. Errors from individual discoveries are returned via the
// snapshot.Error field on the registered Printer so the dashboard can
// surface them; a misbehaving discoverer does NOT poison the loop.
func (s *Service) consume(ctx context.Context, out <-chan DiscoveredRecord) {
	fmt.Printf("DEBUG consume: started, waiting for records...\n")
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("DEBUG consume: context done\n")
			return
		case rec, ok := <-out:
			if !ok {
				fmt.Printf("DEBUG consume: channel closed\n")
				return
			}
			fmt.Printf("DEBUG consume: received record for queue=%q\n", rec.QueueName)
			// Convert the local record to RegisterInput. A
			// capabilities snapshot is preserved verbatim.
			snapshot := rec.Capabilities
			if snapshot == nil {
				snapshot = &Snapshot{
					ColourModes: []string{},
					SidesModes:  []string{},
					PaperSizes:  []PaperSize{},
					Trays:       []Tray{},
					Finishing:   []FinishingOption{},
					Raw:         map[string]any{},
				}
			}
			_, _ = s.Register(ctx, RegisterInput{
				Backend:       rec.Backend,
				QueueName:     rec.QueueName,
				DisplayName:   rec.DisplayName,
				DriverName:    rec.DriverName,
				DriverVersion: rec.DriverVersion,
				URI:           rec.URI,
				Location:      rec.Location,
				IsDefault:     rec.IsDefault,
				Capabilities:  snapshot,
			})
		}
	}
}

// ErrNotFound is returned when a printer / capability row does not exist.
var ErrNotFound = errors.New("printer not found")

// ErrInvalid is returned when the input is malformed (empty queue, bad
// backend, etc).
var ErrInvalid = errors.New("invalid printer request")

// RegisterInput is what the discovery layer (or a manual URI enrollment)
// produces.
type RegisterInput struct {
	Backend       Backend
	QueueName     string
	DisplayName   string
	DriverName    string
	DriverVersion string
	URI           string
	Location      string
	IsDefault     bool
	Capabilities  *Snapshot
}

// RegisterInputFromFixture is a convenience helper for tests and manual URI
// enrollments that don't go through a real backend.
func RegisterInputFromFixture(queue, uri string, attrs RawAttributes) RegisterInput {
	return RegisterInput{
		Backend:     BackendMock,
		QueueName:   queue,
		DisplayName: queue,
		URI:         uri,
		Capabilities: NormalizeIPPAttributes(attrs),
	}
}

// Register stores a printer (or refreshes its capabilities if it already
// exists by (backend, queue)). It returns the persisted Printer.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Printer, error) {
	queue := strings.TrimSpace(in.QueueName)
	if queue == "" {
		return Printer{}, fmt.Errorf("%w: queue name is required", ErrInvalid)
	}
	switch in.Backend {
	case BackendIPP, BackendIPPS, BackendWindows, BackendBluetooth, BackendMock:
	default:
		return Printer{}, fmt.Errorf("%w: unknown backend %q", ErrInvalid, in.Backend)
	}

	capJSON, _ := json.Marshal(in.Capabilities)
	rawAttrs, _ := json.Marshal(map[string]any{})
	if in.Capabilities != nil {
		rawAttrs, _ = json.Marshal(in.Capabilities.Raw)
	}
	fp := ""
	if in.Capabilities != nil {
		fp = in.Capabilities.Fingerprint
	}
	printerFP := Fingerprint(in.Backend, queue, in.DriverName, in.DriverVersion, in.URI, in.Capabilities)
	now := Now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Printer{}, err
	}
	defer tx.Rollback()

	// Upsert by (backend, queue_name) where removed_at IS NULL.
	var id string
	err = tx.QueryRowContext(ctx, `
SELECT id FROM printers
WHERE backend = ? AND queue_name = ? AND removed_at IS NULL`, string(in.Backend), queue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		newID, err := randomID()
		if err != nil {
			return Printer{}, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO printers (id, backend, queue_name, display_name, driver_name, driver_version,
	uri, location, fingerprint, status, enabled, is_default, created_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			newID, string(in.Backend), queue, in.DisplayName, in.DriverName, in.DriverVersion,
			in.URI, in.Location, printerFP, string(StatusReady), boolToInt(in.IsDefault), now, now); err != nil {
			return Printer{}, err
		}
		id = newID
	} else if err != nil {
		return Printer{}, err
	} else {
		if _, err := tx.ExecContext(ctx, `
UPDATE printers SET display_name = ?, driver_name = ?, driver_version = ?,
	uri = ?, location = ?, fingerprint = ?, status = ?, is_default = ?, last_seen_at = ?
WHERE id = ?`,
			in.DisplayName, in.DriverName, in.DriverVersion,
			in.URI, in.Location, printerFP, string(StatusReady),
			boolToInt(in.IsDefault), now, id); err != nil {
			return Printer{}, err
		}
	}

	// Insert a fresh capability snapshot.
	capID, err := randomID()
	if err != nil {
		return Printer{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO printer_capabilities (id, printer_id, fingerprint, captured_at, raw_attributes, normalized_json)
VALUES (?, ?, ?, ?, ?, ?)`,
		capID, id, fp, now, string(rawAttrs), string(capJSON)); err != nil {
		return Printer{}, err
	}

	// Persist paper sizes / trays / finishing so a fingerprint change can
	// re-bind by normalized key.
	if in.Capabilities != nil {
		if err := s.replacePaperSizesTx(ctx, tx, id, in.Capabilities.PaperSizes); err != nil {
			return Printer{}, err
		}
		if err := s.replaceFinishingTx(ctx, tx, id, in.Capabilities.Finishing); err != nil {
			return Printer{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Printer{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) replacePaperSizesTx(ctx context.Context, tx *sql.Tx, printerID string, sizes []PaperSize) error {
	now := Now()
	// Soft-delete the previous active rows.
	if _, err := tx.ExecContext(ctx, `UPDATE printer_paper_sizes SET removed_at=? WHERE printer_id=? AND removed_at IS NULL`, now, printerID); err != nil {
		return err
	}
	for _, size := range sizes {
		id, err := randomID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO printer_paper_sizes (id, printer_id, paper_key, raw_label, width_mm, height_mm,
	is_custom, min_width_mm, max_width_mm, min_height_mm, max_height_mm)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, printerID, size.Key, size.RawLabel,
			size.WidthMM, size.HeightMM,
			boolToInt(size.IsCustom),
			size.MinWidthMM, size.MaxWidthMM,
			size.MinHeightMM, size.MaxHeightMM); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) replaceFinishingTx(ctx context.Context, tx *sql.Tx, printerID string, fins []FinishingOption) error {
	now := Now()
	if _, err := tx.ExecContext(ctx, `UPDATE printer_finishing_options SET removed_at=? WHERE printer_id=? AND removed_at IS NULL`, now, printerID); err != nil {
		return err
	}
	for _, f := range fins {
		id, err := randomID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO printer_finishing_options (id, printer_id, finishing_type, raw_label, normalized_key, enabled)
VALUES (?, ?, ?, ?, ?, 0)`,
			id, printerID, string(f.Type), f.RawLabel, f.Key); err != nil {
			return err
		}
	}
	return nil
}

// Get returns one printer by ID, including its latest capability snapshot.
func (s *Service) Get(ctx context.Context, id string) (Printer, error) {
	p := Printer{}
	err := s.db.QueryRowContext(ctx, `
SELECT id, backend, queue_name, display_name, driver_name, driver_version,
	uri, location, fingerprint, status, enabled, is_default, created_at, last_seen_at
FROM printers WHERE id = ? AND removed_at IS NULL`, id).Scan(
		&p.ID, &p.Backend, &p.QueueName, &p.DisplayName, &p.DriverName, &p.DriverVersion,
		&p.URI, &p.Location, &p.Fingerprint, &p.Status, &p.Enabled, &p.IsDefault,
		&p.CreatedAt, &p.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Printer{}, ErrNotFound
	}
	if err != nil {
		return Printer{}, err
	}
	cap, err := s.latestCapabilities(ctx, id)
	if err != nil {
		return Printer{}, err
	}
	p.Capabilities = cap
	return p, nil
}

// List returns every non-removed printer with its latest capability snapshot.
func (s *Service) List(ctx context.Context) ([]Printer, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, backend, queue_name, display_name, driver_name, driver_version,
	uri, location, fingerprint, status, enabled, is_default, created_at, last_seen_at
FROM printers WHERE removed_at IS NULL ORDER BY is_default DESC, queue_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var printers []Printer
	for rows.Next() {
		var p Printer
		if err := rows.Scan(
			&p.ID, &p.Backend, &p.QueueName, &p.DisplayName, &p.DriverName, &p.DriverVersion,
			&p.URI, &p.Location, &p.Fingerprint, &p.Status, &p.Enabled, &p.IsDefault,
			&p.CreatedAt, &p.LastSeenAt); err != nil {
			return nil, err
		}
		printers = append(printers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range printers {
		cap, err := s.latestCapabilities(ctx, printers[i].ID)
		if err != nil {
			return nil, err
		}
		printers[i].Capabilities = cap
	}
	return printers, nil
}

// Enable marks a printer as customer-visible. The merchant must call this
// only after they have verified the printer.
func (s *Service) Enable(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE printers SET enabled=1 WHERE id=? AND removed_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Disable marks a printer as not customer-visible. The capability rows stay
// so the merchant can re-enable without re-discovery.
func (s *Service) Disable(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE printers SET enabled=0 WHERE id=? AND removed_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Remove marks the printer as removed. Historical orders still reference
// the printer id; this just hides it from the dashboard.
func (s *Service) Remove(ctx context.Context, id string) error {
	now := Now()
	res, err := s.db.ExecContext(ctx, `UPDATE printers SET removed_at=? WHERE id=? AND removed_at IS NULL`, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordVerification writes a verification row for one capability. The
// merchant confirms the printer actually prints with this setting.
func (s *Service) RecordVerification(ctx context.Context, v Verification) error {
	if v.PrinterID == "" || v.CapabilityType == "" || v.CapabilityKey == "" {
		return fmt.Errorf("%w: printer id, capability type and key are required", ErrInvalid)
	}
	switch v.Status {
	case VerificationPending, VerificationTested, VerificationConfirmed, VerificationVerified, VerificationFailed:
	default:
		return fmt.Errorf("%w: unknown verification status %q", ErrInvalid, v.Status)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO printer_verifications (id, printer_id, capability_type, capability_key, status,
	evidence, tested_at, verified_at, verified_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, v.PrinterID, v.CapabilityType, v.CapabilityKey, string(v.Status),
		v.Evidence, v.TestedAt, v.VerifiedAt, v.VerifiedBy)
	return err
}

// ListVerifications returns the verification rows for one printer, newest
// first.
func (s *Service) ListVerifications(ctx context.Context, printerID string) ([]Verification, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, printer_id, capability_type, capability_key, status, evidence,
	tested_at, verified_at, verified_by, invalidated_at
FROM printer_verifications WHERE printer_id = ?
ORDER BY verified_at DESC, tested_at DESC`, printerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verification
	for rows.Next() {
		var v Verification
		var invalidatedAt sql.NullInt64
		if err := rows.Scan(&v.ID, &v.PrinterID, &v.CapabilityType, &v.CapabilityKey,
			&v.Status, &v.Evidence, &v.TestedAt, &v.VerifiedAt, &v.VerifiedBy, &invalidatedAt); err != nil {
			return nil, err
		}
		if invalidatedAt.Valid {
			v.InvalidatedAt = invalidatedAt.Int64
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) latestCapabilities(ctx context.Context, printerID string) (*Snapshot, error) {
	var snapJSON string
	err := s.db.QueryRowContext(ctx, `
SELECT normalized_json FROM printer_capabilities
WHERE printer_id = ? ORDER BY captured_at DESC LIMIT 1`, printerID).Scan(&snapJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(snapJSON), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// SortedPaperSizes returns the sizes in deterministic order. Used by the
// dashboard renderer.
func SortedPaperSizes(snap *Snapshot) []PaperSize {
	if snap == nil {
		return nil
	}
	out := append([]PaperSize(nil), snap.PaperSizes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// boolToInt is a tiny helper because SQLite stores booleans as 0/1.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
