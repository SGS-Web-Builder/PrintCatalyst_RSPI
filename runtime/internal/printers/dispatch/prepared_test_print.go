package dispatch

import (
	"context"
	"crypto/rand"
	"errors"
)

// Owner test pages use the same renderer; they are explicitly requested admin
// operations and never mutate a customer pickup or order submission journal.
func (d *Dispatcher) submitTest(ctx context.Context, queue string, body []byte, ref DocumentRef) (string, error) {
	if d.config.Prepared == nil {
		return d.backend.Submit(ctx, queue, body, ref)
	}
	if d.config.Renderer == nil {
		return "", errors.New("test page renderer unavailable")
	}
	ref.MIMEType = "application/pdf"
	ref.PageStart = 1
	ref.PageEnd = ref.PageCount
	ref.PagesPerSheet = 1
	ref.Orientation = "portrait"
	pdf, settings, err := d.config.Renderer.RenderPrepared(ctx, body, ref)
	if err != nil {
		return "", err
	}
	ref.Prepared = true
	ref.LineID = rand.Text()
	ref.PageCount = settings.Pages
	ref.PageStart = 0
	ref.PageEnd = 0
	ref.Pages = nil
	ref.Sides = settings.Sides
	return d.backend.Submit(ctx, queue, pdf, ref)
}
