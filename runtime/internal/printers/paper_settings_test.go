package printers_test

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"testing"
)

func TestPaperSettingsControlEligibility(t *testing.T) {
	db, s := fixture(t)
	defer db.Close()
	ctx := context.Background()
	p, err := s.Register(ctx, printers.RegisterInput{Backend: printers.BackendWindows, QueueName: "Shop", Capabilities: printers.NormalizeIPPAttributes(sampleAttributes())})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, true, false, true} {
		if err = s.SetPaperEnabled(ctx, p.ID, "A4", on, "owner"); err != nil {
			t.Fatal(err)
		}
		fleet, e := printers.Eligible(ctx, db.DB(), "", p.ID)
		if e != nil {
			t.Fatal(e)
		}
		if (len(fleet) > 0) != on {
			t.Fatalf("eligible=%v enabled=%v", fleet, on)
		}
		if on && len(fleet[0].Papers) != 1 {
			t.Fatal("duplicate enabled paper rows")
		}
		status, e := (provisioning.LocalReadiness{DB: db.DB()}).Status(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, g := range status.Gates {
			if g.Gate == provisioning.GatePrinter && g.Complete != on {
				t.Fatalf("printer gate=%v want %v", g.Complete, on)
			}
		}
	}
	if err = s.SetPaperEnabled(ctx, p.ID, "unsupported-size", true, "owner"); err == nil {
		t.Fatal("unsupported paper accepted")
	}
}
