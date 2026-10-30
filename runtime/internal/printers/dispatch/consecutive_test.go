package dispatch

import (
	"context"
	"fmt"
	"testing"
)

// A printer can still be processing earlier output. Dispatch must not ask for
// completion, render again, merge jobs or lose each document's frozen settings.
type noCompletionBackend struct {
	*separatorBackend
	t *testing.T
}

func (b *noCompletionBackend) JobProgress(context.Context, string, string, string) (string, string, error) {
	b.t.Fatal("release waited for physical completion")
	return "processing", "", nil
}

func TestTenSeparatePreparedDocumentsKeepSettingsWithoutCompletionWait(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, renderer, _ := prepareFixture(t, 1, false, 0)
	for i := 1; i < 10; i++ {
		sides, colour := "one-sided", "monochrome"
		if i%2 == 1 {
			sides, colour = "two-sided-long-edge", "colour"
		}
		_, err := d.db.Exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,orientation,unit_price_minor,line_total_minor)
SELECT ?,order_id,document_id,paper_size,paper_key,?,?,?,page_range_start,page_range_end,'portrait',unit_price_minor,line_total_minor FROM order_lines WHERE id='order-00-line'`, fmt.Sprintf("separate-%02d", i), colour, sides, i%3+1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	renders := renderer.calls
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	d.backend = &noCompletionBackend{b, t}
	d.tick(ctx)
	if d.LastError() != nil || len(b.refs) != 10 {
		t.Fatal(len(b.refs), d.LastError())
	}
	if renderer.calls != renders {
		t.Fatal("rendering occurred after OTP")
	}
	seen := map[string]bool{}
	for _, ref := range b.refs {
		if seen[ref.LineID] || !ref.Prepared || ref.PaperSize != "A4" {
			t.Fatal("lost independent prepared job", ref)
		}
		seen[ref.LineID] = true
		for i := 1; i < 10; i++ {
			if ref.LineID == fmt.Sprintf("separate-%02d", i) {
				sides, colour := "one-sided", "monochrome"
				if i%2 == 1 {
					sides, colour = "two-sided-long-edge", "colour"
				}
				if ref.Copies != i%3+1 || ref.Sides != sides || ref.ColourMode != colour {
					t.Fatal("settings changed", ref)
				}
			}
		}
	}
	again := New(d.db, d.docs, d.backend, d.resolver, d.config)
	again.tick(ctx)
	if len(b.refs) != 10 {
		t.Fatal("restart replayed documents")
	}
}
