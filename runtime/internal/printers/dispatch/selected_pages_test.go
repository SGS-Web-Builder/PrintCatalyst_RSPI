package dispatch

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"reflect"
	"testing"
)

type selectionBackend struct {
	mockBackend
	received []int
}

func (b *selectionBackend) Submit(ctx context.Context, queue string, body []byte, ref DocumentRef) (string, error) {
	b.received = printPages(ref, ref.PageCount)
	return b.mockBackend.Submit(ctx, queue, body, ref)
}
func TestDispatchPreservesSelectedPages(t *testing.T) {
	db := newTestDB(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, context.Background(), db, docs, files, "selection")
	for _, query := range []string{
		"UPDATE documents SET page_count=5",
		"UPDATE order_lines SET page_range_start=1,page_range_end=5,selected_pages_json='[1,3,5]'",
		"UPDATE business_settings SET auto_print_mode='on_payment_captured'",
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	backend := &selectionBackend{}
	d := NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "Test"})
	d.tick(context.Background())
	if !reflect.DeepEqual(backend.received, []int{1, 3, 5}) {
		t.Fatalf("printed pages %v, error %v", backend.received, d.LastError())
	}
	legacy := DocumentRef{PageStart: 2, PageEnd: 4}
	if !reflect.DeepEqual(printPages(legacy, 5), []int{2, 3, 4}) {
		t.Fatal("legacy range changed")
	}
}

func TestOneClickUnpaidReleaseDoesNotRecordPaymentOrReprint(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "manual-unpaid")
	db.Exec("UPDATE orders SET status='pending_payment'")
	db.Exec("UPDATE business_settings SET auto_print_mode='off'")
	backend := &selectionBackend{}
	dispatcher := NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "Test"})
	dispatcher.tick(ctx)
	if len(backend.Jobs()) != 0 {
		t.Fatal("unpaid job released without owner action")
	}
	if err = dispatcher.RequestPrint(ctx, "manual-unpaid", false); err != nil {
		t.Fatal(err)
	}
	dispatcher.tick(ctx)
	var status string
	var requested int
	if err = db.QueryRow("SELECT status,print_requested FROM orders WHERE id='manual-unpaid'").Scan(&status, &requested); err != nil {
		t.Fatal(err)
	}
	if status != "pending_payment" || requested != 0 || len(backend.Jobs()) != 1 {
		t.Fatalf("status=%s requested=%d jobs=%v error=%v", status, requested, backend.Jobs(), dispatcher.LastError())
	}
	if err = dispatcher.RequestPrint(ctx, "manual-unpaid", false); err != nil {
		t.Fatal(err)
	}
	dispatcher.tick(ctx)
	if len(backend.Jobs()) != 1 {
		t.Fatal("duplicate click printed twice")
	}
	db.Exec("UPDATE orders SET status='cancelled'")
	if err = dispatcher.RequestPrint(ctx, "manual-unpaid", false); err == nil {
		t.Fatal("released cancelled order")
	}
}
func TestOneClickMissingPrinterIsVisibleAndRequiresRetry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "missing-manual")
	db.Exec("UPDATE business_settings SET auto_print_mode='off'")
	d := NewWithDefaultQueue(db, docs, &mockBackend{}, DispatcherConfig{})
	if err = d.RequestPrint(ctx, "missing-manual", false); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	var state, detail string
	if err = db.QueryRow("SELECT state,error FROM print_submissions WHERE line_id='missing-manual-line'").Scan(&state, &detail); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || detail == "" {
		t.Fatalf("missing failure: %s %s", state, detail)
	}
	if err = d.RequestPrint(ctx, "missing-manual", false); err == nil {
		t.Fatal("failure bypassed review")
	}
	d.config.DefaultQueue = "Restored printer"
	if err = d.RequestPrint(ctx, "missing-manual", true); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if d.LastError() != nil {
		t.Fatal(d.LastError())
	}
}
