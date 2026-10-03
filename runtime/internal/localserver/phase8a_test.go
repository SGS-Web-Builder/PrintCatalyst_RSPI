package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---- Phase 8a: Licensing HTTP ----

func TestLicenceStatusRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/licence")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated status = %d, want 401/403", resp.StatusCode)
	}
}

func TestLicenceStatusReportsUnconfiguredBeforeActivation(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/licence", nil)
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
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["unconfigured"] != true {
		t.Fatalf("status = %v, want unconfigured=true", status)
	}
	if status["installationId"] == "" {
		t.Fatal("status did not include installationId")
	}
}

func TestLicenceActivatePersistsCurrentAndStatusReflectsIt(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/licence/activate", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("activate status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var license map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&license); err != nil {
		t.Fatal(err)
	}
	if license["isCurrent"] != true {
		t.Fatalf("activated licence is not flagged as current: %v", license)
	}
	if entitlements, _ := license["entitlements"].([]any); len(entitlements) == 0 {
		t.Fatal("activated licence carries no entitlements")
	}
	statusReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/licence", nil)
	statusReq.AddCookie(cookie)
	statusReq.Header.Set("X-CSRF-Token", csrf)
	statusReq.Header.Set("Origin", ts.URL)
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	var status map[string]any
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["unconfigured"] == true {
		t.Fatal("Status still reports unconfigured after activation")
	}
	if _, ok := status["current"]; !ok {
		t.Fatal("Status did not include current licence")
	}
	if status["offlineGrace"] != true {
		t.Fatal("Status did not report offline grace")
	}
}

func TestLicenceRefreshSucceedsAfterActivation(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	postJSON(t, ts.URL, "/api/v1/owner/licence/activate", cookie, csrf, []byte("{}"))
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/licence/refresh", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("refresh status = %d, body = %s", resp.StatusCode, string(bs))
	}
}

func TestLicenceRevokeRequiresReason(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	postJSON(t, ts.URL, "/api/v1/owner/licence/activate", cookie, csrf, []byte("{}"))
	body, _ := json.Marshal(map[string]string{"reason": "   "})
	resp := postJSON(t, ts.URL, "/api/v1/owner/licence/revoke", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("empty reason status = %d, want 400", resp.StatusCode)
	}
}

func TestLicenceRevokeAndStatusReflectsRevocation(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	postJSON(t, ts.URL, "/api/v1/owner/licence/activate", cookie, csrf, []byte("{}"))
	body, _ := json.Marshal(map[string]string{"reason": "merchant requested cancellation"})
	resp := postJSON(t, ts.URL, "/api/v1/owner/licence/revoke", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("revoke status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["revoked"] != true {
		t.Fatalf("status = %v, want revoked=true", status)
	}
	if status["revokedReason"] != "merchant requested cancellation" {
		t.Fatalf("revokedReason = %v", status["revokedReason"])
	}
}

func TestLicenceTransferClearsCurrentAndStatusReportsUnconfigured(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	postJSON(t, ts.URL, "/api/v1/owner/licence/activate", cookie, csrf, []byte("{}"))
	resp := postJSON(t, ts.URL, "/api/v1/owner/licence/transfer", cookie, csrf, []byte("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("transfer status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["unconfigured"] != true {
		t.Fatalf("status = %v, want unconfigured=true after transfer", status)
	}
	if _, ok := status["current"]; ok {
		t.Fatal("Status still reports a current licence after transfer")
	}
}

func TestLicenceEventsListsTransitionsNewestFirst(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	postJSON(t, ts.URL, "/api/v1/owner/licence/activate", cookie, csrf, []byte("{}"))
	body, _ := json.Marshal(map[string]string{"reason": "manual"})
	postJSON(t, ts.URL, "/api/v1/owner/licence/revoke", cookie, csrf, body)
	postJSON(t, ts.URL, "/api/v1/owner/licence/transfer", cookie, csrf, []byte("{}"))
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/licence/events?limit=50", nil)
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
		t.Fatalf("events status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var events []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("events count = %d, want at least 4", len(events))
	}
	for i := 1; i < len(events); i++ {
		prev, _ := events[i-1]["occurredAt"].(string)
		cur, _ := events[i]["occurredAt"].(string)
		if prev < cur {
			t.Fatalf("events are not newest-first: %s < %s", prev, cur)
		}
	}
}

func TestLicenceEventsRejectsInvalidLimit(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/licence/events?limit=abc", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("invalid limit status = %d, want 400", resp.StatusCode)
	}
}

func TestLicenceEndpointsRejectForwardedHeaders(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/licence", nil)
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
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("forwarded header status = %d, want 403, body = %s", resp.StatusCode, string(bs))
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/plain") &&
		resp.Header.Get("Content-Type") != "" {
		t.Fatalf("unexpected content type: %s", resp.Header.Get("Content-Type"))
	}
}

func TestLicenceActivateMethodNotAllowed(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/licence/activate", bytes.NewReader([]byte("{}")))
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

func TestLicenceStatusRejectsBadJSONOnRevoke(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	resp := postJSON(t, ts.URL, "/api/v1/owner/licence/revoke", cookie, csrf, []byte("{not-json"))
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("bad JSON status = %d, want 400", resp.StatusCode)
	}
}
