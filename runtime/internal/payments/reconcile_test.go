package payments

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"testing"
	"time"
)

type pollingGateway struct{ calls int }

func (p *pollingGateway) Create(Intent, []byte) (CreateResult, error)         { return CreateResult{}, nil }
func (p *pollingGateway) VerifyWebhookSignature([]byte, string, []byte) error { return nil }
func (p *pollingGateway) ParseWebhook([]byte) (Webhook, error)                { return Webhook{}, nil }
func (p *pollingGateway) FetchCapture(_ context.Context, i Intent, _ []byte) (Webhook, bool, error) {
	p.calls++
	return Webhook{IntentID: i.ID, GatewayOrderID: i.GatewayOrderID, GatewayPaymentID: "pay-1", AmountMinor: i.AmountMinor, Currency: i.Currency, Status: StatusCaptured}, true, nil
}
func TestLANReconciliationCapturesAndRecoversOrder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gateway := &pollingGateway{}
	svc, err := New(db.DB(), dir, WithProviderAdapter(KindRazorpayMerchant, gateway), WithOrders(orders.New(db.DB(), pricing.New(db.DB()))))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := svc.CreateProvider(ctx, ProviderInput{Kind: KindRazorpayMerchant, DisplayName: "Merchant", Enabled: true, Secret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at,submitted_at) VALUES('order','token','pending_payment','INR',2,500,'Customer','',1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.DB().Exec(`INSERT INTO payment_intents(id,order_id,installation_id,provider_id,amount_minor,currency,currency_minor_units,status,gateway_order_id,idempotency_key,created_at,updated_at) VALUES('intent','order','installation',?,500,'INR',2,'redirected','plink_1','key',?,?)`, provider.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	intent, err := svc.loadIntent(ctx, "intent")
	if err != nil || intent.Status != StatusCaptured {
		t.Fatalf("intent %+v %v", intent, err)
	}
	deadline := time.Now().Add(time.Second)
	var status string
	for time.Now().Before(deadline) {
		db.DB().QueryRow("SELECT status FROM orders WHERE id='order'").Scan(&status)
		if status == "paid" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "paid" {
		t.Fatal("capture did not release order")
	}
	if err = svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if gateway.calls != 1 {
		t.Fatal("captured payment fetched again")
	}
	var verified int
	if err = db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_verified_payments WHERE order_id='order'").Scan(&verified); err != nil || verified != 1 {
		t.Fatal("verified API capture did not create durable pickup work", err)
	}
}
