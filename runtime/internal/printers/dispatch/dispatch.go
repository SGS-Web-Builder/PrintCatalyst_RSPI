// Package dispatch journals print submissions and tracks their progress.
// Production Pi dispatch requires a durable physical-kiosk pickup claim;
// payment and inherited auto-print settings alone cannot release an order.
package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
)

// OrderStatus is the subset of orders.Service statuses we react to.
const (
	OrderStatusPaid       = "paid"
	OrderStatusDispatched = "dispatched"
	OrderStatusCompleted  = "completed"
	OrderStatusFailed     = "failed"
)

// DocumentRef identifies one print job in the orders table. The
// dispatcher looks up the document blob via documents.Service.FetchAt
// and pipes it to the configured PrinterBackend.
type DocumentRef struct {
	Prepared      bool   // Final PDF with source selection and layout already applied.
	InvoiceLogo   []byte // Optional merchant PNG/JPEG, printed above the invoice.
	Invoice       bool
	InvoiceSheets *int   // Filled by the renderer after successful invoice pagination.
	Tray          string // Exact driver label; empty preserves the document's default tray.
	Pages         []int
	LineID        string
	MIMEType      string
	OrderID       string
	DocumentID    string
	StoragePath   string
	PageCount     int
	ColourMode    string
	Sides         string
	PaperSize     string
	Copies        int
	PageStart     int
	PageEnd       int
	Orientation   string // "auto" / "portrait" / "landscape"
	PagesPerSheet int    // 1, 2 or 4
}

// PrinterBackend is the abstraction over the OS print spooler. The
// production implementation wraps winspool.drv on Windows and IPP on
// macOS/Linux; tests substitute an in-memory recorder.
//
// Submit receives the raw bytes to print plus a hint of how the
// document was configured by the customer (paper size, sides, colour).
// The backend is responsible for opening a queue, writing the bytes,
// and closing the queue. The function returns nil on a successful
// spool submission (not necessarily on completion — the OS reports
// completion asynchronously via the spooler).
type PrinterBackend interface {
	// Name returns a human-readable identifier for the backend
	// (e.g. "winspool", "ipp", "mock"). Used in log lines so a
	// field operator can confirm which backend is wired.
	Name() string
	// Submit writes the document bytes to the named queue. The
	// returned jobID is a backend-local identifier; the dispatcher
	// stores it on the orders row for later cross-reference.
	Submit(ctx context.Context, queue string, content []byte, ref DocumentRef) (jobID string, err error)
	// Queues returns the queue names currently visible to the
	// backend. Used by the dispatcher's polling loop to map a
	// stored printer id back to a backend queue name.
	Queues(ctx context.Context) ([]string, error)
}

// QueueResolver maps a printer id (the persistent row id in the printers
// table) to the OS-level queue name the backend expects. The dispatch
// package owns the interface so any backend can plug in its own
// resolution strategy without a hard dependency on the printers
// package. The production main.go wires a resolver that reads from the
// printers.Service.
type QueueResolver interface {
	// QueueFor returns the spooler queue name for the supplied printer id.
	// An empty string with a nil error means "no mapping" — the dispatcher
	// will fall back to DefaultQueue.
	QueueFor(ctx context.Context, printerID string) (string, error)
	// DefaultQueue returns the merchant's default printer queue, or "" when
	// no default has been set. Used when an order does not name a specific
	// printer.
	DefaultQueue(ctx context.Context) (string, error)
}

// DispatcherConfig bundles the tunables for the dispatcher loop.
type DispatcherConfig struct {
	Prepared      *prepared.Store
	Renderer      PreparationRenderer
	RequirePickup bool // Production Pi releases require a durable physical kiosk claim.
	LicenseCheck  func(context.Context) error
	// PollInterval is how often the dispatcher queries the orders
	// table for newly-paid orders. Default: 200 ms.
	PollInterval time.Duration
	// DefaultQueue is the printer queue used when an order has no
	// explicit queue mapping (e.g. the merchant has not yet
	// assigned the order to a specific printer). Empty disables
	// dispatching such orders.
	DefaultQueue string
}

// DefaultDispatcherConfig returns the production defaults: 20 ms
// poll and no default queue. 20 ms was chosen to meet the sub-second
// SLA from payment confirmation to "first byte hits the spooler".
// The dispatcher is the hot path for instant print release; a slow
// poll interval would add unnecessary latency after the order is
// promoted to "paid".
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{PollInterval: 20 * time.Millisecond, RequirePickup: true}
}

