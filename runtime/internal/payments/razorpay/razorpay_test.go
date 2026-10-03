package razorpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
)

func validCredentials() Credentials {
	return Credentials{
		KeyID:         "rzp_test_keyid",
		KeySecret:     "rzp_test_keysecret",
		WebhookSecret: "whsec_test",
	}
}

func credentialsBlob(t *testing.T) []byte {
	t.Helper()
	c := validCredentials()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestParseCredentialsJSON(t *testing.T) {
	got, err := ParseCredentials(credentialsBlob(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.KeyID != "rzp_test_keyid" {
		t.Fatalf("key id mismatch: %q", got.KeyID)
	}
	if got.KeySecret != "rzp_test_keysecret" {
		t.Fatalf("key secret mismatch: %q", got.KeySecret)
	}
	if got.WebhookSecret != "whsec_test" {
		t.Fatalf("webhook secret mismatch: %q", got.WebhookSecret)
	}
}

func TestParseCredentialsLegacyNewline(t *testing.T) {
	got, err := ParseCredentials([]byte("kid\nksec\nwsec"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.KeyID != "kid" || got.KeySecret != "ksec" || got.WebhookSecret != "wsec" {
		t.Fatalf("unexpected credentials: %+v", got)
	}
}

func TestParseCredentialsRejectsMissingFields(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(`{"key_id":"x","key_secret":"y"}`), // no webhook
		[]byte(`kid\nksec`),                       // only two
	}
	for _, c := range cases {
		if _, err := ParseCredentials(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestCreateSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/payment_links" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "rzp_test_keyid" || pass != "rzp_test_keysecret" {
			t.Fatalf("basic auth = %q:%q ok=%v", user, pass, ok)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got, _ := body["amount"].(float64); int64(got) != 12345 {
			t.Fatalf("amount = %v, want 12345", body["amount"])
		}
		if body["currency"] != "INR" {
			t.Fatalf("currency = %v", body["currency"])
		}
		if body["callback_url"] != "https://print.example/portal/" || body["callback_method"] != "get" {
			t.Fatalf("missing payment return: %v", body)
		}
		customer := body["customer"].(map[string]any)
		if customer["contact"] != "+919999999999" {
			t.Fatalf("contact not passed: %v", customer)
		}
		notes, _ := body["notes"].(map[string]any)
		if notes["intent_id"] != "intent_abc" {
			t.Fatalf("notes.intent_id = %v", notes["intent_id"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        "plink_xyz",
			"short_url": "https://rzp.io/i/abc",
			"status":    "created",
			"amount":    12345,
			"currency":  "INR",
			"expire_by": time.Now().Add(20 * time.Minute).Unix(),
		})
	}))
	defer server.Close()

	adapter := New().WithEndpoint(server.URL + "/v1")
	intent := payments.Intent{
		ID:             "intent_abc",
		ReturnURL:      "https://print.example/portal/",
		OrderID:        "ord_1",
		InstallationID: "inst_1",
		AmountMinor:    12345,
		Currency:       "INR",
		CustomerName:   "Alice",
		CustomerPhone:  "+919999999999",
		CustomerEmail:  "alice@example.com",
	}
	got, err := adapter.Create(intent, credentialsBlob(t))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.GatewayOrderID != "plink_xyz" {
		t.Fatalf("order id = %q", got.GatewayOrderID)
	}
	if got.RedirectURL != "https://rzp.io/i/abc" {
		t.Fatalf("redirect = %q", got.RedirectURL)
	}
	if got.ExpiresAt.IsZero() {
		t.Fatalf("expires_at not set")
	}
}

func TestCreateServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"code":"BAD_REQUEST_AUTH","description":"key id invalid"}}`)
	}))
	defer server.Close()

	adapter := New().WithEndpoint(server.URL + "/v1")
	_, err := adapter.Create(payments.Intent{AmountMinor: 1, Currency: "INR"}, credentialsBlob(t))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want status mention", err)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"event":"payment_link.paid"}`)
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	adapter := New()
	if err := adapter.VerifyWebhookSignature(body, sig, credentialsBlob(t)); err != nil {
		t.Fatalf("verify with valid sig: %v", err)
	}
	if err := adapter.VerifyWebhookSignature(body, "deadbeef", credentialsBlob(t)); err == nil {
		t.Fatal("verify with wrong sig should fail")
	}
	if err := adapter.VerifyWebhookSignature(body, "", credentialsBlob(t)); err == nil {
		t.Fatal("verify with empty sig should fail")
	}
}

