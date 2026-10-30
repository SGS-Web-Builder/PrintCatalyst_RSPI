package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
)

type preparationRenderer struct {
	fail  bool
	calls int
}

func (r *preparationRenderer) RenderPrepared(_ context.Context, body []byte, ref DocumentRef) ([]byte, prepared.Settings, error) {
	r.calls++
	if r.fail {
		return nil, prepared.Settings{}, errors.New("render failed")
	}
	colour := ref.ColourMode
	if colour == "colour" {
		colour = "color"
	}
	settings := prepared.Settings{Pages: 1, Paper: ref.PaperSize, Tray: ref.Tray, Colour: colour, Sides: orientationDuplex(ref.Sides, ref.Orientation), Copies: ref.Copies}
	return append([]byte("%PDF-1.4\nprepared fixture\n"), body...), settings, nil
}
func prepareFixture(t *testing.T, n int, invoices bool, threshold int) (*Dispatcher, *separatorBackend, *pickup.Service, []string, *preparationRenderer, string) {
	t.Helper()
	d, b, db := separatorFixture(t, n, invoices, threshold)
	d.config.RequirePickup = true
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := prepared.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d.config.Prepared = s
	renderer := &preparationRenderer{}
	d.config.Renderer = renderer
	p, err := pickup.New(db, []byte(strings.Repeat("e", 32)), []byte(strings.Repeat("h", 32)))
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("order-%02d", i)
		body, err := d.docs.FetchAt(context.Background(), "documents/"+id+"/doc.pdf")
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(body)
		if _, err = db.Exec(`UPDATE documents SET sha256=? WHERE order_id=?`, hex.EncodeToString(h[:]), id); err != nil {
			t.Fatal(err)
		}
		code, err := p.IssueVerified(context.Background(), id, "verified-"+id)
		if err != nil {
			t.Fatal(err)
		}
		codes = append(codes, code)
	}
	return d, b, p, codes, renderer, dir
}
func TestPreparedPipelineClaimInvoiceAndNoReplay(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, _ := prepareFixture(t, 5, true, 4)
	for i := range codes {
		id := fmt.Sprintf("order-%02d", i)
		if err := d.prepareOrder(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	d.tick(ctx)
	if len(b.refs) != 0 {
		t.Fatal("printed without physical claim")
	}
	for _, code := range codes {
		if _, err := p.Claim(ctx, code); err != nil {
			t.Fatal(err)
		}
	}
	d.tick(ctx)
	if len(b.refs) != 10 {
		t.Fatalf("got %d submitted jobs; err %v", len(b.refs), d.lastError)
	}
	invoices := 0
	for _, r := range b.refs {
		if r.Invoice {
			invoices++
		}
		if r.MIMEType != "application/pdf" {
			t.Fatal("not prepared PDF")
		}
	}
	if invoices != 5 {
		t.Fatal("invoice threshold lost", invoices)
	}
	// Recreate the dispatcher; durable journals suppress duplicate documents/invoices.
	next := New(d.db, d.docs, b, d.resolver, d.config)
	next.tick(ctx)
	if len(b.refs) != 10 {
		t.Fatal("restart reprinted")
	}
}
func TestPreparedFreezeAndFailedRendererRecovery(t *testing.T) {
	ctx := context.Background()
	d, _, p, codes, r, _ := prepareFixture(t, 1, false, 4)
	r.fail = true
	if err := d.prepareWaiting(ctx); err == nil {
		t.Fatal("renderer failure hidden")
	}
	if _, err := p.Claim(ctx, codes[0]); !errors.Is(err, pickup.ErrPreparing) {
		t.Fatal("failed rendering consumed code", err)
	}
	for _, q := range []string{`UPDATE order_lines SET copies=2`, `DELETE FROM order_lines`, `UPDATE orders SET total_minor=1`, `UPDATE documents SET sha256='changed'`} {
		if _, err := d.db.Exec(q); err == nil {
			t.Fatal("frozen data changed", q)
		}
	}
	r.fail = false
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	calls := r.calls
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if r.calls != calls {
		t.Fatal("ready order rerendered")
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
}
func TestPreparedCorruptionBlocksWholeOrderBeforeSubmit(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, dir := prepareFixture(t, 1, true, 0)
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := d.db.QueryRow(`SELECT prepared_digest FROM kiosk_preparations`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, digest, "001.pdf"), []byte("%PDF-bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(b.refs) != 0 {
		t.Fatal("partial order submitted despite broken invoice")
	}
}
func TestPreparedUsesFrozenBytesAndHonoursBelowThreshold(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, _ := prepareFixture(t, 1, true, 4)
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	// Original source availability no longer affects prepared dispatch.
	if err := d.docs.Delete(ctx, "documents/order-00/doc.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(b.refs) != 1 || b.refs[0].Invoice {
		t.Fatalf("unexpected submission %d, %v", len(b.refs), d.lastError)
	}
}
func TestPreparedChangedInvoiceAndRouteBlockBeforeSubmit(t *testing.T) {
	for _, kind := range []string{"invoice", "route"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			d, b, p, codes, _, _ := prepareFixture(t, 1, true, 0)
			if err := d.prepareOrder(ctx, "order-00"); err != nil {
				t.Fatal(err)
			}
			if kind == "invoice" {
				if _, err := d.db.Exec(`UPDATE printer_invoice_settings SET tray='changed'`); err != nil {
					t.Fatal(err)
				}
			} else {
				d.config.DefaultQueue = "changed"
			}
			if _, err := p.Claim(ctx, codes[0]); err != nil {
				t.Fatal(err)
			}
			d.tick(ctx)
			if len(b.refs) != 0 {
				t.Fatal("changed plan printed")
			}
		})
	}
}

func TestPreparedInvoiceFailureDoesNotReplayDocuments(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, _ := prepareFixture(t, 1, true, 0)
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	b.failInvoice = true
	d.tick(ctx)
	d.tick(ctx)
	if len(b.refs) != 1 || b.refs[0].Invoice {
		t.Fatal("document replayed after uncertain invoice")
	}
	b.failInvoice = false
	if err := d.RequestPrint(ctx, "order-00", true); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(b.refs) != 2 || !b.refs[1].Invoice {
		t.Fatal("explicit review retry did not isolate invoice")
	}
}

func TestPreparedSourceChecksumFailureLeavesCodeUnclaimed(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, _ := prepareFixture(t, 1, false, 4)
	if _, err := d.db.Exec(`UPDATE documents SET sha256=?`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := d.prepareWaiting(ctx); err == nil {
		t.Fatal("source corruption accepted")
	}
	if _, err := p.Claim(ctx, codes[0]); !errors.Is(err, pickup.ErrPreparing) {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(b.refs) != 0 {
		t.Fatal("corrupt source printed")
	}
}

func TestReadyDigestCannotBeReplaced(t *testing.T) {
	ctx := context.Background()
	d, _, p, _, _, _ := prepareFixture(t, 1, false, 4)
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkPrepared(ctx, "order-00", strings.Repeat("f", 64)); err == nil {
		t.Fatal("ready digest replaced")
	}
}

func TestPreparedCleanupRequiresDocumentsAndInvoiceCompletion(t *testing.T) {
	ctx := context.Background()
	d, _, p, codes, _, _ := prepareFixture(t, 1, true, 0)
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	var digest string
	d.db.QueryRow(`SELECT prepared_digest FROM kiosk_preparations`).Scan(&digest)
	if err := d.cleanupPrepared(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.config.Prepared.Load("order-00", digest); err != nil {
		t.Fatal("uncollected file deleted", err)
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if _, err := d.db.Exec(`UPDATE print_submissions SET progress='completed'`); err != nil {
		t.Fatal(err)
	}
	if err := d.cleanupPrepared(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.config.Prepared.Load("order-00", digest); err != nil {
		t.Fatal("invoice still incomplete", err)
	}
	if _, err := d.db.Exec(`UPDATE separator_invoices SET progress='completed'`); err != nil {
		t.Fatal(err)
	}
	if err := d.cleanupPrepared(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.config.Prepared.Load("order-00", digest); err == nil {
		t.Fatal("completed files remain")
	}
	var orders, plans int
	d.db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&orders)
	d.db.QueryRow(`SELECT COUNT(*) FROM kiosk_preparations WHERE purged_at>0`).Scan(&plans)
	if orders != 1 || plans != 1 {
		t.Fatal("order details lost", orders, plans)
	}
	if err := d.cleanupPrepared(ctx); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}

func TestPrintWorkersStopAndRestart(t *testing.T) {
	d, _, _, _, _, _ := prepareFixture(t, 1, false, 0)
	w := NewWorkers(d)
	for i := 0; i < 2; i++ {
		if err := w.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := w.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := w.Stop(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if w.Ready(context.Background()) == nil {
			t.Fatal("stopped worker reports ready")
		}
	}
}
