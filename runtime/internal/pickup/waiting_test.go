package pickup

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitingReleaseSurvivesRestartAndRequiresSameValidPickup(t *testing.T) {
	for _, scenario := range []string{"ready", "unpaid", "expired", "reissued", "never-entered", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			key := []byte(strings.Repeat("a", 32))
			lookup := []byte(strings.Repeat("b", 32))
			s, err := New(db.DB(), key, lookup)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			exec := func(q string, args ...any) {
				t.Helper()
				if _, e := db.DB().Exec(q, args...); e != nil {
					t.Fatal(e)
				}
			}
			exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('o','t','paid','INR',2,500,'','',1,1)`)
			code, err := s.IssueVerified(ctx, "o", "verified")
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "never-entered" {
				id, e := s.Claim(ctx, code)
				if id != "o" || !errors.Is(e, ErrPreparing) {
					t.Fatal(id, e)
				}
			}
			if n, e := s.ReleaseWaiting(ctx); e != nil || n != 0 {
				t.Fatal("released before preparation", n, e)
			}
			s, err = New(db.DB(), key, lookup)
			if err != nil {
				t.Fatal(err)
			}
			s.now = func() time.Time { return now }
			if scenario == "reissued" {
				now = now.Add(24 * time.Hour)
				if err = s.ReissueExpired(ctx, "o"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expired" {
				now = now.Add(24 * time.Hour)
			}
			if scenario == "failed" {
				exec(`UPDATE kiosk_pickups SET preparation='failed'`)
			} else if err = s.MarkPrepared(ctx, "o", strings.Repeat("c", 64)); err != nil {
				t.Fatal(err)
			}
			if scenario == "unpaid" {
				exec(`UPDATE orders SET status='pending_payment'`)
			}
			n, err := s.ReleaseWaiting(ctx)
			want := 0
			if scenario == "ready" {
				want = 1
			}
			if err != nil || n != want {
				t.Fatal(n, want, err)
			}
			if n, err = s.ReleaseWaiting(ctx); err != nil || n != 0 {
				t.Fatal("release replayed", n, err)
			}
		})
	}
}
