package payments

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type pickupGateway struct{}

func (pickupGateway) Create(Intent, []byte) (CreateResult, error) { return CreateResult{}, nil }
func (pickupGateway) VerifyWebhookSignature(_ []byte, signature string, _ []byte) error {
	if signature != "verified-test-signature" {
		return ErrWebhook
	}
	return nil
}
func (pickupGateway) ParseWebhook(raw []byte) (Webhook, error) {
	var w Webhook
	err := json.Unmarshal(raw, &w)
	return w, err
}

func pickupPaymentFixture(t *testing.T) (*Service, *store.Store, string, Webhook, *pickup.Service) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := New(db.DB(), dir, WithProviderAdapter(KindRazorpayMerchant, pickupGateway{}), WithOrders(orders.New(db.DB(), pricing.New(db.DB()))))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := svc.CreateProvider(ctx, ProviderInput{Kind: KindRazorpayMerchant, DisplayName: "Test merchant", Enabled: true, Secret: "test-only-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at,submitted_at) VALUES('order','private-token','pending_payment','INR',2,500,'Customer','',1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.DB().Exec(`INSERT INTO payment_intents(id,order_id,installation_id,provider_id,amount_minor,currency,currency_minor_units,status,gateway_order_id,idempotency_key,created_at,updated_at) VALUES('intent','order','installation',?,500,'INR',2,'redirected','plink-test','test-key',?,?)`, provider.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pickup.New(db.DB(), []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	w := Webhook{IntentID: "intent", GatewayOrderID: "plink-test", GatewayPaymentID: "pay-test", GatewayEventID: "event-test", IdempotencyKey: "event-test", AmountMinor: 500, Currency: "INR", Status: StatusCaptured}
	return svc, db, provider.ID, w, p
}

func TestVerifiedCaptureCreatesRecoverablePickupOnlyOnce(t *testing.T) {
	svc, db, provider, w, _ := pickupPaymentFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(w)
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); !errors.Is(err, ErrDuplicate) {
		t.Fatal("duplicate", err)
	}
	// Reconstruct the worker: capture happened before the pickup service ran.
	p, err := pickup.New(db.DB(), []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	var view pickup.View
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err = p.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		view, err = p.ViewForOrder(ctx, "order")
		if err != nil {
			t.Fatal(err)
		}
		if view.Code != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(view.Code) != 4 || view.Preparation != "pending" {
		t.Fatalf("code not recovered: state=%s", view.State)
	}
	if err = p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := p.ViewForOrder(ctx, "order")
	if err != nil || again.Code != view.Code {
		t.Fatal("code changed", err)
	}
	var count int
	db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_pickups").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate pickup")
	}
	db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_releases").Scan(&count)
	if count != 0 {
		t.Fatal("payment released print")
	}
}

func TestPickupRecoveryFailureIsVisibleAndRetryable(t *testing.T) {
	svc, db, provider, w, p := pickupPaymentFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(w)
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); err != nil {
		t.Fatal(err)
	}
	// Force the order state now; the real asynchronous promotion is idempotent.
	if _, err := db.DB().Exec("UPDATE orders SET status='paid'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`CREATE TRIGGER reject_code BEFORE INSERT ON kiosk_pickups BEGIN SELECT RAISE(ABORT,'simulated storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := p.Recover(ctx); err == nil {
		t.Fatal("failure ignored")
	}
	view, err := p.ViewForOrder(ctx, "order")
	if err != nil || view.State != "assistance" || view.Code != "" {
		t.Fatal("failure not shown", err)
	}
	if _, err := db.DB().Exec("DROP TRIGGER reject_code"); err != nil {
		t.Fatal(err)
	}
	if err = p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = p.ViewForOrder(ctx, "order")
	if err != nil || len(view.Code) != 4 {
		t.Fatal("recovery did not retry", err)
	}
}

func TestCaptureAndPickupQueueCommitTogether(t *testing.T) {
	svc, db, provider, w, _ := pickupPaymentFixture(t)
	ctx := context.Background()
	if _, err := db.DB().Exec(`CREATE TRIGGER reject_pickup BEFORE INSERT ON kiosk_verified_payments BEGIN SELECT RAISE(ABORT,'simulated storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(w)
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); err == nil {
		t.Fatal("storage failure ignored")
	}
	var status string
	if err := db.DB().QueryRow("SELECT status FROM payment_intents WHERE id='intent'").Scan(&status); err != nil || status != "redirected" {
		t.Fatal("capture committed without work item", err)
	}
	if _, err := db.DB().Exec("DROP TRIGGER reject_pickup"); err != nil {
		t.Fatal(err)
	}
	// The stored webhook event must not prevent a retry from completing its transaction.
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); !errors.Is(err, ErrDuplicate) {
		t.Fatal("retry failed", err)
	}
	var count int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_verified_payments").Scan(&count); err != nil || count != 1 {
		t.Fatal("retry did not queue pickup", err)
	}
}

func TestCallbackCannotSupplyItsOwnExpectedLink(t *testing.T) {
	svc, db, provider, w, p := pickupPaymentFixture(t)
	if _, err := db.DB().Exec("UPDATE payment_intents SET gateway_order_id='' WHERE id='intent'"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(w)
	ctx := context.Background()
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); !errors.Is(err, ErrDuplicate) {
		t.Fatal(err)
	}
	if err := p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_verified_payments").Scan(&count); err != nil || count != 0 {
		t.Fatal("callback manufactured expected link", err)
	}
}

func TestUnverifiedOrMismatchedPaymentCannotIssuePickup(t *testing.T) {
	for _, name := range []string{"signature", "amount", "currency", "link", "provider", "order-price", "manual-paid"} {
		t.Run(name, func(t *testing.T) {
			svc, db, provider, w, p := pickupPaymentFixture(t)
			ctx := context.Background()
			signature := "verified-test-signature"
			switch name {
			case "signature":
				signature = "invalid"
			case "amount":
				w.AmountMinor = 1
			case "currency":
				w.Currency = "USD"
			case "link":
				w.GatewayOrderID = "different-link"
			case "provider":
				other, err := svc.CreateProvider(ctx, ProviderInput{Kind: KindRazorpayMerchant, DisplayName: "Other", Enabled: true, Secret: "another-test-secret"})
				if err != nil {
					t.Fatal(err)
				}
				provider = other.ID
			case "order-price":
				if _, err := db.DB().Exec("UPDATE orders SET total_minor=999"); err != nil {
					t.Fatal(err)
				}
			case "manual-paid":
				if _, err := db.DB().Exec("UPDATE orders SET status='paid'"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.DB().Exec("UPDATE payment_intents SET status='captured'"); err != nil {
					t.Fatal(err)
				}
			}
			if name != "manual-paid" {
				raw, _ := json.Marshal(w)
				if _, err := svc.RecordWebhook(ctx, provider, raw, signature); err == nil {
					t.Fatal("bad verification accepted")
				}
			}
			if err := p.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_verified_payments").Scan(&count); err != nil || count != 0 {
				t.Fatal("unverified capture queued", count, err)
			}
			if err := db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_pickups").Scan(&count); err != nil || count != 0 {
				t.Fatal("unverified code issued", count, err)
			}
		})
	}
}
