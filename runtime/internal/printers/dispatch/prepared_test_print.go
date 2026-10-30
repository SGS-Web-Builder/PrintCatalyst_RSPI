package dispatch

import (
	"context"
	"crypto/rand"
	"errors"
)

// submitTest accepts only the application-generated single-page test PDFs from
// TestPrint/TestPrintOn. They already have their final paper size and no customer
// content or page composition; avoid rasterizing them in a Python subprocess.
// Customer documents still use the full prepared-document pipeline.
func (d *Dispatcher) submitTest(ctx context.Context, queue string, body []byte, ref DocumentRef) (string, error) {
	if d.config.Prepared == nil {
		return d.backend.Submit(ctx, queue, body, ref)
	}
	if ref.PageCount != 1 || ref.Copies != 1 || ref.ColourMode != "monochrome" || ref.Sides != "one-sided" {
		return "", errors.New("invalid generated test page")
	}
	ref.MIMEType = "application/pdf"
	ref.Prepared = true
	ref.LineID = rand.Text()
	ref.PagesPerSheet = 1
	ref.PageStart = 0
	ref.PageEnd = 0
	ref.Pages = nil
	return d.backend.Submit(ctx, queue, body, ref)
}
