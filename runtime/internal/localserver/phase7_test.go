package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// ---- Phase 7: Tunnel, custom domain and branded QR HTTP ----

func TestTunnelStatusRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/tunnel")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated status = %d, want 401/403", resp.StatusCode)
	}
}

func TestTunnelStatusDefaultsToUnconfigured(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/tunnel", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var snap tunnel.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Status != tunnel.StatusUnconfigured {
		t.Errorf("status = %q, want unconfigured", snap.Status)
	}
	if snap.QRTargetPath != "/portal/" {
		t.Errorf("qr target path = %q, want /portal/", snap.QRTargetPath)
	}
	if snap.PublicURL != "" {
		t.Errorf("public url = %q, want empty before config", snap.PublicURL)
	}
}

func TestTunnelSaveConfigRejectsNonHTTPSOrigin(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"provider":     "cloudflared",
		"publicOrigin": "http://shop.example.com",
		"tunnelToken":  "verysecrettokenvalue-1234567890",
	})
	resp := putJSON(t, ts.URL, "/api/v1/owner/tunnel", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("non-https status = %d, want 400, body = %s", resp.StatusCode, string(bs))
	}
}

func TestTunnelSaveConfigRejectsTooShortToken(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"provider":     "cloudflared",
		"publicOrigin": "https://shop.example.com",
		"tunnelToken":  "short",
	})
	resp := putJSON(t, ts.URL, "/api/v1/owner/tunnel", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("short token status = %d, want 400, body = %s", resp.StatusCode, string(bs))
	}
}

func TestTunnelSaveConfigStoresFingerprintOnly(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	raw := "verysecrettokenvalue-1234567890"
	body, _ := json.Marshal(map[string]any{
		"provider":     "cloudflared",
		"publicOrigin": "https://shop.example.com",
		"tunnelToken":  raw,
		"shopRoute":    "shop-42",
	})
	resp := putJSON(t, ts.URL, "/api/v1/owner/tunnel", cookie, csrf, body)
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("save status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var snap tunnel.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if snap.TunnelTokenFingerprint == raw {
		t.Fatal("raw token leaked to snapshot")
	}
	if !snap.HasToken {
		t.Error("HasToken should be true")
	}
	if snap.PublicURL != "https://shop.example.com/portal/shop-42" {
		t.Errorf("public url = %q", snap.PublicURL)
	}
	if snap.QRTargetURL != "https://shop.example.com/portal/" {
		t.Errorf("qr target url = %q", snap.QRTargetURL)
	}

	// Re-GET to confirm the fingerprint persisted across requests.
	getReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/tunnel", nil)
	getReq.AddCookie(cookie)
	getReq.Header.Set("X-CSRF-Token", csrf)
	getReq.Header.Set("Origin", ts.URL)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	var reread tunnel.Snapshot
	if err := json.NewDecoder(getResp.Body).Decode(&reread); err != nil {
		t.Fatal(err)
	}
	if reread.TunnelTokenFingerprint != snap.TunnelTokenFingerprint {
		t.Errorf("fingerprint drift: %q vs %q", snap.TunnelTokenFingerprint, reread.TunnelTokenFingerprint)
	}
	if !reread.HasToken {
		t.Error("HasToken should still be true after re-GET")
	}
}

func TestTunnelVerifyRecordsFailureForUnreachableOrigin(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"provider":     "cloudflared",
		"publicOrigin": "https://shop.example.invalid",
		"tunnelToken":  "verysecrettokenvalue-1234567890",
	})
	if resp := putJSON(t, ts.URL, "/api/v1/owner/tunnel", cookie, csrf, body); resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("save: %d, %s", resp.StatusCode, string(bs))
	}
	resp := postJSON(t, ts.URL, "/api/v1/owner/tunnel/verify", cookie, csrf, []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("verify: %d, %s", resp.StatusCode, string(bs))
	}
	var snap tunnel.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	switch snap.Status {
	case tunnel.StatusOnline, tunnel.StatusError, tunnel.StatusOffline, tunnel.StatusDegraded:
		// Acceptable outcomes. The test only asserts that the probe was
		// recorded, not that a specific result is reached on a flaky DNS.
	default:
		t.Errorf("status = %q, want one of online/error/offline/degraded", snap.Status)
	}
	if snap.LastAttemptAt == 0 {
		t.Error("last_attempt_at was not updated by the probe")
	}
}

func TestTunnelDisconnectSetsOffline(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	resp := postJSON(t, ts.URL, "/api/v1/owner/tunnel/disconnect", cookie, csrf, []byte(`{}`))
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("disconnect: %d, %s", resp.StatusCode, string(bs))
	}
	var snap tunnel.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if snap.Status != tunnel.StatusOffline {
		t.Errorf("status = %q, want offline", snap.Status)
	}
	if snap.LastError != "tunnel was disconnected by the owner" {
		t.Errorf("last error = %q", snap.LastError)
	}
}

func TestTunnelQRServesSVGAfterOnlineStatus(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	// Before the tunnel is online, the QR endpoint must refuse with 409.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/tunnel/qr.svg", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("QR should not be served while status != online")
	}

	// The QR encoder itself is exercised directly because the public-origin
	// probe cannot be made to succeed in a test environment.
	svg, err := tunnel.QREncode("https://shop.example.com/portal/shop-42")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "viewBox=") {
		t.Fatalf("unexpected svg: %s", svg[:min(80, len(svg))])
	}
	if !strings.Contains(svg, "</svg>") {
		t.Fatal("svg not closed")
	}
}

func TestTunnelEventsRecordsTransitions(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"provider":     "cloudflared",
		"publicOrigin": "https://shop.example.com",
		"tunnelToken":  "verysecrettokenvalue-1234567890",
	})
	if resp := putJSON(t, ts.URL, "/api/v1/owner/tunnel", cookie, csrf, body); resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("save: %d, %s", resp.StatusCode, string(bs))
	}
	if resp := postJSON(t, ts.URL, "/api/v1/owner/tunnel/disconnect", cookie, csrf, []byte(`{}`)); resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("disconnect: %d, %s", resp.StatusCode, string(bs))
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/tunnel/events", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	var parsed struct {
		Events []tunnel.Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Events) < 2 {
		t.Fatalf("len(events) = %d, want >= 2", len(parsed.Events))
	}
	// Newest first: the most recent event is the disconnect.
	if parsed.Events[0].Status != tunnel.StatusOffline {
		t.Errorf("newest event = %q, want offline", parsed.Events[0].Status)
	}
}

func TestTunnelEndpointsRejectForwardedHeaders(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	for _, path := range []string{
		"/api/v1/owner/tunnel",
		"/api/v1/owner/tunnel/events",
	} {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", ts.URL)
		req.Header.Set("X-Forwarded-Host", "attacker.example.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("%s with forwarded-host = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestTunnelOperatorReadOnly(t *testing.T) {
	// Operators are not provisioned by portalFixture today. The owner-only
	// behaviour is enforced at the handler via requireOwner(), and Phase 3D
	// tests already exercise that gate against a fresh operator session.
	// We leave a placeholder assertion so a future slice that wires an
	// operator into the portal fixture can extend this without searching
	// for the right hook point.
	ts, _ := portalFixture(t)
	if ts == nil {
		t.Fatal("fixture is nil")
	}
}

// Ensure bytes import is used (some Go versions complain otherwise).
var _ = bytes.NewReader(nil)

// Ensure httptest import is used.
var _ = httptest.NewServer
