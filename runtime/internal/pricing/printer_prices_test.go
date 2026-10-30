package pricing

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
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
	if _, err = s.SaveGrid(ctx, p.ID, Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 400}}}); !errors.Is(err, ErrSetup) {
		t.Fatalf("first override should explain shared setup: %v", err)
	}
	base := Entry{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250}
	if _, err = s.SaveGrid(ctx, "", Input{Entries: []Entry{base}}); err != nil {
		t.Fatal(err)
	}
	ready, err := (provisioning.LocalReadiness{DB: db.DB()}).ProductionReady(ctx)
	if err != nil || !ready {
		t.Fatalf("saving shared A4 must unlock portal: ready=%v error=%v", ready, err)
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

func TestGridWithoutBusinessProfileExplainsSetup(t *testing.T) {
	db, s, _ := fixture(t)
	if _, err := db.DB().Exec("DELETE FROM business_profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GridBook(context.Background(), ""); !errors.Is(err, ErrSetup) {
		t.Fatalf("want setup guidance, got %v", err)
	}
}

func TestSharedColourDuplexRatesPersistWithMonoQueue(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	ps := printers.New(db.DB())
	p, err := ps.Register(ctx, printers.RegisterInput{Backend: printers.BackendWindows, QueueName: "Mono only", Capabilities: printers.NormalizeIPPAttributes(printers.RawAttributes{"media-supported": {"A4"}, "print-color-mode-supported": {"monochrome"}, "sides-supported": {"one-sided"}})})
	if err != nil {
		t.Fatal(err)
	}
	if err = ps.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = ps.SetPaperEnabled(ctx, p.ID, "A4", true, "owner"); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{}
	for _, colour := range []string{ColourMonochrome, ColourColour} {
		for _, sides := range []string{SidesOneSided, SidesTwoSidedLongEdge, SidesTwoSidedShortEdge} {
			entries = append(entries, Entry{PaperSize: "A4", ColourMode: colour, Sides: sides, UnitPriceMinor: int64(100 + len(entries)*100)})
		}
	}
	if _, err = s.SaveGrid(ctx, "", Input{Entries: entries}); err != nil {
		t.Fatal(err)
	}
	book, err := New(db.DB()).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 6 {
		t.Fatalf("lost rates: %+v", book.Entries)
	}
	for _, want := range entries {
		found := false
		for _, got := range book.Entries {
			if entryKey(got) == entryKey(want) && got.UnitPriceMinor == want.UnitPriceMinor {
				found = true
			}
		}
		if !found {
			t.Fatalf("rate not persisted: %+v", want)
		}
	}
	fleet, err := printers.Eligible(ctx, db.DB(), "", p.ID)
	if err != nil || len(fleet) != 1 {
		t.Fatal(err)
	}
	if fleet[0].Supports("A4", ColourColour, SidesTwoSidedLongEdge) {
		t.Fatal("saved price invented hardware capabilities")
	}
	if _, err = s.SaveGrid(ctx, p.ID, Input{Entries: []Entry{entries[5]}}); err == nil {
		t.Fatal("unsupported printer override accepted")
	}
}
