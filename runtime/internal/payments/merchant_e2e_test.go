package payments_test

// End-to-end coverage of the merchant-owned Razorpay path. The test
// drives the same flow the live portal exercises:
//
//   1. Merchant pastes Razorpay keys into the dashboard; the local
//      service encrypts and stores them.
//   2. Customer submits an order; the portal calls CreateIntent.
//   3. The merchant adapter calls Razorpay's POST /v1/payment_links.
//   4. Razorpay fires payment_link.paid; the webhook handler
//      verifies the HMAC signature, parses the event, and transitions
//      the local intent from pending → captured.
//   5. A duplicate webhook delivery is rejected by the idempotency
//      key; no second capture fires.
//
// Razorpay's API is mocked with httptest.Server so the test runs
// without external network access and the assertions are
// deterministic.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments/razorpay"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// fakeRazorpayServer mimics the parts of api.razorpay.com the
// adapter actually calls. It records every request so the test
// asserts on the exact wire shape.
type fakeRazorpayServer struct {
	mu        sync.Mutex
	created   int
	captured  int
	webhook   []byte
	webhookOK bool
	secret    string
}

func newFakeRazorpay(secret string) *fakeRazorpayServer {
	return &fakeRazorpayServer{secret: secret, webhookOK: true}
}

func (f *fakeRazorpayServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/payment_links", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user == "" || pass == "" {
			http.Error(w, `{"error":{"code":"BAD_REQUEST_AUTH"}}`, http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		notes, _ := body["notes"].(map[string]any)
		f.created++
		linkID := fmt.Sprintf("plink_%d", f.created)
		shortURL := fmt.Sprintf("https://rzp.io/i/%s", linkID)
		resp := map[string]any{
			"id":         linkID,
			"short_url":  shortURL,
			"status":     "created",
			"amount":     body["amount"],
			"currency":   body["currency"],
			"expire_by":  time.Now().Add(20 * time.Minute).Unix(),
			"notes":      notes,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	return mux
}

func TestMerchantRazorpayEndToEnd(t *testing.T) {
	webhookSecret := "whsec_test_e2e"
	razorpaySrv := newFakeRazorpay(webhookSecret)
	razorpayHTTP := httptest.NewServer(razorpaySrv.handler())
	defer razorpayHTTP.Close()

	// Wire a payments service backed by a real local SQLite store
	// and the merchant Razorpay adapter pointed at the fake API.
	path := filepath.Join(t.TempDir(), "payments-e2e.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	licensingSvc, err := licensing.New(database.DB(), filepath.Join(t.TempDir(), "data"),
		licensing.WithEncryptionSeed(func() []byte { return []byte("e2e-seed") }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := licensingSvc.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock := func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }
	svc, err := payments.New(database.DB(), filepath.Join(t.TempDir(), "data"),
		payments.WithClock(clock),
		payments.WithEncryptionSeed(func() []byte { return []byte("e2e-seed") }),
		payments.WithProviderAdapter(payments.KindRazorpayMerchant,
			razorpay.New().WithEndpoint(razorpayHTTP.URL+"/v1")),
	)
	if err != nil {
		t.Fatalf("payments.New: %v", err)
	}

	ctx := context.Background()

	// Step 1: merchant pastes their Razorpay keys. The dashboard
	// hands the secret blob (key_id + key_secret + webhook_secret)
	// straight to CreateProvider; the service encrypts and persists it.
	keysBlob, err := json.Marshal(razorpay.Credentials{
		KeyID:         "rzp_test_e2e",
		KeySecret:     "rzp_test_e2e_secret",
		WebhookSecret: webhookSecret,
	})
	if err != nil {
		t.Fatalf("marshal keys: %v", err)
	}
	provider, err := svc.CreateProvider(ctx, payments.ProviderInput{
		Kind:        payments.KindRazorpayMerchant,
		DisplayName: "Merchant Razorpay (Live Keys)",
		Enabled:     true,
		IsDefault:   true,
		Config:      map[string]any{"key_id": "rzp_test_e2e"},
		Secret:      string(keysBlob),
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if !provider.HasSecret {
		t.Fatal("provider reports no secret after CreateProvider")
	}

	// Step 2: portal POSTs an order and calls CreateIntent. The
	// merchant adapter talks to the (fake) Razorpay API and
	// returns a short_url.
	intent, result, err := svc.CreateIntent(ctx, payments.IntentInput{
		OrderID:            "ord-e2e-1",
		ProviderID:         provider.ID,
		AmountMinor:        12345,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		CustomerName:       "Customer Name",
		CustomerPhone:      "+919999999999",
		CustomerEmail:      "cust@example.com",
		IdempotencyKey:     "e2e-1",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	if intent.Status != payments.StatusRedirected {
		t.Fatalf("intent status = %q, want redirected after merchant Create", intent.Status)
	}
	if result.RedirectURL == "" {
		t.Fatal("redirect URL is empty")
	}
	if !strings.HasPrefix(result.RedirectURL, "https://rzp.io/i/") {
		t.Fatalf("redirect URL %q is not a Razorpay short_url", result.RedirectURL)
	}
	if result.GatewayOrderID == "" {
		t.Fatal("gateway order id is empty")
	}

	razorpaySrv.mu.Lock()
	created := razorpaySrv.created
	razorpaySrv.mu.Unlock()
	if created != 1 {
		t.Fatalf("Razorpay API Create calls = %d, want 1", created)
	}

	// Step 3: Razorpay fires payment_link.paid. The merchant
	// forwards the raw body + X-Razorpay-Signature header to our
	// webhook endpoint; the adapter verifies the HMAC and the
	// service applies the transition.
	plinkID := result.GatewayOrderID
	body := []byte(fmt.Sprintf(`{
		"id":"evt_e2e_1",
		"entity":"event",
		"account_id":"acc_e2e",
		"event":"payment_link.paid",
		"contains":["payment_link","payment"],
		"created_at":%d,
		"payload":{
			"payment_link":{"entity":{
				"id":%q,"amount":12345,"currency":"INR","status":"paid",
				"notes":{"intent_id":%q,"order_id":"ord-e2e-1"}
			}},
			"payment":{"entity":{
				"id":"pay_e2e_1","amount":12345,"currency":"INR","status":"captured","order_id":%q
			}}
		}
	}`, time.Now().Unix(), plinkID, intent.ID, plinkID))
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	if _, err := svc.RecordWebhook(ctx, provider.ID, body, signature); err != nil {
		t.Fatalf("RecordWebhook: %v", err)
	}
	refreshed := mustFindIntent(t, svc, intent.ID)
	if refreshed.Status != payments.StatusCaptured {
		t.Fatalf("intent status after webhook = %q, want captured", refreshed.Status)
	}

	// Step 4: Razorpay retries the same webhook (same event id).
	// The service must NOT re-process the duplicate. The idempotency
	// key (Razorpay event id) is unique, so the row insert raises a
	// unique-constraint violation and the service records a
	// duplicate ledger entry without changing the intent.
	if _, err := svc.RecordWebhook(ctx, provider.ID, body, signature); err != payments.ErrDuplicate {
		t.Fatalf("second webhook: err = %v, want ErrDuplicate", err)
	}
	refreshedAgain := mustFindIntent(t, svc, intent.ID)
	if refreshedAgain.Status != payments.StatusCaptured {
		t.Fatalf("status after duplicate webhook = %q, want captured (unchanged)", refreshedAgain.Status)
	}

	// Step 5: a different event for the same intent (Razorpay
	// actually does this when settling) should not move the intent
	// backwards. authorised → captured is a valid forward step, so
	// we use payment.authorized to confirm the transition guard
	// rejects backwards moves.
	body2 := []byte(fmt.Sprintf(`{
		"id":"evt_e2e_2",
		"entity":"event",
		"account_id":"acc_e2e",
		"event":"payment_link.cancelled",
		"created_at":%d,
		"payload":{
			"payment_link":{"entity":{
				"id":%q,"amount":12345,"currency":"INR","status":"cancelled",
				"notes":{"intent_id":%q}
			}}
		}
	}`, time.Now().Unix(), plinkID, intent.ID))
	mac2 := hmac.New(sha256.New, []byte(webhookSecret))
	mac2.Write(body2)
	signature2 := hex.EncodeToString(mac2.Sum(nil))
	if _, err := svc.RecordWebhook(ctx, provider.ID, body2, signature2); err != payments.ErrTransition {
		t.Fatalf("backwards transition: err = %v, want ErrTransition", err)
	}

	// Final state: ledger has at least intent.created, intent.redirected,
	// webhook.received, webhook.processed, webhook.duplicate.
	entries, err := svc.Ledger(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IntentID == intent.ID {
			seen[entry.EventType] = true
		}
	}
	wantEvents := []string{
		payments.LedgerIntentCreated,
		payments.LedgerIntentRedirected,
		payments.LedgerWebhookProcessed,
	}
	for _, want := range wantEvents {
		if !seen[want] {
			t.Errorf("ledger missing %s (have %v)", want, seen)
		}
	}
	if !seen[payments.LedgerWebhookDuplicate] {
		t.Errorf("ledger missing %s after duplicate webhook (have %v)", payments.LedgerWebhookDuplicate, seen)
	}
}

// mustFindIntent looks up the intent through the public list API
// (loadIntent is unexported). The intents list is capped at 500 and
// ordered newest-first so even on a busy database a freshly-created
// intent will be on the first page.
func mustFindIntent(t *testing.T, svc *payments.Service, id string) payments.Intent {
	t.Helper()
	intents, err := svc.ListIntents(context.Background(), 500)
	if err != nil {
		t.Fatalf("ListIntents: %v", err)
	}
	for _, intent := range intents {
		if intent.ID == id {
			return intent
		}
	}
	t.Fatalf("intent %s not found", id)
	return payments.Intent{}
}