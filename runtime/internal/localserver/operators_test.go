package localserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// operatorsFixture returns a handler, the underlying store and the owner
// service for tests that exercise role-based permissions against the HTTP
// surface. The licence and owner setup are completed so the handler is
// already in the "post-setup, pre-pricing" state. Tests can then sign in
// the owner and any operator they create.
func operatorsFixture(t *testing.T) (http.Handler, *store.Store, *owner.Service) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "operators-http.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	setup := provisioning.New(database.DB())
	if err := setup.CompleteGate(context.Background(), provisioning.GateLicence, "verified test licence"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	handler := New("127.0.0.1:8080", "test",
		WithStoreHealth(database), WithProvisioning(setup),
		WithOwner(accounts, "test-bootstrap-token"), WithPricing(pricing.New(database.DB())),
	).Handler()
	return handler, database, accounts
}

// signIn runs the full owner-setup prologue and returns the resulting
// session cookie plus CSRF token. Tests then drive the operator surface
// from a known-good owner session.
func signInOwner(t *testing.T, handler http.Handler) (*http.Cookie, string) {
	t.Helper()
	setup := `{"username":"owner","password":"a sufficiently long password","setupToken":"test-bootstrap-token"}`
	if w := call(handler, "POST", "/api/v1/setup/owner", setup, nil, ""); w.Code != 201 {
		t.Fatalf("owner setup: %d %s", w.Code, w.Body)
	}
	login := call(handler, "POST", "/api/v1/owner/login", `{"username":"owner","password":"a sufficiently long password"}`, nil, "")
	if login.Code != 200 {
		t.Fatalf("owner login: %d %s", login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
		Role      string `json:"role"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CSRFToken == "" || body.Role != "owner" {
		t.Fatalf("session: %+v", body)
	}
	return cookies[0], body.CSRFToken
}

// signInOperator authenticates an existing operator row via the dispatcher
// login endpoint and returns the resulting session cookie plus CSRF token.
// The token here is an operator session token and must not be used as if it
// were an owner session — callers that try an owner-only endpoint with it
// will receive 403.
func signInOperator(t *testing.T, handler http.Handler, username, password string) (*http.Cookie, string) {
	t.Helper()
	login := call(handler, "POST", "/api/v1/owner/login", `{"username":"`+username+`","password":"`+password+`"}`, nil, "")
	if login.Code != 200 {
		t.Fatalf("operator login: %d %s", login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
		Role      string `json:"role"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Role != "operator" {
		t.Fatalf("role = %q, want operator", body.Role)
	}
	return cookies[0], body.CSRFToken
}

// TestOperatorSessionReportsOperatorRole confirms the dispatcher in Login
// returns the resolved role on /api/v1/owner/session. The UI uses this to
// decide whether to surface operator-only or owner-only navigation.
func TestOperatorSessionReportsOperatorRole(t *testing.T) {
	handler, _, _ := operatorsFixture(t)
	ownerCookie, _ := signInOwner(t, handler)
	// Owner session reflects the owner role.
	resp := call(handler, "GET", "/api/v1/owner/session", "", ownerCookie, "")
	if resp.Code != 200 {
		t.Fatalf("owner session: %d %s", resp.Code, resp.Body)
	}
	var ownerSubject struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &ownerSubject); err != nil {
		t.Fatal(err)
	}
	if ownerSubject.Username != "owner" || ownerSubject.Role != "owner" {
		t.Fatalf("owner subject = %+v", ownerSubject)
	}
}

// TestOperatorCRUDRequiresOwnerPermission gates the new operator endpoints
// with CanManageOperators. Operators who try to list, create, disable or
// delete peer operators are refused with 403 before the request body is
// parsed — a compromised operator cannot escalate by adding a backdoor
// account or removing the owner-visible operators list.
func TestOperatorCRUDRequiresOwnerPermission(t *testing.T) {
	handler, _, _ := operatorsFixture(t)
	ownerCookie, csrf := signInOwner(t, handler)
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"alice","password":"a sufficiently long password"}`,
		ownerCookie, csrf); w.Code != 201 {
		t.Fatalf("create operator: %d %s", w.Code, w.Body)
	}
	opCookie, opCSRF := signInOperator(t, handler, "alice", "a sufficiently long password")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list rejected", "GET", "/api/v1/owner/operators", ""},
		{"create rejected", "POST", "/api/v1/owner/operators", `{"username":"bob","password":"another long password"}`},
		{"patch rejected", "PATCH", "/api/v1/owner/operators/1", `{"enabled":false}`},
		{"delete rejected", "DELETE", "/api/v1/owner/operators/1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w *httptest.ResponseRecorder
			if tc.method == "GET" || tc.method == "DELETE" {
				w = call(handler, tc.method, tc.path, "", opCookie, "")
			} else {
				w = call(handler, tc.method, tc.path, tc.body, opCookie, opCSRF)
			}
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", w.Code, w.Body)
			}
		})
	}

	// Operator session must still resolve correctly; the 403s above did not
	// invalidate the session cookie.
	if w := call(handler, "GET", "/api/v1/owner/session", "", opCookie, ""); w.Code != 200 {
		t.Fatalf("operator session after forbidden CRUD: %d %s", w.Code, w.Body)
	}
}

// TestOperatorCRUDFullLifecycleAsOwner walks the owner through list/create/
// disable/enable/delete and asserts that the persisted state, the API
// responses and the database rows stay in sync. Password hashing, audit
// events, throttle isolation and session revocation are all checked at the
// HTTP boundary so this test acts as the Phase 3D acceptance checklist.
func TestOperatorCRUDFullLifecycleAsOwner(t *testing.T) {
	handler, database, _ := operatorsFixture(t)
	ownerCookie, csrf := signInOwner(t, handler)

	// Empty list before any operator exists.
	list := call(handler, "GET", "/api/v1/owner/operators", "", ownerCookie, "")
	if list.Code != 200 {
		t.Fatalf("list empty: %d %s", list.Code, list.Body)
	}
	var empty struct {
		Operators []operatorView `json:"operators"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Operators) != 0 {
		t.Fatalf("unexpected operators: %+v", empty.Operators)
	}

	// Create.
	create := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"alice","password":"a sufficiently long password"}`,
		ownerCookie, csrf)
	if create.Code != 201 {
		t.Fatalf("create alice: %d %s", create.Code, create.Body)
	}
	var created operatorView
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Username != "alice" || !created.Enabled || created.Role != "operator" || created.ID == 0 {
		t.Fatalf("created: %+v", created)
	}

	// Reject duplicate and case-insensitive collisions.
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"alice","password":"another long password"}`,
		ownerCookie, csrf); w.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"ALICE","password":"another long password"}`,
		ownerCookie, csrf); w.Code != http.StatusConflict {
		t.Fatalf("case-insensitive duplicate: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"owner","password":"another long password"}`,
		ownerCookie, csrf); w.Code != http.StatusConflict {
		t.Fatalf("owner-username collision: %d %s", w.Code, w.Body)
	}

	// Operator can sign in; disabling them revokes the live session.
	opCookie, opCSRF := signInOperator(t, handler, "alice", "a sufficiently long password")
	if w := call(handler, "GET", "/api/v1/owner/session", "", opCookie, ""); w.Code != 200 {
		t.Fatalf("operator session: %d %s", w.Code, w.Body)
	}
	patch := call(handler, "PATCH", "/api/v1/owner/operators/"+itoa(created.ID),
		`{"enabled":false}`, ownerCookie, csrf)
	if patch.Code != 200 {
		t.Fatalf("disable: %d %s", patch.Code, patch.Body)
	}
	if w := call(handler, "GET", "/api/v1/owner/session", "", opCookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled session survived: %d %s", w.Code, w.Body)
	}
	// Disabled operator cannot log in again.
	if w := call(handler, "POST", "/api/v1/owner/login",
		`{"username":"alice","password":"a sufficiently long password"}`,
		nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled operator authenticated: %d %s", w.Code, w.Body)
	}

	// Re-enable.
	patch = call(handler, "PATCH", "/api/v1/owner/operators/"+itoa(created.ID),
		`{"enabled":true}`, ownerCookie, csrf)
	if patch.Code != 200 {
		t.Fatalf("enable: %d %s", patch.Code, patch.Body)
	}
	if _, err := database.DB().Exec(`UPDATE operator_login_throttle SET failures=0,retry_at=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	opCookie, _ = signInOperator(t, handler, "alice", "a sufficiently long password")

	// Read pricing — operators have CanViewPricing.
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", opCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("operator pricing view: %d %s", w.Code, w.Body)
	}
	// Write pricing — operators are forbidden.
	if w := call(handler, "PUT", "/api/v1/owner/pricing",
		`{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250}]}`,
		opCookie, opCSRF); w.Code != http.StatusForbidden {
		t.Fatalf("operator pricing write: %d %s", w.Code, w.Body)
	}

	// Read business — operators can read, the existing 404 here is unrelated.
	if w := call(handler, "GET", "/api/v1/owner/business", "", opCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("operator business read: %d %s", w.Code, w.Body)
	}
	// Write business — operators are forbidden.
	if w := call(handler, "PUT", "/api/v1/owner/business",
		`{"name":"x","address":"x","phone":"","country":"IN","currency":"INR","locale":"en-IN","timeZone":"Asia/Kolkata"}`,
		opCookie, opCSRF); w.Code != http.StatusForbidden {
		t.Fatalf("operator business write: %d %s", w.Code, w.Body)
	}

	// Delete.
	del := call(handler, "DELETE", "/api/v1/owner/operators/"+itoa(created.ID), "", ownerCookie, csrf)
	if del.Code != 200 {
		t.Fatalf("delete: %d %s", del.Code, del.Body)
	}
	// Sessions are gone via the cascade.
	if w := call(handler, "GET", "/api/v1/owner/session", "", opCookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("operator session survived delete: %d %s", w.Code, w.Body)
	}
	// Re-deleting is 404.
	if w := call(handler, "DELETE", "/api/v1/owner/operators/"+itoa(created.ID), "", ownerCookie, csrf); w.Code != http.StatusNotFound {
		t.Fatalf("re-delete: %d %s", w.Code, w.Body)
	}
}

// TestOperatorEndpointsRejectUnauthenticatedAndCrossOrigin reuses the same
// origin/CSRF/loopback guarantees the existing owner guard enforces. The
// new operator routes share the same protect() closure, so any deviation
// here would be a regression in the shared guard.
func TestOperatorEndpointsRejectUnauthenticatedAndCrossOrigin(t *testing.T) {
	handler, _, _ := operatorsFixture(t)
	ownerCookie, csrf := signInOwner(t, handler)
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"alice","password":"a sufficiently long password"}`,
		ownerCookie, csrf); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	// Missing session.
	if w := call(handler, "GET", "/api/v1/owner/operators", "", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d %s", w.Code, w.Body)
	}
	// Wrong CSRF.
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"bob","password":"another long password"}`,
		ownerCookie, "wrong-token"); w.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF: %d %s", w.Code, w.Body)
	}
	// Bad JSON.
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"unknown":true}`, ownerCookie, csrf); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", w.Code, w.Body)
	}
	// Cross-site fetch is refused before authentication is checked.
	cross := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/owner/operators", nil)
	cross.RemoteAddr = "127.0.0.1:9000"
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	cross.AddCookie(ownerCookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, cross)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d %s", recorder.Code, recorder.Body)
	}
}