// Dispatcher polls the orders table and submits paid orders to the
// configured printer backend.
type Dispatcher struct {
	db        *sql.DB
	docs      *documents.Service
	backend   PrinterBackend
	resolver  QueueResolver
	config    DispatcherConfig
	mu        sync.Mutex
	seen      map[string]bool // order ids already submitted (idempotency)
	lastError error
	tickMu    sync.Mutex
}

// New returns a Dispatcher. backend may be nil in tests; the loop is
// only started by Run. The resolver is optional; when nil the dispatcher
// falls back to config.DefaultQueue for every order.
func New(db *sql.DB, docs *documents.Service, backend PrinterBackend, resolver QueueResolver, config DispatcherConfig) *Dispatcher {
	if config.PollInterval <= 0 {
		config.PollInterval = 20 * time.Millisecond
	}
	return &Dispatcher{
		db:       db,
		docs:     docs,
		backend:  backend,
		resolver: resolver,
		config:   config,
		seen:     map[string]bool{},
	}
}

// NewWithDefaultQueue is a convenience wrapper for tests and call
// sites that don't need the per-printer resolver. Equivalent to
// New(db, docs, backend, nil, config).
func NewWithDefaultQueue(db *sql.DB, docs *documents.Service, backend PrinterBackend, config DispatcherConfig) *Dispatcher {
	return New(db, docs, backend, nil, config)
}

// Run is the dispatcher's main loop. It returns when ctx is cancelled.
// The loop is restartable: calling Run twice on the same Dispatcher is
// safe; the second invocation will simply process any orders the first
// one missed.
func (d *Dispatcher) Run(ctx context.Context) error {
	if d.backend == nil {
		return errors.New("dispatcher has no printer backend configured")
	}
	if d.docs == nil {
		return errors.New("dispatcher has no document service configured")
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); d.monitorJobs(monitorCtx) }()
	defer func() { stopMonitor(); <-monitorDone }()
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()
	d.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

