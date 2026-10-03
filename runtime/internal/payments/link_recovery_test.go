package payments

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recoveringGateway struct{ creates, recoveries int }

func (g *recoveringGateway) Create(Intent, []byte) (CreateResult, error) {
	g.creates++
	return CreateResult{}, errors.New("connection lost after gateway accepted request")
}
func (g *recoveringGateway) RecoverCreate(context.Context, Intent, []byte) (CreateResult, error) {
	g.recoveries++
	return CreateResult{GatewayOrderID: "plink-existing", RedirectURL: "https://example.test/pay", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}
func (g *recoveringGateway) VerifyWebhookSignature([]byte, string, []byte) error { return nil }
func (g *recoveringGateway) ParseWebhook([]byte) (Webhook, error)                { return Webhook{}, nil }

func TestMerchantPaymentRetryReusesIntentAndSavedLink(t *testing.T) {
	svc, db := newServiceFixture(t)
	ctx := context.Background()
	gateway := &recoveringGateway{}
	svc.adapters[KindRazorpayMerchant] = gateway
	provider, err := svc.CreateProvider(ctx, ProviderInput{Kind: KindRazorpayMerchant, DisplayName: "Merchant", Enabled: true, Secret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	input := IntentInput{OrderID: "order", ProviderID: provider.ID, AmountMinor: 100, Currency: "INR", CurrencyMinorUnits: 2, IdempotencyKey: "order:order"}
	if _, _, err = svc.CreateIntent(ctx, input); err == nil {
		t.Fatal("expected network error")
	}
	intent, result, err := svc.CreateIntent(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, second, err := svc.CreateIntent(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if intent.ID != again.ID || result.RedirectURL != second.RedirectURL || gateway.creates != 1 || gateway.recoveries != 1 {
		t.Fatal("retry created another payment link")
	}
	var count int
	db.DB().QueryRow("SELECT COUNT(*) FROM payment_intents").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	input.AmountMinor = 200
	if _, _, err = svc.CreateIntent(ctx, input); err == nil {
		t.Fatal("idempotency reused with different amount")
	}
}
