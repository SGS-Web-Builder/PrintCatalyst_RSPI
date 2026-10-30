package dispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func actualRenderer(t *testing.T) *PDFRenderer {
	t.Helper()
	path := os.Getenv("PC_RENDERER_PYTHON")
	font := os.Getenv("PC_TEST_INVOICE_FONT")
	if path == "" || font == "" {
		t.Skip("set PC_RENDERER_PYTHON and PC_TEST_INVOICE_FONT for real renderer integration")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return &PDFRenderer{python: absolute, fonts: []string{font}}
}
func TestNativeRendererRealPDFAndInvoice(t *testing.T) {
	r := actualRenderer(t)
	ctx := context.Background()
	ref := DocumentRef{OrderID: "order", LineID: "line", MIMEType: "application/pdf", PaperSize: "A4", ColourMode: "monochrome", Sides: "two-sided-long-edge", Copies: 2, PageCount: 1, PageStart: 1, PageEnd: 1, PagesPerSheet: 1, Orientation: "landscape"}
	pdf, settings, err := r.RenderPrepared(ctx, TestPrintContent, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || settings.Pages != 1 || settings.Copies != 2 || settings.Sides != "two-sided-short-edge" {
		t.Fatal(settings)
	}
	ref.Invoice = true
	ref.MIMEType = "text/plain"
	ref.PaperSize = "A6"
	ref.Sides = "one-sided"
	ref.Copies = 1
	ref.Orientation = "portrait"
	_, settings, err = r.RenderPrepared(ctx, []byte("श्री Print Catalyst\n"+strings.Repeat("A4 one copy INR 5.00\n", 100)), ref)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Pages < 2 {
		t.Fatal("invoice not paginated", settings)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = r.RenderPrepared(canceled, TestPrintContent, ref); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestNativeRendererPipelineAndInvoiceSheetAccounting(t *testing.T) {
	r := actualRenderer(t)
	d, b, p, codes, _, _ := prepareFixture(t, 1, true, 0)
	d.config.Renderer = r
	// Replace the synthetic seed with a real PDF in a private source directory.
	root := t.TempDir()
	files, err := localfiles.New(root)
	if err != nil {
		t.Fatal(err)
	}
	d.docs = documents.New(files, d.db)
	path := filepath.Join(root, "documents", "order-00")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "doc.pdf"), TestPrintContent, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(TestPrintContent)
	if _, err := d.db.Exec(`UPDATE documents SET sha256=?,size_bytes=?`, hex.EncodeToString(hash[:]), len(TestPrintContent)); err != nil {
		t.Fatal(err)
	}
	if err := d.prepareOrder(context.Background(), "order-00"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(context.Background(), codes[0]); err != nil {
		t.Fatal(err)
	}
	d.tick(context.Background())
	if len(b.refs) != 2 {
		t.Fatal(len(b.refs), d.LastError())
	}
	var sheets int
	if err := d.db.QueryRow(`SELECT sheets FROM separator_invoices`).Scan(&sheets); err != nil || sheets < 1 {
		t.Fatal(sheets, err)
	}
}

func TestPrintWorkersStopJoinsRendering(t *testing.T) {
	d, _, _, _, _, _ := prepareFixture(t, 1, false, 4)
	blocker := &blockingRenderer{entered: make(chan struct{})}
	d.config.Renderer = blocker
	workers := NewWorkers(d)
	if err := workers.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := workers.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if workers.Ready(ctx) == nil {
		t.Fatal("stopped worker ready")
	}
}

type blockingRenderer struct{ entered chan struct{} }

func (r *blockingRenderer) RenderPrepared(ctx context.Context, _ []byte, _ DocumentRef) ([]byte, prepared.Settings, error) {
	close(r.entered)
	<-ctx.Done()
	return nil, prepared.Settings{}, ctx.Err()
}

func TestNativeRendererBadInputAndCapacityCancellation(t *testing.T) {
	r := actualRenderer(t)
	ref := DocumentRef{MIMEType: "application/pdf", PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", Copies: 1, PageCount: 1, PageStart: 1, PageEnd: 1, PagesPerSheet: 1, Orientation: "portrait"}
	if _, _, err := r.RenderPrepared(context.Background(), []byte("%PDF-broken"), ref); err == nil {
		t.Fatal("invalid PDF accepted")
	}
	rendererSlots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err := r.RenderPrepared(ctx, TestPrintContent, ref)
	<-rendererSlots
	if err == nil {
		t.Fatal("waiting renderer ignored cancellation")
	}
}