// tick journals each line before calling the OS. An interrupted submission is
// deliberately held for operator reconciliation instead of printing twice.
func (d *Dispatcher) tick(ctx context.Context) {
	d.tickMu.Lock()
	defer d.tickMu.Unlock()
	var mode string
	if err := d.db.QueryRowContext(ctx, "SELECT auto_print_mode FROM business_settings WHERE singleton=1").Scan(&mode); err != nil {
		d.setLastError(err)
		return
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT id, primary_printer_id, service_id, status FROM orders
WHERE submitted_at > 0 AND (
 (status='paid' AND (? != 'off' OR print_requested=1)) OR
 (status='pending_payment' AND (?='all_documents' OR print_requested=1)))
AND EXISTS (SELECT 1 FROM order_lines l WHERE l.order_id=orders.id)
ORDER BY submitted_at, id`, mode, mode)
	if err != nil {
		d.setLastError(err)
		return
	}
	var batches []dispatchBatch
	for rows.Next() {
		var b dispatchBatch
		if err = rows.Scan(&b.id, &b.printer, &b.service, &b.status); err != nil {
			break
		}
		batches = append(batches, b)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		d.setLastError(err)
		return
	}
	if err := licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
		d.setLastError(err)
		return
	}
	if d.config.RequirePickup {
		filtered := batches[:0]
		for _, b := range batches {
			if d.checkPickup(ctx, b.id) == nil {
				filtered = append(filtered, b)
			}
		}
		batches = filtered
	}
	if err := d.planSeparators(ctx, batches); err != nil {
		d.setLastError(err)
		return
	}
	var failures []error
	for _, b := range batches {
		d.mu.Lock()
		skip := d.seen[b.id]
		d.mu.Unlock()
		if skip {
			continue
		}
		if err := d.dispatchOrder(ctx, b.id, b.printer, b.service, b.status); err != nil {
			failures = append(failures, err)
		}
	}
	d.setLastError(errors.Join(failures...))
}

func (d *Dispatcher) dispatchOrder(ctx context.Context, id, printer, service, status string) error {
	if err := d.checkPickup(ctx, id); err != nil {
		return err
	}
	if err := licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
		return err
	}
	if d.config.Prepared != nil {
		return d.dispatchPrepared(ctx, id, printer, service)
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT l.id,l.document_id,COALESCE(doc.storage_path,''),COALESCE(doc.page_count,0),
 COALESCE(doc.mime_type,''),l.colour_mode,l.sides,l.paper_size,l.copies,
 l.page_range_start,l.page_range_end,l.orientation,l.pages_per_sheet,l.selected_pages_json,
 COALESCE(j.state,''),COALESCE(j.error,'')
FROM order_lines l LEFT JOIN documents doc ON doc.id=l.document_id
LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=? ORDER BY l.id`, id)
	if err != nil {
		return err
	}
	type line struct {
		ref           DocumentRef
		state, detail string
	}
	var lines []line
	for rows.Next() {
		v := line{}
		var selectedJSON string
		v.ref.OrderID = id
		err = rows.Scan(&v.ref.LineID, &v.ref.DocumentID, &v.ref.StoragePath, &v.ref.PageCount,
			&v.ref.MIMEType, &v.ref.ColourMode, &v.ref.Sides, &v.ref.PaperSize, &v.ref.Copies,
			&v.ref.PageStart, &v.ref.PageEnd, &v.ref.Orientation, &v.ref.PagesPerSheet, &selectedJSON, &v.state, &v.detail)
		if err != nil {
			break
		}
		v.ref.Pages, err = pageselection.Decode(selectedJSON)
		if err != nil {
			break
		}
		lines = append(lines, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return fmt.Errorf("order %s has no print lines", id)
	}
	for _, l := range lines {
		if l.state == "submitted" {
			continue
		}
		if l.state != "" {
			return fmt.Errorf("order %s line %s: %s; review before retry: %s", id, l.ref.LineID, l.state, l.detail)
		}
		var queue string
		err := d.db.QueryRowContext(ctx, "SELECT queue_name FROM print_routes WHERE line_id=?", l.ref.LineID).Scan(&queue)
		if err == sql.ErrNoRows {
			queue, err = d.resolveQueue(ctx, printer, service, l.ref)
		} else if err == nil {
			current, resolveErr := d.resolveQueue(ctx, printer, service, l.ref)
			if resolveErr != nil {
				err = resolveErr
			} else if current != queue {
				err = errors.New("printer routing changed after the invoice group was planned; restore the selected printer before retrying")
			}
		}
		if err != nil {
			return d.recordManualFailure(ctx, id, l.ref.LineID, queue, err)
		}
		var invoiceHeld bool
		if err = d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM separator_invoices i JOIN orders o ON o.id=i.order_id WHERE i.queue_name=? AND i.order_id<>? AND i.state IN ('failed','submitting') AND o.status<>'cancelled')`, queue, id).Scan(&invoiceHeld); err != nil {
			return err
		}
		if invoiceHeld {
			return fmt.Errorf("printer %s is waiting for a previous invoice to be reviewed and retried", queue)
		}
		content, err := d.docs.FetchAt(ctx, l.ref.StoragePath)
		if err != nil {
			return d.recordManualFailure(ctx, id, l.ref.LineID, queue, err)
		}
		if l.ref.MIMEType == "application/pdf" {
			content, err = documents.ReflowImages(content, l.ref.PaperSize, l.ref.Orientation)
			if err != nil {
				return d.recordManualFailure(ctx, id, l.ref.LineID, queue, err)
			}
		}
		if err := licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
			return err
		}
		claim, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO print_submissions(line_id,state,queue_name,updated_at) VALUES(?,'submitting',?,?)`, l.ref.LineID, queue, time.Now().Unix())
		if err != nil {
			return err
		}
		n, _ := claim.RowsAffected()
		if n != 1 {
			return fmt.Errorf("line %s is already claimed; review its print state", l.ref.LineID)
		}
		jobID, submitErr := d.backend.Submit(ctx, queue, content, l.ref)
		if submitErr != nil {
			_, saveErr := d.db.ExecContext(ctx, `UPDATE print_submissions SET state='failed',error=?,updated_at=? WHERE line_id=?`, submitErr.Error(), time.Now().Unix(), l.ref.LineID)
			return errors.Join(fmt.Errorf("submit order %s: %w", id, submitErr), saveErr)
		}
		// Keep the claim if persistence fails: automatic replay could duplicate paper.
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE print_submissions SET state='submitted',job_id=?,error='',updated_at=? WHERE line_id=?`, jobID, time.Now().Unix(), l.ref.LineID)
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	if err := d.submitSeparators(ctx, id); err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE orders SET print_requested=0 WHERE id=?", id); err != nil {
		return err
	}
	// An unpaid order keeps its payment state; the line journal records printing.
	if status == "paid" {
		_, err = tx.ExecContext(ctx, `UPDATE orders SET status='dispatched',print_requested=0,updated_at=? WHERE id=? AND status='paid'`, time.Now().Unix(), id)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE documents SET dispatched_at=? WHERE order_id=? AND dispatched_at=0`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Dispatcher) orderQueue(ctx context.Context, printer, service string) (string, error) {
	if service != "" {
		// Explicit choices must belong to this service. Never silently route elsewhere.
		var selected string
		err := d.db.QueryRowContext(ctx, `SELECT p.id FROM service_printers sp JOIN services s ON s.id=sp.service_id JOIN printers p ON p.id=sp.printer_id
WHERE s.id=? AND s.enabled=1 AND p.enabled=1 AND p.removed_at IS NULL AND (?='' OR p.id=?)
ORDER BY p.is_default DESC,p.id LIMIT 1`, service, printer, printer).Scan(&selected)
		if err != nil {
			return "", fmt.Errorf("no enabled printer assigned to service: %w", err)
		}
		printer = selected
	}
	if printer != "" {
		if d.resolver == nil {
			return "", errors.New("printer resolver unavailable")
		}
		q, err := d.resolver.QueueFor(ctx, printer)
		if err != nil {
			return "", err
		}
		if q == "" {
			return "", errors.New("selected printer is unavailable")
		}
		return q, nil
	}
	if d.resolver != nil {
		q, err := d.resolver.DefaultQueue(ctx)
		if err != nil {
			return "", err
		}
		if q != "" {
			return q, nil
		}
	}
	if d.config.DefaultQueue != "" {
		return d.config.DefaultQueue, nil
	}
	return "", errors.New("no default printer configured")
}

