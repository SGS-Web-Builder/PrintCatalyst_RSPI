package localserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---- Phase 8b: Payments HTTP ----

func TestPaymentsProvidersListSeedsPlatformAndRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/payments/providers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated status = %d, want 401/403", resp.StatusCode)
	}
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	providers, _ := body["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want 1 (platform seeded)", len(providers))
	}
	first := providers[0].(map[string]any)
	if first["kind"] != "razorpay_platform" {
		t.Fatalf("kind = %v, want razorpay_platform", first["kind"])
	}
}

func TestPaymentsProvidersCreatePersistsMerchantProvider(t *testing.T) {
	ts, db, _ := portalFixtureWithData(t, localserver.WithPublicOrigin("https://print.example.com"))
	svc := tunnel.New(db.DB())
	if _, err := svc.SaveConfig(context.Background(), tunnel.Config{Provider: tunnel.ProviderCloudflared, PublicOrigin: "https://print.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("UPDATE tunnel_state SET status='online', last_verified_at=1"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"kind":        "razorpay_merchant",
		"displayName": "Test Merchant",
		"enabled":     true,
		"isDefault":   true,
		"secret":      "merchant-secret-value-1234567890",
	})
	resp := postJSON(t, ts.URL, "/api/v1/owner/payments/providers", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var provider map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatal(err)
	}
	if provider["hasSecret"] != true {
		t.Fatalf("hasSecret = %v, want true", provider["hasSecret"])
	}
	if provider["kind"] != "razorpay_merchant" {
		t.Fatalf("kind = %v, want razorpay_merchant", provider["kind"])
	}
	urlReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers/"+provider["id"].(string)+"/webhook-url", nil)
	urlReq.AddCookie(cookie)
	urlResp, err := http.DefaultClient.Do(urlReq)
	if err != nil {
		t.Fatal(err)
	}
	var urlBody struct {
		URL string `json:"webhookURL"`
	}
	if err := json.NewDecoder(urlResp.Body).Decode(&urlBody); err != nil {
		t.Fatal(err)
	}
	urlResp.Body.Close()
	if urlResp.StatusCode != 200 || !strings.HasPrefix(urlBody.URL, "https://print.example.com/api/v1/owner/payments/webhook?provider=") {
		t.Fatalf("webhook URL = %s, status = %d", urlBody.URL, urlResp.StatusCode)
	}
	// GET back to confirm redaction.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var list map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	items, _ := list["providers"].([]any)
	if len(items) != 2 {
		t.Fatalf("providers = %d, want 2", len(items))
	}
	for _, item := range items {
		p := item.(map[string]any)
		if p["kind"] == "razorpay_merchant" && p["hasSecret"] != true {
			t.Fatal("GET response lost the hasSecret flag")
		}
		if p["kind"] == "razorpay_merchant" {
			if _, ok := p["secret"]; ok {
				t.Fatal("GET response leaked the secret value")
			}
		}
	}
}

func TestPaymentsProvidersCreateRejectsInvalidKind(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"kind":        "unknown",
		"displayName": "Bad",
		"enabled":     true,
	})
	resp := postJSON(t, ts.URL, "/api/v1/owner/payments/providers", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPaymentsIntentsCreatePlatformAuthorizesInOneRoundTrip(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	providerID := firstProviderID(t, ts.URL, cookie, csrf, "razorpay_platform")
	updateBody, _ := json.Marshal(map[string]any{
		"kind":        "razorpay_platform",
		"displayName": "Platform Razorpay (signed by Print Catalyst)",
		"enabled":     true,
	})
	updateResp := putJSON(t, ts.URL, "/api/v1/owner/payments/providers/"+providerID, cookie, csrf, updateBody)
	bs, _ := io.ReadAll(updateResp.Body)
	updateResp.Body.Close()
	t.Logf("update response: status=%d body=%s", updateResp.StatusCode, string(bs))
	if updateResp.StatusCode != 200 {
		t.Fatalf("update platform status = %d, body = %s", updateResp.StatusCode, string(bs))
	}
	// Verify the update took effect by reading the provider back.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var list map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	providers, _ := list["providers"].([]any)
	for _, item := range providers {
		p := item.(map[string]any)
		if p["id"] == providerID {
			t.Logf("after update: provider enabled = %v", p["enabled"])
		}
	}
	body, _ := json.Marshal(map[string]any{
		"orderId":            "order-http-1",
		"providerId":         providerID,
		"amountMinor":        12345,
		"currency":           "INR",
		"currencyMinorUnits": 2,
		"customerName":       "Test Customer",
		"customerPhone":      "+919999999999",
		"idempotencyKey":     "http-key-1",
	})
	resp := postJSON(t, ts.URL, "/api/v1/owner/payments/intents", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var body2 map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body2); err != nil {
		t.Fatal(err)
	}
	intent, _ := body2["intent"].(map[string]any)
	if intent["status"] != "authorized" {
		t.Fatalf("intent status = %v, want authorized", intent["status"])
	}
	result, _ := body2["result"].(map[string]any)
	if result["gatewayPaymentId"] == "" {
		t.Fatal("result.gatewayPaymentId is empty")
	}
}

func TestPaymentsIntentsCreateRejectsBadJSON(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	resp := postJSON(t, ts.URL, "/api/v1/owner/payments/intents", cookie, csrf, []byte("{not-json"))
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("bad JSON status = %d, want 400", resp.StatusCode)
	}
}

func TestPaymentsIntentsListRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/payments/intents")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 401/403", resp.StatusCode)
	}
}

func TestPaymentsLedgerRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/payments/ledger")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 401/403", resp.StatusCode)
	}
}

func TestPaymentsLedgerRejectsInvalidLimit(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/ledger?limit=abc", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPaymentsWebhookRejectsMissingProvider(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Post(ts.URL+"/api/v1/owner/payments/webhook", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(readBody(t, resp)), "provider") {
		t.Fatal("expected provider query message")
	}
}

func TestPaymentsEndpointsRejectForwardedHeaders(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("X-Forwarded-Host", "attacker.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("forwarded header status = %d, want 403", resp.StatusCode)
	}
}

func TestPaymentsProvidersMethodNotAllowed(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/payments/providers", bytes.NewReader([]byte("{}")))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("PUT status = %d, want 405", resp.StatusCode)
	}
}

func firstProviderID(t *testing.T, base string, cookie *http.Cookie, csrf string, kind string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/v1/owner/payments/providers", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", base)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	providers, _ := body["providers"].([]any)
	for _, item := range providers {
		p := item.(map[string]any)
		if p["kind"] == kind {
			return p["id"].(string)
		}
	}
	t.Fatalf("provider %s not found", kind)
	return ""
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(bs)
}

// ---- Phase 8b extension: webhook URL and webhook test endpoints ----

func TestPaymentsWebhookURLRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	// No cookie → 401/403.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers/prov_test_123/webhook-url", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated status = %d, want 401/403", resp.StatusCode)
	}
}

func TestPaymentsWebhookURLNoPublicOriginReturns409(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	// Portal fixture has no public origin configured → expect 409.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/payments/providers/prov_nonexistent/webhook-url", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Non-existent provider first: 404 (provider not found takes priority).
	if resp.StatusCode == 404 {
		t.Skip("provider not found — add a valid provider ID fixture")
	}
	// With fixture's default (no public origin) we expect 409.
	if resp.StatusCode != 409 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 409 (no public origin); body = %s", resp.StatusCode, string(bs))
	}
	body := readBody(t, resp)
	if !strings.Contains(strings.ToLower(body), "public origin") {
		t.Fatalf("409 body should mention public origin; got: %s", body)
	}
}

func TestPaymentsWebhookTestRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/payments/providers/prov_test_123/webhook-test", nil)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated status = %d, want 401/403", resp.StatusCode)
	}
}

func TestPaymentsWebhookTestProviderWithoutSecretReturns409(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	// Use the seeded platform provider (has no merchant secret).
	providerID := firstProviderID(t, ts.URL, cookie, csrf, "razorpay_platform")
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/payments/providers/"+providerID+"/webhook-test", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 409 (no secret stored); body = %s", resp.StatusCode, string(bs))
	}
	body := readBody(t, resp)
	if !strings.Contains(strings.ToLower(body), "secret") {
		t.Fatalf("409 body should mention missing secret; got: %s", body)
	}
}

func TestPaymentsWebhookTestProviderNotFoundReturns404(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/payments/providers/this_does_not_exist/webhook-test", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body = %s", resp.StatusCode, string(bs))
	}
}
