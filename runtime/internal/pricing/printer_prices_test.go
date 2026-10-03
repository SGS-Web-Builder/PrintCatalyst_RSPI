package pricing

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"testing"
)

func TestPrinterPricingInheritanceAndEligibility(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	ps := printers.New(db.DB())
	p, err := ps.Register(ctx, printers.RegisterInput{Backend: printers.BackendWindows, QueueName: "Prices", Capabilities: printers.NormalizeIPPAttributes(printers.RawAttributes{"media-supported": {"A4"}, "print-color-mode-supported": {"monochrome"}, "sides-supported": {"one-sided"}})})
	if err != nil {
		t.Fatal(err)
	}
	if err = ps.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = ps.SetPaperEnabled(ctx, p.ID, "A4", true, "owner"); err != nil {
		t.Fatal(err)
	}
	base := Entry{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250}
	if _, err = s.SaveGrid(ctx, "", Input{Entries: []Entry{base}}); err != nil {
		t.Fatal(err)
	}
	override := base
	override.UnitPriceMinor = 500
	override.Tiers = []Tier{{MinQuantity: 10, UnitPriceMinor: 400}}
	if _, err = s.SaveGrid(ctx, p.ID, Input{Entries: []Entry{override}}); err != nil {
		t.Fatal(err)
	}
	book, err := s.Effective(ctx, p.ID)
	if err != nil || book.Entries[0].UnitPriceMinor != 500 || len(book.Entries[0].Tiers) != 1 {
		t.Fatalf("override not applied: %+v %v", book, err)
	}
	shared, _ := s.Effective(ctx, "")
	if shared.Entries[0].UnitPriceMinor != 250 {
		t.Fatal("shared price changed")
	}
	if _, err = s.SaveGrid(ctx, p.ID, Input{}); err != nil {
		t.Fatal(err)
	}
	book, _ = s.Effective(ctx, p.ID)
	if book.Entries[0].UnitPriceMinor != 250 {
		t.Fatal("inheritance not restored")
	}
	if err = ps.SetPaperEnabled(ctx, p.ID, "A4", false, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGrid(ctx, "", Input{Entries: []Entry{base}}); err == nil {
		t.Fatal("disabled paper can be priced")
	}
}
