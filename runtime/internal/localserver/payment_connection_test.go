package localserver_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

func TestPaymentSetupRequiresDomainAndGeneratesDistinctSecrets(t *testing.T) {
	ts, db, _ := portalFixtureWithData(t, localserver.WithPublicOrigin("https://print.example.com"))
	cookie, csrf := signedInAsOwner(t, ts.URL)
	path := "/api/v1/owner/payments/webhook-secret"
	resp := postJSON(t, ts.URL, path, cookie, csrf, []byte(`{}`))
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("unconfigured secret status = %d", resp.StatusCode)
	}
	resp = postJSON(t, ts.URL, "/api/v1/owner/payments/providers", cookie, csrf, []byte(`{"kind":"razorpay_merchant","displayName":"Test","secret":"x"}`))
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("unconfigured provider status = %d", resp.StatusCode)
	}
	svc := tunnel.New(db.DB())
	if _, err := svc.SaveConfig(context.Background(), tunnel.Config{Provider: tunnel.ProviderCloudflared, PublicOrigin: "https://print.example.com"}); err != nil {
		t.Fatal(err)
	}
	resp = postJSON(t, ts.URL, path, cookie, csrf, []byte(`{}`))
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("unverified secret status = %d", resp.StatusCode)
	}
	if _, err := db.DB().Exec("UPDATE tunnel_state SET status='online', last_verified_at=123"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		resp = postJSON(t, ts.URL, path, cookie, csrf, []byte(`{}`))
		var result struct {
			Secret string `json:"secret"`
		}
		err := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode != 200 || err != nil {
			t.Fatalf("secret response = %d, %v", resp.StatusCode, err)
		}
		value, err := hex.DecodeString(result.Secret)
		if err != nil || len(value) != 32 || seen[result.Secret] {
			t.Fatal("invalid or reused secret")
		}
		seen[result.Secret] = true
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("secret response may be cached")
		}
	}
	req, _ := http.NewRequest("POST", ts.URL+path, nil)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("anonymous generation = %d", resp.StatusCode)
	}
}
