package localserver_test

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"net/http"
	"testing"
)

func TestPortalOptionsUsePrinterOverride(t *testing.T) {
	ts, db := portalFixture(t)
	ctx := context.Background()
	var id string
	if err := db.DB().QueryRow("SELECT id FROM printers WHERE enabled=1 LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	prices := pricing.New(db.DB())
	if _, err := prices.SaveGrid(ctx, id, pricing.Input{Entries: []pricing.Entry{{PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", UnitPriceMinor: 725}}}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/api/v1/portal/options")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Selected string          `json:"selectedPrinterId"`
		Required bool            `json:"printerSelectionRequired"`
		Entries  []pricing.Entry `json:"combinations"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !result.Required || result.Selected != id {
		t.Fatalf("selection result %+v status %d", result, resp.StatusCode)
	}
	for _, e := range result.Entries {
		if e.PaperSize == "A4" && e.ColourMode == "monochrome" && e.Sides == "one-sided" {
			if e.UnitPriceMinor != 725 {
				t.Fatalf("portal price %d", e.UnitPriceMinor)
			}
			return
		}
	}
	t.Fatal("overridden combination missing")
}
