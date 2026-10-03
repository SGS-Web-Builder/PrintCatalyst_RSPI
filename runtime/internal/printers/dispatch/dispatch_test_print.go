package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// TestPrintResult is the outcome of a successful dispatcher test
// print. The localserver returns the queue and the spooler job id
// to the caller (the system tray launcher, in production) so an
// operator can verify the spooler actually accepted the job.
type TestPrintResult struct {
	Queue string
	JobID string
}

// TestPrintContent is the byte sequence submitted to the spooler
// when the operator triggers a test print. The format is RAW
// (driver-specific) and works on every Windows printer driver
// installed by the runtime's Win32 EnumPrintersW path. Each
// line is plain ASCII so a monochrome receipt printer does not
// drop the test page because it cannot decode a richer format.
//
// The contents are intentionally short: a test page is meant to
// confirm the spooler connection, not exercise the full document
// pipeline. Customers will receive real orders through the
// dispatcher's main loop; the test path exists for the "I just
// installed the runtime and I want to know the printer works"
// use case.
var TestPrintContent = testPagePDF()

func testPagePDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	stream := "BT /F1 18 Tf 50 780 Td (Print Catalyst - Printer test) Tj 0 -35 Td /F1 11 Tf (If this page is readable, rendering and the printer driver work.) Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	for i, o := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, o := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return []byte(b.String())
}

// ErrTestPrintNoBackend is returned when the dispatcher was
// constructed with a nil backend. main.go checks this so a
// developer build on macOS / Linux does not log a fatal error.
var ErrTestPrintNoBackend = errors.New("dispatcher: cannot test print without a printer backend (likely running on non-Windows)")

// ErrTestPrintNoQueue is returned when the runtime cannot resolve
// a default printer. The localserver translates this into a 409
// response with an actionable error message.
var ErrTestPrintNoQueue = errors.New("dispatcher: no default printer is configured; set one through the dashboard first")

// TestPrint submits a small RAW test page to the printer queue
// resolved from the configured resolver. The function is the
// backend half of the "Print Test Page" tray menu item: it
// produces a real spool submission so the operator can verify
// the connection without leaving the system tray.
//
// The function prefers the resolver's DefaultQueue (which reads
// the printers table) over config.DefaultQueue because the
// resolver always returns the merchant's current default even
// when the operator has not changed config.env. A failure to
// resolve the queue is returned to the caller so the localserver
// can surface a precise error message instead of a generic 500.
//
// The caller must supply a context with at least a 5-second
// deadline. The backend.Submit call itself may take longer on a
// stalled spooler; the localserver uses a longer timeout in
// production.
func (d *Dispatcher) TestPrint(ctx context.Context) (TestPrintResult, error) {
	if d.backend == nil {
		return TestPrintResult{}, ErrTestPrintNoBackend
	}
	queue, err := d.resolveDefaultQueue(ctx)
	if err != nil {
		return TestPrintResult{}, fmt.Errorf("resolve default queue: %w", err)
	}
	if strings.TrimSpace(queue) == "" {
		return TestPrintResult{}, ErrTestPrintNoQueue
	}
	jobID, err := d.backend.Submit(ctx, queue, TestPrintContent, DocumentRef{
		OrderID:    "test-print",
		DocumentID: "test-print",
		PageCount:  1,
		ColourMode: "monochrome",
		Sides:      "one-sided",
		PaperSize:  "A4",
		Copies:     1,
	})
	if err != nil {
		return TestPrintResult{}, fmt.Errorf("submit test print: %w", err)
	}
	return TestPrintResult{Queue: queue, JobID: jobID}, nil
}

// resolveDefaultQueue returns the merchant's default printer
// queue, with the same priority as tick: explicit order choice
// is irrelevant for a test print, so the resolver's default is
// the only source we consult. config.DefaultQueue is the
// fallback when no resolver is wired (mostly relevant in tests).
func (d *Dispatcher) resolveDefaultQueue(ctx context.Context) (string, error) {
	if d.resolver != nil {
		queue, err := d.resolver.DefaultQueue(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(queue) != "" {
			return queue, nil
		}
	}
	return d.config.DefaultQueue, nil
}

// TestPrintOn targets the selected Windows queue even while it is hidden from customers.
// Submission never verifies or enables a paper size: the operator must inspect the output.
func (d *Dispatcher) TestPrintOn(ctx context.Context, queue, paper string, widthMM, heightMM int) (TestPrintResult, error) {
	if d.backend == nil {
		return TestPrintResult{}, ErrTestPrintNoBackend
	}
	queues, err := d.backend.Queues(ctx)
	if err != nil {
		return TestPrintResult{}, err
	}
	found := false
	for _, q := range queues {
		if q == queue {
			found = true
		}
	}
	if !found {
		return TestPrintResult{}, fmt.Errorf("printer queue is unavailable in Windows: %s", queue)
	}
	if widthMM <= 0 || heightMM <= 0 {
		return TestPrintResult{}, fmt.Errorf("paper dimensions are missing; refresh the Windows printer driver")
	}
	content := sizedTestPDF(paper, widthMM, heightMM)
	id, err := d.backend.Submit(ctx, queue, content, DocumentRef{OrderID: "paper-test", DocumentID: "paper-test", MIMEType: "application/pdf", PageCount: 1, PageStart: 1, PageEnd: 1, PagesPerSheet: 1, ColourMode: "monochrome", Sides: "one-sided", PaperSize: paper, Copies: 1, Orientation: "portrait"})
	if err != nil {
		return TestPrintResult{}, fmt.Errorf("test print failed: %w", err)
	}
	return TestPrintResult{Queue: queue, JobID: id}, nil
}

func sizedTestPDF(paper string, widthMM, heightMM int) []byte {
	w, h := float64(widthMM)*72/25.4, float64(heightMM)*72/25.4
	safe := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)", "\r", " ", "\n", " ").Replace(paper)
	stream := fmt.Sprintf("0.5 w 28 28 %.2f %.2f re S BT /F1 14 Tf 36 %.2f Td (Print Catalyst - Paper test) Tj 0 -25 Td /F1 10 Tf (%s - %d x %d mm) Tj 0 -22 Td (Check paper size, clarity and all four border edges.) Tj 0 -18 Td (Then confirm this size in Printer setup.) Tj ET", w-56, h-56, h-55, safe, widthMM, heightMM)
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", w, h), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}
	for i, o := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, o := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return []byte(b.String())
}
