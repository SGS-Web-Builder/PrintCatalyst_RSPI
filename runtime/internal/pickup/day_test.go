package pickup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSameDayPickupAndLegacyRecovery(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "day.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db.DB(), []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 30, 18, 29, 0, 0, time.UTC) // 23:59 in India
	s.now = func() time.Time { return now }
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('paid','secret','paid','INR',2,500,'Test','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueVerified(ctx, "paid", "verified")
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.ViewForOrder(ctx, "paid")
	if err != nil || view.ExpiresAt != now.Add(time.Minute).Unix() {
		t.Fatalf("expiry: %+v %v", view, err)
	}
	if err = s.MarkPrepared(ctx, "paid", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy rolling-24-hour code; every reader must still enforce the day.
	if _, err = db.DB().Exec("UPDATE kiosk_pickups SET expires_at=?", now.Add(24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err = s.Claim(ctx, code); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("midnight claim: %v", err)
	}
	view, err = s.ViewForOrder(ctx, "paid")
	if err != nil || view.State != "expired" || view.Code != "" {
		t.Fatalf("expired view: %+v %v", view, err)
	}
	if err = s.ReissueExpired(ctx, "paid"); err != nil {
		t.Fatal(err)
	}
	view, err = s.ViewForOrder(ctx, "paid")
	if err != nil || view.State != "active" || view.Code == code {
		t.Fatalf("reissue: %+v %v", view, err)
	}
	if _, err = s.Claim(ctx, view.Code); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, view.Code); !errors.Is(err, ErrUnavailable) {
		t.Fatal("reused code accepted", err)
	}
}

func TestDayEndShopTimezoneDST(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "zone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, _ := json.Marshal(map[string]string{"timeZone": "America/New_York"})
	_, err = db.DB().Exec(`INSERT INTO business_profile(singleton,profile_json,updated_at) VALUES(1,?,1)`, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
	end, err := dayEnd(ctx, db.DB(), at)
	if err != nil || end-at.Unix() != 23*3600 {
		t.Fatalf("DST day end: %d %v", end-at.Unix(), err)
	}
}
