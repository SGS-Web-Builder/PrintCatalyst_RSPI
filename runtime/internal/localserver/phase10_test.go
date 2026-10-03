package localserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pairing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/reports"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// portalFixtureWithPairing builds a server with all services including the
// pairing service wired, fully provisioned so the owner can authenticate
// and the public pairing exchange endpoint is accessible.
func portalFixtureWithPairing(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	path := t.TempDir() + "/portal-pairing.sqlite"
	dataRoot := t.TempDir() + "/data"
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, owner.Profile{
		Name: "Campus Prints", Address: "Pune", Phone: "+919999999999",
		Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
	}); err != nil {
		t.Fatal(err)
	}
	pairingSvc, err := pairing.New(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	files := mustNewFiles(t, dataRoot)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	licensingSvc, err := licensing.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	paymentsSvc, err := payments.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	srv := localserver.New(listener.Addr().String(), "installation-test",
		localserver.WithStoreHealth(database),
		localserver.WithProvisioning(provisioning.New(database.DB())),
		localserver.WithOwner(accounts, ""),
		localserver.WithPricing(pricingSvc),
		localserver.WithOrders(orders.New(database.DB(), pricingSvc)),
		localserver.WithReports(reports.New(database.DB())),
		localserver.WithNotifications(notifications.New(database.DB(), nil)),
		localserver.WithPrinters(printers.New(database.DB())),
		localserver.WithIDCards(idcards.New(database.DB(), files)),
		localserver.WithPassports(passport.New(database.DB(), files, passport.GeometricDetector{})),
		localserver.WithTunnel(tunnel.New(database.DB())),
		localserver.WithLicensing(licensingSvc),
		localserver.WithPayments(paymentsSvc),
		localserver.WithPairing(pairingSvc),
		localserver.WithDB(database.DB()),
		localserver.WithFiles(files),
	)
	ts := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: srv.Handler()},
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, database
}

// ---- Phase 10: Companion pairing endpoints ----

func TestPairingListRequiresAuth(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/pairing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated status = %d, want 401/403", resp.StatusCode)
	}
}

func TestPairingInitiateCreatesCode(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/initiate", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
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
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	code, ok := body["code"].(string)
	if !ok || code == "" {
		t.Error("code missing or empty")
	}
	if len(code) != 8 {
		t.Errorf("code length = %d, want 8", len(code))
	}
	deepLink, ok := body["deepLink"].(string)
	if !ok || !strings.Contains(deepLink, code) {
		t.Errorf("deepLink missing code: %s", deepLink)
	}
	if !strings.Contains(deepLink, "printcatalyst://pair") {
		t.Errorf("deepLink missing scheme: %s", deepLink)
	}
}

