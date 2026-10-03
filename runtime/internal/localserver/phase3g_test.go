package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
)

// ---- Reports ----

func TestOwnerReportsSummaryRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	// No cookie → 401 or 403.
	resp, err := http.Get(ts.URL + "/api/v1/owner/reports/summary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated summary = %d, want 401/403", resp.StatusCode)
	}
}

func TestOwnerReportsSummaryReturnsData(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	orderID := placeAnOrder(t, ts.URL)

	// Move to paid so it counts toward settled revenue.
	putBody, _ := json.Marshal(map[string]string{"status": orders.StatusPaid})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/orders/"+orderID+"/status", bytes.NewReader(putBody))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	// Summary with a wide range.
	summaryReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/reports/summary?from=0&to=9999999999", nil)
	summaryReq.AddCookie(cookie)
	summaryReq.Header.Set("X-CSRF-Token", csrf)
	summaryReq.Header.Set("Origin", ts.URL)
	summaryResp, err := http.DefaultClient.Do(summaryReq)
	if err != nil {
		t.Fatal(err)
	}
	defer summaryResp.Body.Close()
	if summaryResp.StatusCode != 200 {
		bs, _ := io.ReadAll(summaryResp.Body)
		t.Fatalf("summary status = %d, body = %s", summaryResp.StatusCode, string(bs))
	}
	var summary struct {
		ByStatus []struct {
			Status string `json:"status"`
			Count  int    `json:"count"`
		} `json:"byStatus"`
	}
	if err := json.NewDecoder(summaryResp.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.ByStatus) == 0 {
		t.Fatal("expected status rows in summary")
	}
}

func TestOwnerReportsTopCombinations(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	orderID := placeAnOrder(t, ts.URL)

	// Move to paid so it counts towards top combinations.
	putBody, _ := json.Marshal(map[string]string{"status": orders.StatusPaid})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/orders/"+orderID+"/status", bytes.NewReader(putBody))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	comboReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/reports/top-combinations?from=0&to=9999999999&limit=5", nil)
	comboReq.AddCookie(cookie)
	comboReq.Header.Set("X-CSRF-Token", csrf)
	comboReq.Header.Set("Origin", ts.URL)
	comboResp, err := http.DefaultClient.Do(comboReq)
	if err != nil {
		t.Fatal(err)
	}
	defer comboResp.Body.Close()
	if comboResp.StatusCode != 200 {
		bs, _ := io.ReadAll(comboResp.Body)
		t.Fatalf("top-combinations status = %d, body = %s", comboResp.StatusCode, string(bs))
	}
	var combos struct {
		Combinations []struct {
			PaperSize   string `json:"paperSize"`
			ColourMode  string `json:"colourMode"`
			LineCount   int    `json:"lineCount"`
		} `json:"combinations"`
	}
	if err := json.NewDecoder(comboResp.Body).Decode(&combos); err != nil {
		t.Fatal(err)
	}
	if len(combos.Combinations) == 0 {
		t.Fatal("expected at least one combination")
	}
}

// ---- Notifications ----

func TestOwnerNotificationsSettingsRead(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/notifications/settings", nil)
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
		t.Fatalf("settings read status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var settings struct {
		DesktopAlertsEnabled bool `json:"desktopAlertsEnabled"`
		AudioAlertsEnabled bool  `json:"audioAlertsEnabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	// Defaults from migration 008.
	if !settings.DesktopAlertsEnabled {
		t.Fatal("desktop alerts should default to enabled")
	}
	if !settings.AudioAlertsEnabled {
		t.Fatal("audio alerts should default to enabled")
	}
}

func TestOwnerNotificationsSettingsUpdate(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	// Disable desktop alerts.
	body, _ := json.Marshal(map[string]any{
		"desktopAlertsEnabled": false,
		"audioAlertsEnabled":  true,
	})
	updateReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/notifications/settings", bytes.NewReader(body))
	updateReq.AddCookie(cookie)
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("X-CSRF-Token", csrf)
	updateReq.Header.Set("Origin", ts.URL)
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatal(err)
	}
	defer updateResp.Body.Close()
	if updateResp.StatusCode != 200 {
		bs, _ := io.ReadAll(updateResp.Body)
		t.Fatalf("settings update status = %d, body = %s", updateResp.StatusCode, string(bs))
	}
	var updated struct {
		DesktopAlertsEnabled bool `json:"desktopAlertsEnabled"`
	}
	if err := json.NewDecoder(updateResp.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.DesktopAlertsEnabled {
		t.Fatal("desktop alerts should be disabled after update")
	}
}

func TestOwnerNotificationsSettingsRejectsBadPort(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	body, _ := json.Marshal(map[string]any{
		"desktopAlertsEnabled": true,
		"emailEnabled":         true,
		"emailPort":            0, // invalid
	})
	updateReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/notifications/settings", bytes.NewReader(body))
	updateReq.AddCookie(cookie)
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("X-CSRF-Token", csrf)
	updateReq.Header.Set("Origin", ts.URL)
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatal(err)
	}
	defer updateResp.Body.Close()
	if updateResp.StatusCode != 400 {
		bs, _ := io.ReadAll(updateResp.Body)
		t.Fatalf("bad port status = %d, want 400, body = %s", updateResp.StatusCode, string(bs))
	}
}

func TestOwnerNotificationsStreamRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	// No auth → 401/403.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/notifications/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated stream = %d, want 401/403", resp.StatusCode)
	}
}

func TestOwnerNotificationsStreamReturnsSSEHeaders(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/notifications/stream", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	// Use a short timeout so we don't hang.
	client := &http.Client{Timeout: 2e9}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("expected Cache-Control: no-store")
	}
}
