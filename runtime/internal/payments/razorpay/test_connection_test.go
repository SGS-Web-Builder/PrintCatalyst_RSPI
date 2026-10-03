package razorpay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTestConnectionSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payment_links" {
			t.Fatalf("path = %s, want /v1/payment_links", r.URL.Path)
		}
		if r.URL.Query().Get("count") != "1" {
			t.Fatalf("count = %q, want 1", r.URL.Query().Get("count"))
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "rzp_test_keyid" || pass != "rzp_test_keysecret" {
			t.Fatalf("basic auth = %q:%q ok=%v", user, pass, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"count":0}`))
	}))
	defer server.Close()

	adapter := New().WithEndpoint(server.URL + "/v1")
	if err := adapter.TestConnection(context.Background(), credentialsBlob(t)); err != nil {
		t.Fatalf("test connection: %v", err)
	}
}

func TestTestConnectionUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"BAD_REQUEST_ERROR","description":"Authentication failed"}}`))
	}))
	defer server.Close()

	adapter := New().WithEndpoint(server.URL + "/v1")
	err := adapter.TestConnection(context.Background(), credentialsBlob(t))
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401 mention", err)
	}
}

func TestTestConnectionRejectsEmptySecret(t *testing.T) {
	if err := New().TestConnection(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestTestConnectionRequiresWebhookSecret(t *testing.T) {
	// A blob with only key_id and key_secret (no webhook_secret)
	// must be rejected before any HTTP call.
	blob := []byte(`{"key_id":"rzp_x","key_secret":"secret"}`)
	if err := New().TestConnection(context.Background(), blob); err == nil {
		t.Fatal("expected error for missing webhook_secret")
	}
}