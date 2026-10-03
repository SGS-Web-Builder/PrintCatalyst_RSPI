package orders_test

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"testing"
)

func TestQuoteUsesSelectedPrinterPrice(t *testing.T) {
	db, prices, s := fixture(t)
	ctx := context.Background()
	ps := printers.New(db.DB())
	p, err := ps.Register(ctx, printers.RegisterInput{Backend: printers.BackendWindows, QueueName: "Quote prices", Capabilities: printers.NormalizeIPPAttributes(printers.RawAttributes{"media-supported": {"A4"}, "print-color-mode-supported": {"monochrome"}, "sides-supported": {"one-sided"}})})
	if err != nil {
		t.Fatal(err)
	}
	if err = ps.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = ps.SetPaperEnabled(ctx, p.ID, "A4", true, "owner"); err != nil {
		t.Fatal(err)
	}
	_, err = prices.SaveGrid(ctx, p.ID, pricing.Input{Entries: []pricing.Entry{{PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", UnitPriceMinor: 500, Tiers: []pricing.Tier{{MinQuantity: 10, UnitPriceMinor: 400}}}}})
	if err != nil {
		t.Fatal(err)
	}
	req := orders.QuoteRequest{PrimaryPrinterID: p.ID, Lines: []orders.QuoteLineRequest{{DocumentID: "doc", PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", Copies: 2, PageRangeStart: 1, PageRangeEnd: 5}}}
	q, err := s.ComputeQuote(ctx, map[string]int{"doc": 5}, req)
	if err != nil {
		t.Fatal(err)
	}
	if q.TotalMinor != 4000 {
		t.Fatalf("total %d want 4000", q.TotalMinor)
	}
	req.PrimaryPrinterID = ""
	if _, err = s.ComputeQuote(ctx, map[string]int{"doc": 5}, req); err == nil {
		t.Fatal("ambiguous printer quote accepted")
	}
}

func TestSharedGridPriceOverridesLegacyServicePrice(t *testing.T) {
	db, prices, svc := fixture(t)
	ctx := context.Background()
	_, err := prices.Save(ctx, pricing.Input{Entries: []pricing.Entry{{PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", UnitPriceMinor: 500}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`INSERT INTO services(id,code,display_name,created_at,updated_at,pricing_json,pricing_currency,pricing_minor_units) VALUES('legacy','legacy','Legacy',1,1,'[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":1000}]','INR',2)`)
	if err != nil {
		t.Fatal(err)
	}
	req := orders.QuoteRequest{ServiceID: "legacy", Lines: []orders.QuoteLineRequest{{DocumentID: "image", PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", Copies: 1, PageRangeStart: 1, PageRangeEnd: 1}}}
	q, err := svc.ComputeQuote(ctx, map[string]int{"image": 1}, req)
	if err != nil {
		t.Fatal(err)
	}
	if q.TotalMinor != 500 || q.Lines[0].Sheets != 1 {
		t.Fatalf("one shared-rate sheet: %+v", q)
	}
	req.Lines[0].Copies = 2
	q, err = svc.ComputeQuote(ctx, map[string]int{"image": 1}, req)
	if err != nil || q.TotalMinor != 1000 {
		t.Fatalf("two copies: %+v %v", q, err)
	}
}
