package dispatch

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"testing"
)

type progressBackend struct {
	mockBackend
	state string
	err   error
}

func (b *progressBackend) JobProgress(context.Context, string, string, string) (string, string, error) {
	return b.state, "queue status", b.err
}

func TestProgressTracksRecoveryAndDoneWithoutChangingPaymentOrResubmitting(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "progress")
	if _, err = db.Exec("UPDATE orders SET status='pending_payment',print_requested=1 WHERE id='progress'"); err != nil {
		t.Fatal(err)
	}
	backend := &progressBackend{}
	d := NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "Test"})
	d.tick(ctx)
	svc := orders.New(db, nil)
	for _, tc := range []struct{ state, want string }{{"pending", "pending"}, {"processing", "processing"}, {"printing", "printing"}, {"blocked", "failed"}, {"printing", "printing"}, {"completed", "done"}} {
		backend.state = tc.state
		d.refreshJobs(ctx)
		got, err := svc.PrintProgress(ctx, "progress")
		if err != nil || got != tc.want {
			t.Fatalf("%s: %s %v", tc.state, got, err)
		}
	}
	backend.state = "blocked"
	d.refreshJobs(ctx)
	d.tick(ctx)
	got, err := svc.PrintProgress(ctx, "progress")
	if err != nil || got != "done" {
		t.Fatalf("completion regressed: %s %v", got, err)
	}
	var status string
	db.QueryRow("SELECT status FROM orders WHERE id='progress'").Scan(&status)
	if status != "pending_payment" || len(backend.Jobs()) != 1 {
		t.Fatalf("payment changed or duplicate print: %s %v", status, backend.Jobs())
	}
}

func TestMissingJobNeedsConfirmationAndPollingErrorsRecover(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "review")
	backend := &progressBackend{err: errors.New("offline")}
	d := NewWithDefaultQueue(db, docs, backend, DispatcherConfig{DefaultQueue: "Test"})
	svc := orders.New(db, nil)
	if err := svc.ConfirmPrintDone(ctx, "review", "owner"); err == nil {
		t.Fatal("unsubmitted order marked done")
	}
	d.tick(ctx)
	d.refreshJobs(ctx)
	got, _ := svc.PrintProgress(ctx, "review")
	if got != "failed" {
		t.Fatal(got)
	}
	backend.err = nil
	backend.state = "review"
	d.refreshJobs(ctx)
	got, _ = svc.PrintProgress(ctx, "review")
	if got != "processing" {
		t.Fatal(got)
	}
	if err := svc.ConfirmPrintDone(ctx, "review", "owner"); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.PrintProgress(ctx, "review")
	if got != "done" {
		t.Fatal(got)
	}
	var events int
	db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE event_type='order.print_completed'").Scan(&events)
	if events != 1 {
		t.Fatal(events)
	}
}
