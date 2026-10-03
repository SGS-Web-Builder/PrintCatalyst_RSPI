package localserver

// Regression tests for the eight production-safety bugs found in the audit.
// Each test corresponds to one item on the audit list. They live in
// regression_test.go so a reviewer can map every test back to the bug it
// guards. The tests use httptest with a real loopback listener so the
// localOwnerRequest / portalGuard / pairing paths exercise their network
// code paths, not just their in-memory contracts.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func neturlParse(s string) (string, error) {
	u, err := neturl.Parse(s)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

// regressionFixture builds a fully-provisioned server. The bootstrap token
// is a deterministic value so the tests can exercise both the bootstrap
// path and the post-owner path. The orders service is wired so the portal
// endpoint chain (upload → quote → order) can run end-to-end.
func regressionFixture(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "regression.sqlite")
	dataRoot := t.TempDir()
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	files, err := localfiles.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, gate := range provisioning.OrderedGates {
		if err := provisioning.New(database.DB()).CompleteGate(ctx, gate, "test"); err != nil {
			t.Fatalf("complete %s: %v", gate, err)
		}
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, owner.Profile{
		Name: "Test Shop", Address: "Pune", Phone: "+919999999999",
		Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
	}); err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	if _, err := pricingSvc.Save(ctx, pricing.Input{
		Entries: []pricing.Entry{
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesOneSided, UnitPriceMinor: 250},
			{PaperSize: "A3", ColourMode: pricing.ColourColour, Sides: pricing.SidesOneSided, UnitPriceMinor: 5000},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ordersSvc := orders.New(database.DB(), pricingSvc)
	licensingSvc, err := licensing.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	paymentsSvc, err := payments.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(listener.Addr().String(), "installation-test",
		WithStoreHealth(database),
		WithProvisioning(provisioning.New(database.DB())),
		WithOwner(accounts, "test-bootstrap-token"),
		WithPricing(pricingSvc),
		WithOrders(ordersSvc),
		WithNotifications(notifications.New(database.DB(), nil)),
		WithPrinters(printers.New(database.DB())),
		WithLicensing(licensingSvc),
		WithPayments(paymentsSvc),
		WithDB(database.DB()),
		WithFiles(files),
	)
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.Handler()}}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, database
}

// minimalPDFRegression builds a 3-page PDF that rsc.io/pdf can parse: a
// /Catalog (1) references a /Pages container (2) which lists three leaf
// /Page objects (3, 4, 5). The cross-reference table gives rsc.io/pdf the
// byte offsets it needs to resolve the trailer.
func minimalPDFRegression() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	obj1 := buf.Len()
	buf.WriteString("1 0 obj\n<</Type/Catalog/Pages 2 0 R>>\nendobj\n")
	obj2 := buf.Len()
	buf.WriteString("2 0 obj\n<</Type/Pages/Count 3/Kids[3 0 R 4 0 R 5 0 R]>>\nendobj\n")
	obj3 := buf.Len()
	buf.WriteString("3 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")
	obj4 := buf.Len()
	buf.WriteString("4 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")
	obj5 := buf.Len()
	buf.WriteString("5 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")
	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	fmt.Fprintf(&buf, "%010d 00000 n \n", obj1)
	fmt.Fprintf(&buf, "%010d 00000 n \n", obj2)
	fmt.Fprintf(&buf, "%010d 00000 n \n", obj3)
	fmt.Fprintf(&buf, "%010d 00000 n \n", obj4)
	fmt.Fprintf(&buf, "%010d 00000 n \n", obj5)
	buf.WriteString("trailer\n<</Size 6/Root 1 0 R>>\n")
	fmt.Fprintf(&buf, "startxref\n%d\n", xrefOffset)
	buf.WriteString("%%EOF\n")
	return buf.Bytes()
}

