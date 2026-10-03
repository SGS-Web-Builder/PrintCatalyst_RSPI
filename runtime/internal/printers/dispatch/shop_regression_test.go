package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

func TestShopDispatchPoliciesAndRecovery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	run := func(mode, status, id string, fail bool) (*Dispatcher, *mockBackend) {
		t.Helper()
		seedOrder(t, ctx, db, docs, files, id)
		if _, err := db.Exec("UPDATE business_settings SET auto_print_mode=?", mode); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE orders SET status=? WHERE id=?", status, id); err != nil {
			t.Fatal(err)
		}
		backend := &mockBackend{}
		if fail {
			backend.fail = errors.New("printer unavailable")
		}
		d := NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "Shop printer"})
		d.tick(ctx)
		return d, backend
	}
	d, b := run("off", "paid", "manual", false)
	if len(b.Jobs()) != 0 {
		t.Fatal("off policy printed automatically")
	}
	if err := d.RequestPrint(ctx, "manual", false); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(b.Jobs()) != 1 {
		t.Fatalf("manual print: %v", d.LastError())
	}
	d, b = run("on_payment_captured", "paid", "failed", true)
	var status, state string
	db.QueryRow("SELECT status FROM orders WHERE id='failed'").Scan(&status)
	db.QueryRow("SELECT state FROM print_submissions WHERE line_id='failed-line'").Scan(&state)
	if status != "paid" || state != "failed" || d.LastError() == nil {
		t.Fatalf("failed submission status=%s journal=%s err=%v", status, state, d.LastError())
	}
	b.fail = nil
	d.tick(ctx)
	if len(b.Jobs()) != 0 {
		t.Fatal("failed submission replayed without review")
	}
	if err := d.RequestPrint(ctx, "failed", true); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(b.Jobs()) != 1 {
		t.Fatalf("retry did not print: %v", d.LastError())
	}
	// A fresh dispatcher must honor the durable journal, not just a process-local map.
	db.Exec("UPDATE orders SET status='paid' WHERE id='failed'")
	fresh := NewWithDefaultQueue(db, docs, b, DispatcherConfig{DefaultQueue: "Shop printer"})
	fresh.tick(ctx)
	if len(b.Jobs()) != 1 {
		t.Fatal("restart duplicated a successful submission")
	}
	d, b = run("all_documents", "pending_payment", "kiosk", false)
	if len(b.Jobs()) != 1 {
		t.Fatalf("kiosk order did not print: %v", d.LastError())
	}
	db.QueryRow("SELECT status FROM orders WHERE id='kiosk'").Scan(&status)
	if status != "pending_payment" {
		t.Fatal("printing incorrectly recorded payment")
	}
	db.Exec("UPDATE orders SET status='paid' WHERE id='kiosk'")
	d.tick(ctx)
	if len(b.Jobs()) != 1 {
		t.Fatal("payment printed kiosk order twice")
	}
}

func TestDispatchDoesNotTruncateLargeOrders(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	files, _ := localfiles.New(t.TempDir())
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "large")
	for i := 1; i < 51; i++ {
		_, err := db.Exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,unit_price_minor,line_total_minor) SELECT ?,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,unit_price_minor,line_total_minor FROM order_lines WHERE id='large-line'`, fmt.Sprintf("large-%02d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	backend := &mockBackend{}
	d := NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "Shop"})
	d.tick(ctx)
	if len(backend.Jobs()) != 51 {
		t.Fatalf("printed %d of 51 lines: %v", len(backend.Jobs()), d.LastError())
	}
}

func TestMissingPrinterCanRecoverWithoutRestart(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	files, _ := localfiles.New(t.TempDir())
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "waiting")
	b := &mockBackend{}
	d := NewWithDefaultQueue(db, docs, b, DispatcherConfig{})
	d.tick(ctx)
	if d.LastError() == nil {
		t.Fatal("missing printer not reported")
	}
	d.config.DefaultQueue = "Shop"
	d.tick(ctx)
	if len(b.Jobs()) != 1 {
		t.Fatalf("order stuck after configuring printer: %v", d.LastError())
	}
}

func TestPrintLayoutValidation(t *testing.T) {
	r := DocumentRef{Copies: 2, PageStart: 2, PageEnd: 3, PagesPerSheet: 4, ColourMode: "monochrome", Sides: "two-sided-long-edge", Orientation: "portrait"}
	if err := validatePrintOptions(r, 3); err != nil {
		t.Fatal(err)
	}
	r.PageEnd = 4
	if validatePrintOptions(r, 3) == nil {
		t.Fatal("out-of-range print accepted")
	}
	x, y, w, h := fitPage(100, 200, 0, 0, 200, 200)
	if x != 50 || y != 0 || w != 100 || h != 200 {
		t.Fatalf("wrong aspect fit %d %d %d %d", x, y, w, h)
	}
}
