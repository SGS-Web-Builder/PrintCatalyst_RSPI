package payments

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"strings"
	"testing"
)

func TestAutomaticReleasePolicyVerificationAndReplay(t *testing.T) {
	for _, scenario := range []string{"enabled", "disabled", "unverified", "unprepared", "refund", "re-enabled", "expired-code"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db, provider, w, p := pickupPaymentFixture(t)
			ctx := context.Background()
			raw, _ := json.Marshal(w)
			if _, e := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); e != nil {
				t.Fatal(e)
			}
			db.DB().Exec("UPDATE orders SET status='paid'")
			if e := p.Recover(ctx); e != nil {
				t.Fatal(e)
			}
			if scenario != "unprepared" {
				if e := p.MarkPrepared(ctx, "order", strings.Repeat("c", 64)); e != nil {
					t.Fatal(e)
				}
			}
			if scenario != "enabled" {
				db.DB().Exec("UPDATE kiosk_control SET otp_enabled=0")
			}
			switch scenario {
			case "unverified":
				db.DB().Exec("UPDATE payment_intents SET gateway_payment_id='mismatch'")
			case "re-enabled":
				db.DB().Exec("UPDATE kiosk_control SET otp_enabled=1")
			case "expired-code":
				db.DB().Exec("UPDATE kiosk_pickups SET expires_at=1")
			case "refund":
				db.DB().Exec("INSERT INTO remote_refunds(order_id,intent_id,provider_id,payment_id,amount,currency,request_id,state,reason,updated_at) VALUES('order','intent',?,'pay-test',500,'INR','key','pending','test',1)", provider)
			}
			if scenario == "disabled" || scenario == "expired-code" {
				view, e := p.ViewForOrder(ctx, "order")
				if e != nil || view.Code != "" || view.State != "automatic" {
					t.Fatal(view, e)
				}
			}
			n, e := p.ReleaseAutomatic(ctx)
			if e != nil {
				t.Fatal(e)
			}
			want := 0
			if scenario == "disabled" || scenario == "expired-code" {
				want = 1
			}
			if n != want {
				t.Fatal(n, want)
			}
			again, e := p.ReleaseAutomatic(ctx)
			if e != nil || again != 0 {
				t.Fatal("duplicate release", again, e)
			}
			var releases int
			db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_releases").Scan(&releases)
			if releases != want {
				t.Fatal(releases)
			}
			restarted, e := pickup.New(db.DB(), []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
			if e != nil {
				t.Fatal(e)
			}
			repeated, e := restarted.ReleaseAutomatic(ctx)
			if e != nil || repeated != 0 {
				t.Fatal("restart repeated release", repeated, e)
			}
			if want == 1 {
				var mode string
				db.DB().QueryRow("SELECT release_mode FROM kiosk_releases").Scan(&mode)
				if mode != "automatic" {
					t.Fatal(mode)
				}
				view, e := p.ViewForOrder(ctx, "order")
				if e != nil || view.Code != "" || view.State != "automatic_printing" {
					t.Fatal(view, e)
				}
			}
		})
	}
}
