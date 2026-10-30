package razorpay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteRefundUsesStableIdempotencyAndChecksStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/payments/pay_test/refund" || r.Header.Get("X-Refund-Idempotency") != "pc-refund-order" {
			t.Error("wrong refund identity")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["amount"] != float64(300) || body["speed"] != "normal" {
			t.Error(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"rfnd_test","payment_id":"pay_test","amount":300,"currency":"INR","status":"pending"}`))
	}))
	defer server.Close()
	a := New().WithEndpoint(server.URL).WithHTTPClient(server.Client())
	secret := []byte(`{"key_id":"rzp_test_key","key_secret":"secret","webhook_secret":"hook"}`)
	for i := 0; i < 2; i++ {
		result, e := a.Refund(context.Background(), "pay_test", "pc-refund-order", 300, secret)
		if e != nil || result.Status != "pending" {
			t.Fatal(result, e)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
