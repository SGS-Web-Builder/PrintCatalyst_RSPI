package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"strings"
	"testing"
)

type separatorBackend struct {
	refs        []DocumentRef
	bodies      []string
	failInvoice bool
}

func (b *separatorBackend) Name() string { return "test" }
func (b *separatorBackend) Queues(context.Context) ([]string, error) {
	return []string{"TestPrinter"}, nil
}
func (b *separatorBackend) Submit(_ context.Context, _ string, body []byte, ref DocumentRef) (string, error) {
	if b.failInvoice && ref.Invoice {
		return "", errors.New("invoice tray empty")
	}
	if ref.InvoiceSheets != nil {
		*ref.InvoiceSheets = 2
	}
	b.refs = append(b.refs, ref)
	b.bodies = append(b.bodies, string(body))
	return fmt.Sprintf("job-%d", len(b.refs)), nil
}
func (b *separatorBackend) JobProgress(context.Context, string, string, string) (string, string, error) {
	return "completed", "Printed", nil
}
func separatorFixture(t *testing.T, n int, enabled bool, threshold int) (*Dispatcher, *separatorBackend, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db := newTestDB(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	for i := 0; i < n; i++ {
		seedOrder(t, ctx, db, docs, files, fmt.Sprintf("order-%02d", i))
	}
	for _, q := range []string{`INSERT INTO printers(id,backend,queue_name,created_at,last_seen_at) VALUES('p','mock','TestPrinter',1,1)`, `INSERT INTO business_profile(singleton,profile_json,updated_at) VALUES(1,'{"name":"श्री Print Shop"}',1) ON CONFLICT(singleton) DO UPDATE SET profile_json=excluded.profile_json`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO printer_invoice_settings(printer_id,enabled,threshold,paper,tray) VALUES('p',?,?,'A6','Tray 2')`, enabled, threshold); err != nil {
		t.Fatal(err)
	}
	backend := &separatorBackend{}
	return NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "TestPrinter"}), backend, db
}
func TestSeparatorThresholdAndOrder(t *testing.T) {
	for _, tc := range []struct {
		n        int
		enabled  bool
		invoices int
	}{{1, true, 0}, {3, true, 0}, {4, true, 0}, {5, true, 5}, {5, false, 0}} {
		t.Run(fmt.Sprintf("%d-%v", tc.n, tc.enabled), func(t *testing.T) {
			d, b, db := separatorFixture(t, tc.n, tc.enabled, 4)
			d.tick(context.Background())
			if err := d.LastError(); err != nil {
				t.Fatal(err)
			}
			if len(b.refs) != tc.n+tc.invoices {
				t.Fatalf("got %d jobs, wanted %d", len(b.refs), tc.n+tc.invoices)
			}
			if tc.invoices > 0 {
				for i := 0; i < len(b.refs); i += 2 {
					doc, invoice := b.refs[i], b.refs[i+1]
					if doc.Invoice || !invoice.Invoice || doc.OrderID != invoice.OrderID || doc.PaperSize != "A4" || invoice.PaperSize != "A6" || invoice.Tray != "Tray 2" {
						t.Fatalf("wrong sequence/settings: %+v %+v", doc, invoice)
					}
					for _, want := range []string{"श्री Print Shop", "doc.pdf", "TOTAL ORDER VALUE: INR 50.00", "Copies: 1", "Pages: 1-1", "Black & white", "Single-sided", "Orientation:", "Customer"} {
						if !strings.Contains(b.bodies[i+1], want) {
							t.Fatalf("invoice missing %q", want)
						}
					}
				}
			}
			d = NewWithDefaultQueue(db, d.docs, b, d.config)
			d.tick(context.Background())
			if len(b.refs) != tc.n+tc.invoices {
				t.Fatal("restart duplicated jobs")
			}
		})
	}
}
func TestSeparatorDecisionSurvivesDrainingQueue(t *testing.T) {
	d, b, db := separatorFixture(t, 5, true, 4)
	ctx := context.Background()
	var group []dispatchBatch
	for i := 0; i < 5; i++ {
		group = append(group, dispatchBatch{id: fmt.Sprintf("order-%02d", i), status: "paid"})
	}
	if err := d.planSeparators(ctx, group); err != nil {
		t.Fatal(err)
	}
	if err := d.dispatchOrder(ctx, group[0].id, "", "", "paid"); err != nil {
		t.Fatal(err)
	}
	// Four remain, and even switching settings off must not rewrite this group.
	if _, err := db.Exec(`UPDATE printer_invoice_settings SET enabled=0`); err != nil {
		t.Fatal(err)
	}
	restarted := NewWithDefaultQueue(db, d.docs, b, d.config)
	restarted.tick(ctx)
	if err := restarted.LastError(); err != nil {
		t.Fatal(err)
	}
	if len(b.refs) != 10 {
		t.Fatalf("got %d jobs, want all 5 document/invoice pairs", len(b.refs))
	}
}
func TestSeparatorFailureRetryAndProgress(t *testing.T) {
	d, b, db := separatorFixture(t, 1, true, 0)
	ctx := context.Background()
	b.failInvoice = true
	d.tick(ctx)
	if d.LastError() == nil || len(b.refs) != 1 {
		t.Fatal("expected invoice failure after the document")
	}
	// A completed document alone must not make a failed invoice order Done.
	if _, err := db.Exec(`UPDATE print_submissions SET progress='completed'`); err != nil {
		t.Fatal(err)
	}
	svc := orders.New(db, nil)
	state, err := svc.PrintProgress(ctx, "order-00")
	if err != nil || state != "failed" {
		t.Fatalf("progress %s %v", state, err)
	}
	d.tick(ctx)
	if len(b.refs) != 1 {
		t.Fatal("automatic retry must not print twice")
	}
	b.failInvoice = false
	if err = d.RequestPrint(ctx, "order-00", true); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if err = d.LastError(); err != nil {
		t.Fatal(err)
	}
	if len(b.refs) != 2 || !b.refs[1].Invoice {
		t.Fatal("retry must submit only invoice")
	}
	d.refreshJobs(ctx)
	state, err = svc.PrintProgress(ctx, "order-00")
	if err != nil || state != "done" {
		t.Fatalf("progress %s %v", state, err)
	}
	var sheets int
	if err = db.QueryRow(`SELECT sheets FROM separator_invoices`).Scan(&sheets); err != nil || sheets != 2 {
		t.Fatalf("sheets %d %v", sheets, err)
	}
}

type byPrinterResolver struct{}

func (byPrinterResolver) QueueFor(_ context.Context, id string) (string, error) { return id, nil }
func (byPrinterResolver) DefaultQueue(context.Context) (string, error)          { return "TestPrinter", nil }
func TestSeparatorQueuesAreIndependent(t *testing.T) {
	d, b, db := separatorFixture(t, 8, true, 4)
	ctx := context.Background()
	d.resolver = byPrinterResolver{}
	for _, q := range []string{`INSERT INTO printers(id,backend,queue_name,created_at,last_seen_at) VALUES('p2','mock','SecondPrinter',1,1)`, `INSERT INTO printer_invoice_settings(printer_id,enabled,threshold,paper,tray) VALUES('p2',1,4,'A5','Tray 1')`, `UPDATE orders SET primary_printer_id='TestPrinter' WHERE id<'order-05'`, `UPDATE orders SET primary_printer_id='SecondPrinter' WHERE id>='order-05'`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d.tick(ctx)
	if err := d.LastError(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ref := range b.refs {
		if ref.Invoice {
			count++
			if ref.OrderID >= "order-05" {
				t.Fatal("3-order printer must not print invoices")
			}
		}
	}
	if count != 5 {
		t.Fatalf("invoice count %d", count)
	}
}
func TestInvoiceWrapPreservesUnicodeAndLongNames(t *testing.T) {
	input := "श्री Print\n" + strings.Repeat("long-name", 50)
	lines, err := wrapInvoice(input, 20, func(s string) (int, error) { return len([]rune(s)), nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if len([]rune(line)) > 20 {
			t.Fatal("overflow")
		}
	}
	if strings.Join(lines, "") != strings.ReplaceAll(input, "\n", "") {
		t.Fatal("text lost while wrapping")
	}
}

func TestFailedSeparatorHoldsFollowingDocumentsUntilExplicitRetry(t *testing.T) {
	d, b, db := separatorFixture(t, 5, true, 4)
	ctx := context.Background()
	b.failInvoice = true
	d.tick(ctx)
	if len(b.refs) != 1 {
		t.Fatalf("later documents overtook failed invoice: %d jobs", len(b.refs))
	}
	if err := d.RequestPrint(ctx, "order-00", false); err == nil {
		t.Fatal("failed invoice must require explicit retry")
	}
	b.failInvoice = false
	d = NewWithDefaultQueue(db, d.docs, b, d.config)
	if err := d.RequestPrint(ctx, "order-00", true); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if err := d.LastError(); err != nil {
		t.Fatal(err)
	}
	if len(b.refs) != 10 {
		t.Fatalf("got %d jobs, expected 5 pairs", len(b.refs))
	}
}

func TestUnpaidWaitingAndSubmittedHistoryDoNotInflateGroup(t *testing.T) {
	d, b, db := separatorFixture(t, 5, true, 4)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE orders SET status='pending_payment' WHERE id='order-04'`); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if err := d.LastError(); err != nil {
		t.Fatal(err)
	}
	if len(b.refs) != 4 {
		t.Fatalf("got %d jobs", len(b.refs))
	}
	if _, err := db.Exec(`UPDATE orders SET status='paid' WHERE id='order-04'`); err != nil {
		t.Fatal(err)
	}
	d = NewWithDefaultQueue(db, d.docs, b, d.config)
	d.tick(ctx)
	if len(b.refs) != 5 {
		t.Fatal("history inflated the new group")
	}
	for _, ref := range b.refs {
		if ref.Invoice {
			t.Fatal("a group of four, then one, must have no invoice")
		}
	}
}

func TestEveryOrderInvoiceIncludesConfiguredLogo(t *testing.T) {
	d, b, db := separatorFixture(t, 1, true, 0)
	if _, err := db.Exec(`UPDATE portal_branding SET settings=json_set(settings,'$.logo','data:image/png;base64,aGVsbG8=') WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	d.tick(context.Background())
	if err := d.LastError(); err != nil {
		t.Fatal(err)
	}
	if len(b.refs) != 2 || !b.refs[1].Invoice || string(b.refs[1].InvoiceLogo) != "hello" {
		t.Fatal("single waiting order did not carry its invoice branding")
	}
	if !strings.Contains(b.bodies[1], "श्री Print Shop") || !strings.Contains(b.bodies[1], "Copies: 1") {
		t.Fatal("missing merchant or print settings")
	}
}

func TestInvoiceCompletionHeadingRequiresCompletedDocuments(t *testing.T) {
	d, _, db := separatorFixture(t, 1, true, 0)
	ctx := context.Background()
	d.tick(ctx)
	text, err := d.separatorText(ctx, "order-00", "Test Printer")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "PRINT DONE") {
		t.Fatal("submission reported as completed")
	}
	if _, err = db.Exec(`UPDATE print_submissions SET progress='completed'`); err != nil {
		t.Fatal(err)
	}
	text, err = d.separatorText(ctx, "order-00", "Test Printer")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "PRINT DONE") {
		t.Fatal("completed output not labelled done")
	}
}

func TestInvoiceLicenseExpiryLeavesRemainingInvoicesWaiting(t *testing.T) {
	d, b, db := separatorFixture(t, 1, true, 0)
	ctx := context.Background()
	for _, queue := range []string{"A", "B"} {
		if _, err := db.Exec(`INSERT INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,updated_at) VALUES('order-00',?,'A4','',1,'waiting',1)`, queue); err != nil {
			t.Fatal(err)
		}
	}
	expired := true
	d.config.LicenseCheck = func(context.Context) error {
		if expired && len(b.refs) > 0 {
			return errors.New("licence expired")
		}
		return nil
	}
	if err := d.submitSeparators(ctx, "order-00"); err == nil {
		t.Fatal("expiry ignored between invoices")
	}
	if len(b.refs) != 1 {
		t.Fatalf("submitted %d invoices", len(b.refs))
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM separator_invoices WHERE queue_name='B'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "waiting" {
		t.Fatalf("paused invoice state: %s", state)
	}
	expired = false
	if err := d.submitSeparators(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if len(b.refs) != 2 {
		t.Fatal("did not resume exactly once")
	}
}