func TestPairingInitiateWithDeepLinkTemplate(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	payload := `{"deepLinkTemplate":"printcatalyst-onpremise://pair?code={{code}}&src=merchant"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/initiate", strings.NewReader(payload))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
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
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	deepLink := body["deepLink"].(string)
	if !strings.Contains(deepLink, "printcatalyst-onpremise://pair") {
		t.Errorf("deepLink = %q, missing custom scheme", deepLink)
	}
	if !strings.Contains(deepLink, "src=merchant") {
		t.Errorf("deepLink = %q, missing static query", deepLink)
	}
}

func TestPairExchangeHappyPath(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)

	// Owner mints a pairing code.
	cookie, csrf := signedInAsOwner(t, ts.URL)
	initReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/initiate", strings.NewReader(`{}`))
	initReq.AddCookie(cookie)
	initReq.Header.Set("X-CSRF-Token", csrf)
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Origin", ts.URL)
	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatal(err)
	}
	var initBody map[string]any
	if err := json.NewDecoder(initResp.Body).Decode(&initBody); err != nil {
		t.Fatal(err)
	}
	initResp.Body.Close()
	code := initBody["code"].(string)

	// Companion exchanges the code (no auth required).
	exchangePayload := map[string]string{
		"code":        code,
		"fingerprint": "alice-iphone-15",
		"label":       "Alice's iPhone 15",
	}
	exJSON, _ := json.Marshal(exchangePayload)
	exReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/pair/exchange", strings.NewReader(string(exJSON)))
	exReq.Header.Set("Content-Type", "application/json")
	exResp, err := http.DefaultClient.Do(exReq)
	if err != nil {
		t.Fatal(err)
	}
	defer exResp.Body.Close()
	if exResp.StatusCode != 200 {
		bs, _ := io.ReadAll(exResp.Body)
		t.Fatalf("exchange status = %d, body = %s", exResp.StatusCode, string(bs))
	}
	var exBody map[string]any
	if err := json.NewDecoder(exResp.Body).Decode(&exBody); err != nil {
		t.Fatal(err)
	}
	if exBody["deviceId"] == "" || exBody["token"] == "" {
		t.Error("deviceId or token missing")
	}
}

func TestPairExchangeRejectsUnknownCode(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	payload := map[string]string{
		"code":        "ZZZZZZZZ",
		"fingerprint": "alice",
		"label":       "Alice",
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/pair/exchange", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Errorf("unknown code: status = %d, want 409", resp.StatusCode)
	}
}

func TestPairExchangeRejectsInvalidFingerprint(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	payload := map[string]string{
		"code":        "ZZZZZZZZ",
		"fingerprint": "",
		"label":       "Alice",
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/pair/exchange", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("invalid fingerprint: status = %d, want 400", resp.StatusCode)
	}
}

func TestPairExchangeRejectsEmptyLabel(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	payload := map[string]string{
		"code":        "ZZZZZZZZ",
		"fingerprint": "alice",
		"label":       "",
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/pair/exchange", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("empty label: status = %d, want 400", resp.StatusCode)
	}
}

func TestPairingRevokeRemovesDevice(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	// Mint and exchange a code.
	initReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/initiate", strings.NewReader(`{}`))
	initReq.AddCookie(cookie)
	initReq.Header.Set("X-CSRF-Token", csrf)
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Origin", ts.URL)
	initResp, _ := http.DefaultClient.Do(initReq)
	var initBody map[string]any
	json.NewDecoder(initResp.Body).Decode(&initBody)
	initResp.Body.Close()
	code := initBody["code"].(string)

	exPayload := map[string]string{"code": code, "fingerprint": "bob-phone", "label": "Bob's Phone"}
	exJSON, _ := json.Marshal(exPayload)
	exReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/pair/exchange", strings.NewReader(string(exJSON)))
	exReq.Header.Set("Content-Type", "application/json")
	exResp, _ := http.DefaultClient.Do(exReq)
	var exBody map[string]any
	json.NewDecoder(exResp.Body).Decode(&exBody)
	exResp.Body.Close()
	deviceID := exBody["deviceId"].(string)

	// Revoke the device.
	revPayload := map[string]string{"deviceId": deviceID, "reason": "lost device"}
	revJSON, _ := json.Marshal(revPayload)
	revReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/revoke", strings.NewReader(string(revJSON)))
	revReq.AddCookie(cookie)
	revReq.Header.Set("X-CSRF-Token", csrf)
	revReq.Header.Set("Content-Type", "application/json")
	revReq.Header.Set("Origin", ts.URL)
	revResp, err := http.DefaultClient.Do(revReq)
	if err != nil {
		t.Fatal(err)
	}
	defer revResp.Body.Close()
	if revResp.StatusCode != 200 {
		bs, _ := io.ReadAll(revResp.Body)
		t.Fatalf("revoke status = %d, body = %s", revResp.StatusCode, string(bs))
	}
}

func TestPairingRevokeRequiresReason(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	payload := map[string]string{"deviceId": "some-device-id", "reason": ""}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/revoke", strings.NewReader(string(body)))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("empty reason: status = %d, want 400", resp.StatusCode)
	}
}

func TestPairingRevokeRequiresAuth(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	payload := map[string]string{"deviceId": "some-device-id", "reason": "lost"}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/revoke", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Errorf("unauthenticated revoke: status = %d, want 401/403", resp.StatusCode)
	}
}

func TestPairingListReturnsDevicesAndCodes(t *testing.T) {
	ts, _ := portalFixtureWithPairing(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	// Mint a code and exchange it so we have a device.
	initReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/initiate", strings.NewReader(`{}`))
	initReq.AddCookie(cookie)
	initReq.Header.Set("X-CSRF-Token", csrf)
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Origin", ts.URL)
	initResp, _ := http.DefaultClient.Do(initReq)
	var initBody map[string]any
	json.NewDecoder(initResp.Body).Decode(&initBody)
	initResp.Body.Close()
	code := initBody["code"].(string)

	exPayload := map[string]string{"code": code, "fingerprint": "charlie-tab", "label": "Charlie's Tablet"}
	exJSON, _ := json.Marshal(exPayload)
	exReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/pair/exchange", strings.NewReader(string(exJSON)))
	exReq.Header.Set("Content-Type", "application/json")
	http.DefaultClient.Do(exReq)

	// Mint one more pending code.
	pendingReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/pairing/initiate", strings.NewReader(`{}`))
	pendingReq.AddCookie(cookie)
	pendingReq.Header.Set("X-CSRF-Token", csrf)
	pendingReq.Header.Set("Content-Type", "application/json")
	pendingReq.Header.Set("Origin", ts.URL)
	http.DefaultClient.Do(pendingReq)

	// List the pairing status.
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/pairing", nil)
	listReq.AddCookie(cookie)
	listReq.Header.Set("X-CSRF-Token", csrf)
	listReq.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != 200 {
		bs, _ := io.ReadAll(listResp.Body)
		t.Fatalf("list status = %d, body = %s", listResp.StatusCode, string(bs))
	}
	var status map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	devices, ok := status["devices"].([]any)
	if !ok {
		t.Fatal("devices field missing or not array")
	}
	if len(devices) != 1 {
		t.Errorf("devices count = %d, want 1", len(devices))
	}
	codes, ok := status["pendingCodes"].([]any)
	if !ok {
		t.Fatal("pendingCodes field missing or not array")
	}
	if len(codes) != 2 {
		t.Errorf("codes count = %d, want 2", len(codes))
	}
}
