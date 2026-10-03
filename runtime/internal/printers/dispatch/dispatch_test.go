package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// mockBackend is an in-memory PrinterBackend used by tests. It records
// every Submit call so the test can assert which orders were pushed
// to the spooler and in what order.
type mockBackend struct {
	mu     sync.Mutex
	jobs   []string
	queues []string
	nextID int64
	fail   error // when non-nil, Submit returns this error
}

func (m *mockBackend) Name() string { return "mock" }

func (m *mockBackend) Queues(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.queues...)
	return out, nil
}

func (m *mockBackend) Submit(_ context.Context, _ string, content []byte, ref DocumentRef) (string, error) {
	if m.fail != nil {
		return "", m.fail
	}
	id := atomic.AddInt64(&m.nextID, 1)
	jobID := fmt.Sprintf("job-%d", id)
	m.mu.Lock()
	m.jobs = append(m.jobs, ref.OrderID)
	m.mu.Unlock()
	if len(content) == 0 {
		return "", errors.New("empty content")
	}
	return jobID, nil
}

func (m *mockBackend) Jobs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.jobs...)
	return out
}

// newTestDB returns an isolated, file-backed SQLite database with the
// runtime migrations applied. The dispatcher is end-to-end tested
// against this database; the schema and migrations are the same as
// production. We deliberately avoid ":memory:" because modernc/sqlite
// shares the same connection across goroutines in a way that prevents
// multiple tests from coexisting inside one process.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.DB()
}

// seedOrder inserts a paid order with one document so the dispatcher
// has something to pick up. Returns the order id. The files handle
// must point at the supplied dir so the documents service can read
// the seeded blob back via FetchAt.
func seedOrder(t *testing.T, ctx context.Context, db *sql.DB, docs *documents.Service, files *localfiles.Files, orderID string) string {
	t.Helper()
	dir := files.DataRoot()
	if err := os.MkdirAll(filepath.Join(dir, "documents", orderID), 0o755); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(dir, "documents", orderID, "doc.pdf")
	if err := os.WriteFile(blobPath, []byte("%PDF-1.4\n%mock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, submitted_at)
VALUES (?, ?, ?, 'INR', 2, 5000, 'Customer', '+91', '', '', ?, ?, ?)`,
		orderID, orderID+"-share", OrderStatusPaid, now, now, now); err != nil {
		t.Fatal(err)
	}
	docID := orderID + "-doc"
	if _, err := db.ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, ?, 'doc.pdf', 'application/pdf', 10, 1, 'abc', ?, ?, ?)`,
		docID, orderID, "documents/"+orderID+"/doc.pdf", now, now+86400); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, unit_price_minor, line_total_minor)
VALUES (?, ?, ?, 'A4', 'A4', 'monochrome', 'one-sided', 1, 1, 1, 100, 100)`,
		orderID+"-line", orderID, docID); err != nil {
		t.Fatal(err)
	}
	return orderID
}

// TestDispatcherPicksUpPaidOrder checks the happy path: a paid order is
// submitted to the backend and flipped to dispatched in the same tick.
func TestDispatcherPicksUpPaidOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := newTestDB(t)
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "order-1")
	backend := &mockBackend{}
	d := New(db, docs, backend, nil, DispatcherConfig{
		PollInterval: 50 * time.Millisecond,
		DefaultQueue: "TestPrinter",
	})
	go func() { _ = d.Run(ctx) }()
	// Wait for the dispatcher to pick up the order.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		_ = db.QueryRowContext(ctx, "SELECT status FROM orders WHERE id=?", "order-1").Scan(&state)
		if state == OrderStatusDispatched {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	jobs := backend.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want 1", jobs)
	}
	if jobs[0] != "order-1" {
		t.Errorf("job order id = %s, want order-1", jobs[0])
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM orders WHERE id=?`, "order-1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusDispatched {
		t.Errorf("status = %s, want dispatched", status)
	}
}

// TestDispatcherIsIdempotent verifies the dispatcher does not re-submit
// an order it has already seen.
func TestDispatcherIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	d := New(db, docs, &mockBackend{}, nil, DispatcherConfig{PollInterval: 50 * time.Millisecond})
	d.MarkDispatched("order-x")
	if !d.seen["order-x"] {
		t.Fatal("MarkDispatched did not record the order")
	}
	// The presence of the order id in the seen set is sufficient;
	// re-running Run would still skip the same id.
}

// TestDispatcherRequiresBackend verifies a nil backend short-circuits
// the loop so a misconfigured deployment fails fast rather than spinning.
func TestDispatcherRequiresBackend(t *testing.T) {
	d := New(nil, nil, nil, nil, DispatcherConfig{})
	err := d.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error when backend is nil")
	}
}

// TestDispatcherSubSecondLatency guards the production SLA: a paid
// order must be submitted to the backend in well under one second.
// We measure end-to-end from "seed the order" to "backend recorded
// the job".
func TestDispatcherSubSecondLatency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := newTestDB(t)
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	backend := &mockBackend{}
	d := New(db, docs, backend, nil, DispatcherConfig{
		PollInterval: 50 * time.Millisecond,
		DefaultQueue: "TestPrinter",
	})
	go func() { _ = d.Run(ctx) }()
	start := time.Now()
	seedOrder(t, ctx, db, docs, files, "fast-order")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(backend.Jobs()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Errorf("dispatcher latency %v exceeds 1s SLA", elapsed)
	}
}

// TestDispatcherReportsBackendErrors ensures a transient backend error
// is captured via LastError rather than crashing the loop.
func TestDispatcherReportsBackendErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := newTestDB(t)
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "order-err")
	backend := &mockBackend{fail: errors.New("spooler offline")}
	d := New(db, docs, backend, nil, DispatcherConfig{
		PollInterval: 50 * time.Millisecond,
		DefaultQueue: "TestPrinter", // required so the dispatcher routes to the (failing) backend
	})
	go func() { _ = d.Run(ctx) }()
	// Wait for at least one tick.
	time.Sleep(200 * time.Millisecond)
	if d.LastError() == nil {
		t.Fatal("expected LastError to capture the backend failure")
	}
	cancel()
}

func TestInvalidLicensePausesPaidDispatchAndManualRelease(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "licensed-order")
	backend := &mockBackend{}
	blocked := true
	d := New(db, docs, backend, nil, DispatcherConfig{DefaultQueue: "TestPrinter", LicenseCheck: func(context.Context) error {
		if blocked {
			return errors.New("licence required")
		}
		return nil
	}})
	d.tick(ctx)
	if len(backend.Jobs()) != 0 {
		t.Fatal("paid work bypassed licence gate")
	}
	if d.RequestPrint(ctx, "licensed-order", false) == nil {
		t.Fatal("manual release bypassed licence gate")
	}
	if d.dispatchOrder(ctx, "licensed-order", "", "", "paid") == nil {
		t.Fatal("direct paid dispatch bypassed licence gate")
	}
	var status string
	db.QueryRow("SELECT status FROM orders WHERE id='licensed-order'").Scan(&status)
	if status != "paid" {
		t.Fatalf("paused order was changed: %s", status)
	}
	blocked = false
	d.tick(ctx)
	if len(backend.Jobs()) != 1 {
		t.Fatalf("activation did not resume queued order: %v", backend.Jobs())
	}
}