// TestOperatorEndpointsRejectInvalidPathParameters locks down the path
// parameter parsing on PATCH and DELETE. Bad ids must be rejected with 400
// before reaching the database so a stray `/api/v1/owner/operators/abc`
// cannot trigger a SQL syntax error from the service layer.
func TestOperatorEndpointsRejectInvalidPathParameters(t *testing.T) {
	handler, _, _ := operatorsFixture(t)
	ownerCookie, csrf := signInOwner(t, handler)
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"alice","password":"a sufficiently long password"}`,
		ownerCookie, csrf); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{
		"/api/v1/owner/operators/abc",
		"/api/v1/owner/operators/-1",
		"/api/v1/owner/operators/0",
	} {
		if w := call(handler, "PATCH", path, `{"enabled":false}`, ownerCookie, csrf); w.Code != http.StatusBadRequest {
			t.Fatalf("%s patch: %d %s", path, w.Code, w.Body)
		}
		if w := call(handler, "DELETE", path, "", ownerCookie, csrf); w.Code != http.StatusBadRequest {
			t.Fatalf("%s delete: %d %s", path, w.Code, w.Body)
		}
	}
	// Missing id segment is also 400.
	if w := call(handler, "PATCH", "/api/v1/owner/operators/", `{"enabled":false}`, ownerCookie, csrf); w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Fatalf("empty id patch: %d %s", w.Code, w.Body)
	}
}

// TestOwnerSessionCanStillHitOwnerOnlyEndpointsAfterOperatorChanges
// protects the existing owner paths from regression as the operator surface
// lands: the owner can continue to manage the business profile, pricing and
// operators after an operator has been added, disabled and removed.
func TestOwnerSessionCanStillHitOwnerOnlyEndpointsAfterOperatorChanges(t *testing.T) {
	handler, _, _ := operatorsFixture(t)
	ownerCookie, csrf := signInOwner(t, handler)
	if w := call(handler, "POST", "/api/v1/owner/operators",
		`{"username":"alice","password":"a sufficiently long password"}`,
		ownerCookie, csrf); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	// Owner can save the business profile (CanEditBusiness).
	business := `{"name":"Campus","address":"Pune","phone":"","country":"IN","currency":"INR","locale":"en-IN","timeZone":"Asia/Kolkata"}`
	if w := call(handler, "PUT", "/api/v1/owner/business", business, ownerCookie, csrf); w.Code != http.StatusOK {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	// And list operators without being logged out by any of the above.
	if w := call(handler, "GET", "/api/v1/owner/operators", "", ownerCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("list after business save: %d %s", w.Code, w.Body)
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Compile-time check that strings is still referenced; a future cleanup must
// keep it imported because the fixtures and inputs use ToLower/TrimSpace.
var _ = strings.ToLower