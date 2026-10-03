package dispatch

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"strings"
	"testing"
)

func TestKioskPaymentAndManualRequestCannotRelease(t *testing.T) {
	ctx := context.Background()
	d, backend, db := separatorFixture(t, 2, true, 0)
	d.config.RequirePickup = true
	if !DefaultDispatcherConfig().RequirePickup {
		t.Fatal("production pickup guard disabled")
	}
	if _, err := db.Exec("UPDATE business_settings SET auto_print_mode='all_documents'"); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if len(backend.refs) != 0 {
		t.Fatal("payment auto-printed")
	}
	if err := d.RequestPrint(ctx, "order-00", false); err == nil {
		t.Fatal("manual print bypassed pickup")
	}
	if err := d.dispatchOrder(ctx, "order-00", "", "", "paid"); err == nil {
		t.Fatal("direct dispatch bypassed pickup")
	}
	s, err := pickup.New(db, []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueVerified(ctx, "order-00", "verified-gateway-reference")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkPrepared(ctx, "order-00", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE business_settings SET auto_print_mode='off'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, code); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if err = d.LastError(); err != nil {
		t.Fatal(err)
	}
	if len(backend.refs) != 2 {
		t.Fatalf("want one document and invoice, got %d", len(backend.refs))
	}
	for _, ref := range backend.refs {
		if ref.OrderID != "order-00" {
			t.Fatal("unclaimed order printed")
		}
	}
	d.tick(ctx)
	if len(backend.refs) != 2 {
		t.Fatal("repeat dispatch duplicated output")
	}
	restarted := NewWithDefaultQueue(db, d.docs, backend, d.config)
	restarted.tick(ctx)
	if len(backend.refs) != 2 {
		t.Fatal("restart duplicated output")
	}
}