func uploadMultipartRegression(t *testing.T, name, mime string, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="files"; filename=%q`, name)},
		"Content-Type":        {mime},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// TestBootstrapTokenAuthorisesFirstTimeLicenceActivation walks the exact
// flow that a fresh installation follows: empty database, no owner, no
// licence. The setup token must authorise the activation endpoint. Without
// this path the owner creation endpoint would be unreachable because it
// gates on the licence gate, and the activation endpoint would be
// unreachable because it gates on the owner. Together those gates would
// form an impassable loop.
func TestBootstrapTokenAuthorisesFirstTimeLicenceActivation(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bootstrap.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataRoot := t.TempDir()
	files, err := localfiles.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	licensingSvc, err := licensing.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(listener.Addr().String(), "installation-test",
		WithStoreHealth(database),
		WithProvisioning(provisioning.New(database.DB())),
		WithOwner(accounts, "test-bootstrap-token"),
		WithLicensing(licensingSvc),
		WithDB(database.DB()),
		WithFiles(files),
	)
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.Handler()}}
	ts.Start()
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/licence/activate", bytes.NewReader([]byte("{}")))
	req.Header.Set("X-Setup-Token", "test-bootstrap-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+listener.Addr().String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("activate with bootstrap: %d body=%s", resp.StatusCode, body)
	}

	// Without bootstrap token and no owner session: must be 403.
	req2, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/licence/activate", bytes.NewReader([]byte("{}")))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", "http://"+listener.Addr().String())
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Fatalf("activate without bootstrap or owner: %d, want 403", resp2.StatusCode)
	}
}

// TestBootstrapTokenStopsWorkingAfterOwnerCreated closes the second half
// of the licence/owner bootstrap loop: once an owner row exists the
// bootstrap token path is rejected so a leaked setup token cannot bypass
// the owner session gate on a live installation.
func TestBootstrapTokenStopsWorkingAfterOwnerCreated(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bootstrap2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataRoot := t.TempDir()
	files, err := localfiles.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(context.Background(), "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	licensingSvc, err := licensing.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(listener.Addr().String(), "installation-test",
		WithStoreHealth(database),
		WithProvisioning(provisioning.New(database.DB())),
		WithOwner(accounts, "test-bootstrap-token"),
		WithLicensing(licensingSvc),
		WithDB(database.DB()),
		WithFiles(files),
	)
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.Handler()}}
	ts.Start()
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/licence/activate", bytes.NewReader([]byte("{}")))
	req.Header.Set("X-Setup-Token", "test-bootstrap-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+listener.Addr().String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Once an owner exists the requireOwnerOrSetupToken path uses
	// ownerSession, which returns 401 on missing credentials. 403 is
	// also acceptable — both mean "rejected".
	if resp.StatusCode != 403 && resp.StatusCode != 401 {
		t.Fatalf("bootstrap token after owner: %d, want 401/403", resp.StatusCode)
	}
}

// TestPaymentProviderPutDoesNotDelete is the regression for the bug where
// the PUT handler fell into the DELETE branch. The handler must round-trip
// an update without removing the row, and the DELETE handler must remove a
// non-platform provider on a separate request.
func TestPaymentProviderPutDoesNotDelete(t *testing.T) {
	ts, _ := regressionFixture(t)

	login := loginOwner(t, ts)

	// PUT updates an existing provider. The platform provider is the
	// only one seeded; PUT must not delete it.
	listResp := mustRequest(t, ts.URL+"/api/v1/owner/payments/providers", login.Cookie, login.CSRF, "GET", nil)
	if listResp.StatusCode != 200 {
		t.Fatalf("list providers: %d body=%s", listResp.StatusCode, readBody(listResp))
	}
	var listing struct {
		Providers []struct {
			ID          string `json:"id"`
			Kind        string `json:"kind"`
			DisplayName string `json:"displayName"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	listResp.Body.Close()
	if len(listing.Providers) == 0 {
		t.Fatal("no platform provider seeded")
	}
	id := listing.Providers[0].ID
	original := len(listing.Providers)

	putBody, _ := json.Marshal(map[string]any{
		"kind":        listing.Providers[0].Kind,
		"displayName": "Renamed Gateway",
		"enabled":     true,
		"isDefault":   true,
		"config":      map[string]any{"key_id": "renamed"},
		"secret":      "x",
	})
	putReq := mustRequest(t, ts.URL+"/api/v1/owner/payments/providers/"+id, login.Cookie, login.CSRF, "PUT", bytes.NewReader(putBody))
	if putReq.StatusCode != 200 {
		t.Fatalf("put: %d body=%s", putReq.StatusCode, readBody(putReq))
	}
	putReq.Body.Close()

	// The provider must still exist (PUT did not delete it).
	listAfter := mustRequest(t, ts.URL+"/api/v1/owner/payments/providers", login.Cookie, login.CSRF, "GET", nil)
	if listAfter.StatusCode != 200 {
		t.Fatalf("list after put: %d body=%s", listAfter.StatusCode, readBody(listAfter))
	}
	var after struct {
		Providers []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(listAfter.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	listAfter.Body.Close()
	if len(after.Providers) != original {
		t.Fatalf("PUT changed provider count: %d → %d", original, len(after.Providers))
	}
	if after.Providers[0].DisplayName != "Renamed Gateway" {
		t.Fatalf("display name = %q, want Renamed Gateway", after.Providers[0].DisplayName)
	}

	// Create a fresh merchant-owned provider so we can test DELETE
	// without touching the platform row.
	createBody, _ := json.Marshal(map[string]any{
		"kind":        "manual",
		"displayName": "Merchant Cash",
		"enabled":     true,
		"isDefault":   false,
		"config":      map[string]any{"instructions": "Pay at counter"},
		"secret":      "",
	})
	createReq := mustRequest(t, ts.URL+"/api/v1/owner/payments/providers", login.Cookie, login.CSRF, "POST", bytes.NewReader(createBody))
	if createReq.StatusCode != 201 {
		t.Fatalf("create merchant provider: %d body=%s", createReq.StatusCode, readBody(createReq))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createReq.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	createReq.Body.Close()

	// DELETE actually deletes on a separate request.
	delReq := mustRequest(t, ts.URL+"/api/v1/owner/payments/providers/"+created.ID, login.Cookie, login.CSRF, "DELETE", nil)
	if delReq.StatusCode != 204 {
		t.Fatalf("delete: %d body=%s", delReq.StatusCode, readBody(delReq))
	}
	delReq.Body.Close()

	// Confirm the merchant row is gone while the platform row stays.
	listFinal := mustRequest(t, ts.URL+"/api/v1/owner/payments/providers", login.Cookie, login.CSRF, "GET", nil)
	if listFinal.StatusCode != 200 {
		t.Fatalf("list final: %d body=%s", listFinal.StatusCode, readBody(listFinal))
	}
	var finalListing struct {
		Providers []struct {
			ID string `json:"id"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(listFinal.Body).Decode(&finalListing); err != nil {
		t.Fatal(err)
	}
	listFinal.Body.Close()
	if len(finalListing.Providers) != original {
		t.Fatalf("DELETE did not remove merchant provider: %d remain, want %d", len(finalListing.Providers), original)
	}
}

// TestPortalRejectsUntrustedForwardedHeaders asserts the rule that the
// portal guard only trusts X-Forwarded-* headers when both a public origin
// is configured AND the request comes from a loopback peer. Without this
// the tunnel boundary is meaningless: a malicious caller reaching the
// loopback port directly could forge a peer address.
func TestPortalRejectsUntrustedForwardedHeaders(t *testing.T) {
	ts, _ := regressionFixture(t)

	body, _ := json.Marshal(map[string]any{"lines": []any{}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/quote", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "shop.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("untrusted forwarded: %d, want 403", resp.StatusCode)
	}
}

// TestConcurrentDuplicateOrderSubmissionCannotDuplicateLines exercises
// the duplicate-submission guard under concurrent load. Exactly one
// submission must win (201), every other must lose (409 or similar), and
// the line count must stay at 1.
func TestConcurrentDuplicateOrderSubmissionCannotDuplicateLines(t *testing.T) {
	ts, db := regressionFixture(t)
	seedCheckoutPrinter(t, db)

	body, contentType := uploadMultipartRegression(t, "a.pdf", "application/pdf", minimalPDFRegression())
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != 201 {
		t.Fatalf("upload: %d body=%s", upResp.StatusCode, readBody(upResp))
	}
	var uploaded struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.OrderID == "" {
		t.Fatal("upload: empty orderId")
	}

	lines := []map[string]any{{
		"documentId": uploaded.Files[0].DocumentID,
		"paperSize":  "A4", "colourMode": "monochrome", "sides": "one-sided",
		"copies": 1, "pageRangeStart": 1, "pageRangeEnd": 1,
	}}
	orderBody, _ := json.Marshal(map[string]any{
		"orderId":       uploaded.OrderID,
		"lines":         lines,
		"customerName":  "Race Tester",
		"customerPhone": "+919876543210",
		"customerEmail": "race@example.com",
	})

	const concurrency = 8
	var successes, conflicts int64
	var firstShare string
	var firstMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := http.Post(ts.URL+"/api/v1/portal/orders", "application/json", bytes.NewReader(orderBody))
			if err != nil {
				return
			}
			defer r.Body.Close()
			switch r.StatusCode {
			case 201:
				atomic.AddInt64(&successes, 1)
				var resp struct {
					ShareToken string `json:"shareToken"`
				}
				_ = json.NewDecoder(r.Body).Decode(&resp)
				firstMu.Lock()
				if firstShare == "" {
					firstShare = resp.ShareToken
				}
				firstMu.Unlock()
			case 409:
				atomic.AddInt64(&conflicts, 1)
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successes = %d, conflicts = %d, want 1 + %d", successes, conflicts, concurrency-1)
	}
	if conflicts != concurrency-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, concurrency-1)
	}

	var lineCount int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM order_lines WHERE order_id=?`, uploaded.OrderID).Scan(&lineCount); err != nil {
		t.Fatal(err)
	}
	if lineCount != 1 {
		t.Fatalf("order_lines rows = %d, want 1", lineCount)
	}

	var token string
	if err := db.DB().QueryRow(`SELECT share_token FROM orders WHERE id=?`, uploaded.OrderID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("share token lost after concurrent submissions")
	}
	if firstShare != "" && token != firstShare {
		t.Fatalf("share token changed: first winner %q, persisted %q", firstShare, token)
	}
}

// TestPortalOptionsReflectVerifiedPrinterCapabilities locks the rule that
// the customer-facing options endpoint hides every paper/colour/sides
// combination the printer fleet cannot honour. The fixture sets the price
// book for A4 monochrome + A3 colour but registers NO printers — the
// portal must therefore collapse to an empty projection rather than
// expose the full price book.
func TestPortalOptionsReflectVerifiedPrinterCapabilities(t *testing.T) {
	ts, _ := regressionFixture(t)

	resp, err := http.Get(ts.URL + "/api/v1/portal/options")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("options: %d", resp.StatusCode)
	}
	var payload struct {
		PaperSizes  []string `json:"paperSizes"`
		ColourModes []string `json:"colourModes"`
		SidesModes  []string `json:"sidesModes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PaperSizes) != 0 || len(payload.ColourModes) != 0 || len(payload.SidesModes) != 0 {
		t.Fatalf("portal exposed unsupported combinations: paperSizes=%v colourModes=%v sidesModes=%v",
			payload.PaperSizes, payload.ColourModes, payload.SidesModes)
	}
}

// helper types and functions used by the regression suite.

type sessionInfo struct {
	Cookie *http.Cookie
	CSRF   string
}

func loginOwner(t *testing.T, ts *httptest.Server) sessionInfo {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": "owner",
		"password": "a sufficiently long password",
	})
	req, err := http.NewRequest("POST", ts.URL+"/api/v1/owner/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// The login endpoint sits behind localOwnerRequest which enforces
	// same-origin; without an Origin header the request is 403.
	u, err := neturlParse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", u)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("login: %d body=%s", r.StatusCode, readBody(r))
	}
	var resp struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	cookies := r.Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie returned")
	}
	return sessionInfo{Cookie: cookies[0], CSRF: resp.CSRFToken}
}

func mustRequest(t *testing.T, url string, cookie *http.Cookie, csrf, method string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	// Owner endpoints sit behind localOwnerRequest which enforces
	// same-origin AND requires application/json on every non-GET/HEAD
	// request. The portal test fixture already sets Content-Type where
	// it matters; this helper covers the owner-side calls.
	if method != "GET" && method != "HEAD" {
		req.Header.Set("Content-Type", "application/json")
	}
	// Same-origin guard.
	if strings.HasPrefix(url, "http://127.0.0.1:") || strings.HasPrefix(url, "https://127.0.0.1:") {
		origin, _ := neturlParse(url)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// TestPortalTrustsLoopbackForwardedHeadersWhenPublicOriginConfigured is the
// regression for the bug where WithPublicOrigin existed but main.go never
// called it. After wiring, the portal guard must accept X-Forwarded-*
// headers from a loopback peer when the server has been told the public
// origin. The same-origin check then compares the customer's Origin header
// to https://<forwarded-host> rather than the loopback listener, so the
// tunnel boundary becomes meaningful.
func TestPortalTrustsLoopbackForwardedHeadersWhenPublicOriginConfigured(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tunnel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dataRoot := t.TempDir()
	files, err := localfiles.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, gate := range provisioning.OrderedGates {
		if err := provisioning.New(database.DB()).CompleteGate(ctx, gate, "test"); err != nil {
			t.Fatalf("complete %s: %v", gate, err)
		}
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, owner.Profile{
		Name: "Tunnel Shop", Address: "Pune", Phone: "+919999999999",
		Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
	}); err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	if _, err := pricingSvc.Save(ctx, pricing.Input{
		Entries: []pricing.Entry{
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesOneSided, UnitPriceMinor: 250},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ordersSvc := orders.New(database.DB(), pricingSvc)
	licensingSvc, err := licensing.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	paymentsSvc, err := payments.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(listener.Addr().String(), "installation-test",
		WithStoreHealth(database),
		WithProvisioning(provisioning.New(database.DB())),
		WithOwner(accounts, "test-bootstrap-token"),
		WithPricing(pricingSvc),
		WithOrders(ordersSvc),
		WithNotifications(notifications.New(database.DB(), nil)),
		WithPrinters(printers.New(database.DB())),
		WithLicensing(licensingSvc),
		WithPayments(paymentsSvc),
		WithDB(database.DB()),
		WithFiles(files),
		WithPublicOrigin("https://shop.example.com"),
	)
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.Handler()}}
	ts.Start()
	defer ts.Close()

	seedCheckoutPrinter(t, database)
	// Upload a PDF so we can hit /portal/quote with a real document id.
	body, contentType := uploadMultipartRegression(t, "a.pdf", "application/pdf", minimalPDFRegression())
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID    string `json:"documentID"`
			DocumentIDAlt string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	upResp.Body.Close()
	if uploaded.OrderID == "" {
		t.Fatal("upload: empty orderId")
	}
	docID := uploaded.Files[0].DocumentID
	if docID == "" {
		docID = uploaded.Files[0].DocumentIDAlt
	}

	// Now POST /portal/quote from the same loopback peer but with
	// X-Forwarded-* headers and an Origin matching the configured public
	// origin. Without WithPublicOrigin the guard rejects the forwarded
	// headers; with WithPublicOrigin the request must pass.
	quoteBody, _ := json.Marshal(map[string]any{
		"lines": []map[string]any{{
			"documentId": docID,
			"paperSize":  "A4", "colourMode": "monochrome", "sides": "one-sided",
			"copies": 1, "pageRangeStart": 1, "pageRangeEnd": 1,
		}},
	})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/quote", bytes.NewReader(quoteBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "shop.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://shop.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("trusted forwarded quote: %d body=%s", resp.StatusCode, readBody(resp))
	}

	// Sanity check: an Origin that does NOT match the public origin must
	// still be rejected, even when forwarded headers are present.
	req2, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/quote", bytes.NewReader(quoteBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Forwarded-Host", "shop.example.com")
	req2.Header.Set("X-Forwarded-Proto", "https")
	req2.Header.Set("Origin", "https://evil.example.com")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Fatalf("mismatched origin: %d, want 403", resp2.StatusCode)
	}
}

// TestOrderSubmissionUsesUpdateRowsAffected locks the fix for the bug
// where the duplicate-submission guard checked RowsAffected on a follow-up
// SELECT inside the same transaction. That pattern always returned 1 once
// the UPDATE had landed, so it could only ever report "already submitted"
// when the UPDATE itself failed. The fix reads RowsAffected directly off
// the UPDATE.
//
// To force the UPDATE's WHERE clause to match zero rows while the outer
// SELECT still observes a fresh order we install a SQLite BEFORE UPDATE
// trigger that calls RAISE(IGNORE). The handler's outer SELECT reads
// (status='pending_payment', submitted_at=0) so it passes the guard; the
// guarded UPDATE inside the transaction is silently abandoned by the
// trigger, so the fix must observe RowsAffected=0 and return 409. With the
// bug, the follow-up SELECT inside the transaction finds the row and the
// handler returns 500 instead.
func TestOrderSubmissionUsesUpdateRowsAffected(t *testing.T) {
	ts, db := regressionFixture(t)
	seedCheckoutPrinter(t, db)

	// Upload to mint a stub order id.
	body, contentType := uploadMultipartRegression(t, "a.pdf", "application/pdf", minimalPDFRegression())
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID    string `json:"documentID"`
			DocumentIDAlt string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	upResp.Body.Close()

	docID := uploaded.Files[0].DocumentID
	if docID == "" {
		docID = uploaded.Files[0].DocumentIDAlt
	}

	// Install a BEFORE UPDATE trigger that silently abandons the row for
	// the test order. RAISE(IGNORE) inside a BEFORE trigger causes the
	// UPDATE to match zero rows without raising an error, exactly the
	// race outcome the duplicate-submission guard must catch. SQLite
	// triggers cannot use host parameters, so the order id is inlined
	// directly; the id is a 32-character hex string produced by
	// crypto/rand so it is safe to interpolate.
	if _, err := db.DB().Exec(fmt.Sprintf(`
CREATE TRIGGER force_no_match BEFORE UPDATE ON orders
WHEN OLD.id = '%s'
BEGIN
	SELECT RAISE(IGNORE);
END`, uploaded.OrderID)); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	lines := []map[string]any{{
		"documentId": docID, "paperSize": "A4", "colourMode": "monochrome",
		"sides": "one-sided", "copies": 1, "pageRangeStart": 1, "pageRangeEnd": 1,
	}}
	orderBody, _ := json.Marshal(map[string]any{
		"orderId":       uploaded.OrderID,
		"lines":         lines,
		"customerName":  "Trigger Tester",
		"customerPhone": "+919876543210",
		"customerEmail": "trigger@example.com",
	})
	resp, err := http.Post(ts.URL+"/api/v1/portal/orders", "application/json", bytes.NewReader(orderBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("status = %d body=%s, want 409", resp.StatusCode, readBody(resp))
	}
}

func seedCheckoutPrinter(t *testing.T, db *store.Store) {
	t.Helper()
	svc := printers.New(db.DB())
	ctx := context.Background()
	p, err := svc.Register(ctx, printers.RegisterInput{Backend: printers.BackendMock, QueueName: "Checkout fixture", IsDefault: true, Capabilities: &printers.Snapshot{PaperSizes: []printers.PaperSize{{Key: "A4", RawLabel: "A4"}, {Key: "A3", RawLabel: "A3"}}, ColourModes: []string{"monochrome", "colour"}, SidesModes: []string{"one-sided", "two-sided-long-edge", "two-sided-short-edge"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, paper := range []string{"A4", "A3"} {
		if err = svc.RecordVerification(ctx, printers.Verification{PrinterID: p.ID, CapabilityType: "paper_size", CapabilityKey: paper, Status: printers.VerificationVerified}); err != nil {
			t.Fatal(err)
		}
	}
}
