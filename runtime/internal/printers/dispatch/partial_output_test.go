package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
)

type interruptedBundleBackend struct {
	separatorBackend
	calls int
	fail  bool
}

func (b *interruptedBundleBackend) Submit(ctx context.Context, q string, data []byte, r DocumentRef) (string, error) {
	b.calls++
	if b.fail && b.calls == 2 {
		return "", errors.New("connection lost after sending second document")
	}
	return b.separatorBackend.Submit(ctx, q, data, r)
}

func TestPartialBundleRestartDoesNotReplayAcceptedDocument(t *testing.T) {
	ctx := context.Background()
	d, _, p, codes, _, _ := prepareFixture(t, 1, false, 0)
	_, err := d.db.Exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_end,unit_price_minor,line_total_minor)
 SELECT 'second-line',order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_end,unit_price_minor,line_total_minor FROM order_lines LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	b := &interruptedBundleBackend{fail: true}
	d.backend = b
	d.tick(ctx)
	if b.calls != 2 || len(b.refs) != 1 {
		t.Fatalf("unexpected partial submission: %d/%d: %v", b.calls, len(b.refs), d.LastError())
	}
	restarted := NewWithDefaultQueue(d.db, d.docs, b, d.config)
	restarted.tick(ctx)
	if b.calls != 2 {
		t.Fatal("restart replayed uncertain output")
	}
	if err = restarted.cleanupPrepared(ctx); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err = d.db.QueryRow(`SELECT prepared_digest FROM kiosk_preparations`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.config.Prepared.Load("order-00", digest); err != nil {
		t.Fatal("partial bundle deleted", err)
	}
	b.fail = false
	if err = restarted.RequestPrint(ctx, "order-00", true); err != nil {
		t.Fatal(err)
	}
	restarted.tick(ctx)
	if b.calls != 3 || len(b.refs) != 2 || b.refs[0].LineID == b.refs[1].LineID {
		t.Fatal("review retry replayed accepted document")
	}
	if _, err = p.Claim(ctx, codes[0]); err == nil {
		t.Fatal("used pickup code released again")
	}
}

func TestLegacyCUPSCompletionRetainsPreparedBundleUntilConfirmation(t *testing.T) {
	ctx := context.Background()
	d, _, p, codes, _, _ := prepareFixture(t, 1, true, 0)
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	for _, q := range []string{`UPDATE print_submissions SET progress='completed',job_id='cups:1:legacy'`, `UPDATE separator_invoices SET progress='completed',job_id='cups:2:legacy'`} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.cleanupPrepared(ctx); err != nil {
		t.Fatal(err)
	}
	var purged int
	if err := d.db.QueryRow(`SELECT purged_at FROM kiosk_preparations`).Scan(&purged); err != nil || purged != 0 {
		t.Fatal("unconfirmed CUPS bundle deleted", purged, err)
	}
	d.refreshJobs(ctx)
	var review int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM print_submissions WHERE progress='review'`).Scan(&review); err != nil || review != 1 {
		t.Fatal("legacy completion was not made reviewable", review, err)
	}
	if err := orders.New(d.db, nil).ConfirmPrintDone(ctx, "order-00", "test-owner"); err != nil {
		t.Fatal(err)
	}
	d.refreshJobs(ctx)
	if err := d.cleanupPrepared(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.db.QueryRow(`SELECT purged_at FROM kiosk_preparations`).Scan(&purged); err != nil || purged == 0 {
		t.Fatal("confirmed bundle not cleaned", purged, err)
	}
}

func TestPreparationWakesPreviouslyEnteredOTP(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, _ := prepareFixture(t, 1, false, 0)
	if id, err := p.Claim(ctx, codes[0]); id != "order-00" || err == nil {
		t.Fatal(id, err)
	}
	woke := make(chan struct{}, 1)
	d.config.OnPrepared = func() { woke <- struct{}{} }
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-woke:
	default:
		t.Fatal("preparation did not signal release worker")
	}
	if n, err := p.ReleaseWaiting(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	d.tick(ctx)
	d.tick(ctx)
	if len(b.refs) != 1 {
		t.Fatal("waiting OTP did not dispatch exactly once", len(b.refs), d.LastError())
	}
}