func TestParseWebhookPaid(t *testing.T) {
	body := []byte(`{
		"id":"evt_AbCdEf1234567890",
		"entity":"event",
		"account_id":"acc_x",
		"event":"payment_link.paid",
		"contains":["payment_link","payment"],
		"created_at":1700000000,
		"payload":{
			"payment_link":{"entity":{
				"id":"plink_xyz","amount":50000,"currency":"INR","status":"paid",
				"notes":{"intent_id":"intent_abc","order_id":"ord_1"}
			}},
			"payment":{"entity":{
				"id":"pay_123","amount":50000,"currency":"INR","status":"captured","order_id":"plink_xyz"
			}}
		}
	}`)
	adapter := New()
	if err := adapter.VerifyWebhookSignature(body, signBodyHex(body, "whsec_test"), credentialsBlob(t)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	webhook, err := adapter.ParseWebhook(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if webhook.IntentID != "intent_abc" {
		t.Fatalf("intent id = %q", webhook.IntentID)
	}
	if webhook.GatewayOrderID != "plink_xyz" {
		t.Fatalf("order id = %q", webhook.GatewayOrderID)
	}
	if webhook.GatewayPaymentID != "pay_123" {
		t.Fatalf("payment id = %q", webhook.GatewayPaymentID)
	}
	if webhook.Status != payments.StatusCaptured {
		t.Fatalf("status = %q", webhook.Status)
	}
	if webhook.AmountMinor != 50000 {
		t.Fatalf("amount = %d", webhook.AmountMinor)
	}
	if webhook.Currency != "INR" {
		t.Fatalf("currency = %q", webhook.Currency)
	}
	if webhook.IdempotencyKey != "evt_AbCdEf1234567890" {
		t.Fatalf("idempotency key = %q, want the razorpay event id", webhook.IdempotencyKey)
	}
	if webhook.GatewayEventID != "acc_x:evt_AbCdEf1234567890" {
		t.Fatalf("gateway event id = %q, want acc_x:evt_*", webhook.GatewayEventID)
	}
}

func TestParseWebhookPartiallyPaid(t *testing.T) {
	body := []byte(`{
		"id":"evt_partial_1",
		"entity":"event",
		"account_id":"acc_x",
		"event":"payment_link.partially_paid",
		"created_at":1700000000,
		"payload":{
			"payment_link":{"entity":{
				"id":"plink_xyz","amount":50000,"amount_paid":20000,"currency":"INR","status":"partially_paid",
				"notes":{"intent_id":"intent_abc"}
			}}
		}
	}`)
	webhook, err := New().ParseWebhook(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if webhook.Status != payments.StatusAuthorized {
		t.Fatalf("status = %q, want authorized", webhook.Status)
	}
}

func TestParseWebhookFailed(t *testing.T) {
	body := []byte(`{
		"entity":"event",
		"account_id":"acc_x",
		"event":"payment.failed",
		"created_at":1700000000,
		"payload":{"payment":{"entity":{"id":"pay_1","amount":100,"currency":"INR","status":"failed"}}}
	}`)
	adapter := New()
	webhook, err := adapter.ParseWebhook(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if webhook.Status != payments.StatusFailed {
		t.Fatalf("status = %q", webhook.Status)
	}
	if webhook.GatewayPaymentID != "pay_1" {
		t.Fatalf("payment id = %q", webhook.GatewayPaymentID)
	}
}

func TestParseWebhookUnknownEventRejected(t *testing.T) {
	body := []byte(`{"entity":"event","event":"payment_link.never_heard_of","payload":{}}`)
	if _, err := New().ParseWebhook(body); err == nil {
		t.Fatal("expected error for unknown event")
	}
}

func TestParseWebhookRejectsWrongEntity(t *testing.T) {
	body := []byte(`{"entity":"not_an_event"}`)
	if _, err := New().ParseWebhook(body); err == nil {
		t.Fatal("expected error for non-event envelope")
	}
}

func TestCaptureSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/payments/pay_123/capture" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "rzp_test_keyid" || pass != "rzp_test_keysecret" {
			t.Fatalf("basic auth bad")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if got, _ := body["amount"].(float64); int64(got) != 500 {
			t.Fatalf("amount = %v", body["amount"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"pay_123","status":"captured"}`)
	}))
	defer server.Close()

	adapter := New().WithEndpoint(server.URL + "/v1")
	if err := adapter.Capture(context.Background(), "pay_123", 500, credentialsBlob(t)); err != nil {
		t.Fatalf("capture: %v", err)
	}
}

func signBodyHex(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestMapEventStatusUnknown(t *testing.T) {
	if _, err := mapEventStatus("payment_link.unknown_status", "unheard_of", ""); err == nil {
		t.Fatal("expected error for unknown payment_link status")
	}
	if _, err := mapEventStatus("payment.unknown_status", "", "unheard_of"); err == nil {
		t.Fatal("expected error for unknown payment status")
	}
}

func TestAdapterRejectsEmptySecretOnCreate(t *testing.T) {
	if _, err := New().Create(payments.Intent{}, []byte{}); err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestAdapterDoesNotLeakSecret(t *testing.T) {
	var captured bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&captured, r.Body)
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":"x"}`)
	}))
	defer server.Close()
	_, _ = New().WithEndpoint(server.URL+"/v1").Create(payments.Intent{AmountMinor: 1, Currency: "INR"}, credentialsBlob(t))
	if bytes.Contains(captured.Bytes(), []byte("rzp_test_keysecret")) {
		t.Fatalf("secret leaked in request body: %s", captured.String())
	}
}
