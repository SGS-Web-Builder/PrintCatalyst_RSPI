//go:build licensefree

package pricing

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func TestLicenseFreeBusinessSetupSavesPricingWithoutActivation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fresh.sqlite")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	accounts := owner.New(db.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, testProfile); err != nil {
		t.Fatal(err)
	}
	service := New(db.DB())
	if _, err := service.Save(ctx, Input{Entries: sampleEntries()}); err != nil {
		t.Fatal(err)
	}
	book, err := New(db.DB()).Load(ctx)
	if err != nil || len(book.Entries) != len(sampleEntries()) {
		t.Fatalf("persisted book: %+v, %v", book, err)
	}
	status, err := (provisioning.LocalReadiness{DB: db.DB()}).Status(ctx)
	if err != nil || status.Next != provisioning.GatePrinter {
		t.Fatalf("pricing readiness: %+v, %v", status, err)
	}
	var activated int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM provisioning_gates WHERE gate='licence' AND completed_at IS NOT NULL").Scan(&activated); err != nil {
		t.Fatal(err)
	}
	if activated != 0 {
		t.Fatal("saving prices must not fabricate licence activation")
	}
}