// RequestPrint explicitly releases a submitted order without changing payment state.
// retry is explicit because a failed/interrupting OS call may have printed paper.
func (d *Dispatcher) RequestPrint(ctx context.Context, id string, retry bool) error {
	if err := d.checkPickup(ctx, id); err != nil {
		return err
	}
	if err := licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
		return err
	}
	d.tickMu.Lock()
	defer d.tickMu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE orders SET print_requested=1 WHERE id=? AND status IN ('paid','pending_payment') AND submitted_at>0 AND EXISTS(SELECT 1 FROM order_lines WHERE order_id=orders.id)", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("only submitted pending or paid orders can be released")
	}
	if !retry {
		var held int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM print_submissions WHERE state IN ('failed','submitting') AND line_id IN (SELECT id FROM order_lines WHERE order_id=?)) + (SELECT COUNT(*) FROM separator_invoices WHERE order_id=? AND state IN ('failed','submitting'))`, id, id).Scan(&held); err != nil {
			return err
		}
		if held > 0 {
			return errors.New("printing failed or was interrupted; check the printer queue before using retry")
		}
	}
	if retry {
		if _, err = tx.ExecContext(ctx, `UPDATE separator_invoices SET state='waiting',error='',progress='',progress_detail='' WHERE order_id=? AND state IN ('failed','submitting')`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM print_submissions WHERE state IN ('failed','submitting') AND line_id IN(SELECT id FROM order_lines WHERE order_id=?)`, id)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.seen, id)
	d.mu.Unlock()
	return nil
}

// setLastError records the most recent error so an operator can read
// it from the dashboard's "Print dispatcher" panel. A nil error clears
// the stored value (so a recovered dispatcher reads as healthy).
func (d *Dispatcher) setLastError(err error) {
	d.mu.Lock()
	d.lastError = err
	d.mu.Unlock()
}

// LastError returns the most recent dispatcher error.
func (d *Dispatcher) LastError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastError
}

// MarkDispatched is a small helper used by tests to seed the
// "seen" map so the dispatcher does not re-submit an order that has
// already been processed out-of-band.
func (d *Dispatcher) MarkDispatched(orderID string) {
	d.mu.Lock()
	d.seen[orderID] = true
	d.mu.Unlock()
}

// ErrNoBackend is returned by Run when the dispatcher was constructed
// with a nil PrinterBackend. It signals that the platform does not
// support the configured backend (e.g. winspool.drv on non-Windows).
// main.go checks this error to avoid logging it as a fatal condition.
var ErrNoBackend = errors.New("dispatcher: no printer backend configured for this platform")

// Automatic routing can recover when a printer appears. Explicit releases surface
// pre-submission failures in the journal so the owner gets actionable feedback.
func (d *Dispatcher) recordManualFailure(ctx context.Context, id, line, queue string, cause error) error {
	_, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO print_submissions(line_id,state,queue_name,error,updated_at)
 SELECT ?,'failed',?,?,? FROM orders WHERE id=? AND print_requested=1`, line, queue, cause.Error(), time.Now().Unix(), id)
	return errors.Join(fmt.Errorf("order %s: %w", id, cause), err)
}
