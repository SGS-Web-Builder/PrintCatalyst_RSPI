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
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func ownerHandler(t *testing.T) (http.Handler, *owner.Service) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	setup := provisioning.New(db.DB())
	if err := setup.CompleteGate(context.Background(), provisioning.GateLicence, "verified test licence"); err != nil {
		t.Fatal(err)
	}
	service := owner.New(db.DB())
	return New("127.0.0.1:8080", "test", WithStoreHealth(db), WithProvisioning(setup), WithOwner(service, "test-bootstrap-token")).Handler(), service
}
func call(h http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestOwnerHTTPFlowProtectsSettingsAndKeepsOrderIntakeClosed(t *testing.T) {
	h, _ := ownerHandler(t)
	setup := `{"username":"owner","password":"a sufficiently long password","setupToken":"test-bootstrap-token"}`
	if w := call(h, "POST", "/api/v1/setup/owner", setup, nil, ""); w.Code != 201 {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/v1/setup/owner", setup, nil, ""); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	if w := call(h, "GET", "/api/v1/setup/status", "", nil, ""); w.Code != 401 {
		t.Fatalf("setup status must now require owner: %d", w.Code)
	}
	if w := call(h, "GET", "/api/v1/owner/business", "", nil, ""); w.Code != 401 {
		t.Fatalf("unauthenticated read: %d", w.Code)
	}
	login := call(h, "POST", "/api/v1/owner/login", `{"username":"owner","password":"a sufficiently long password"}`, nil, "")
	if login.Code != 200 {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "" {
		t.Fatal("unsafe cookie")
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.CSRFToken == "" {
		t.Fatal("missing CSRF token")
	}
	profile := `{"name":"Campus","address":"Pune","phone":"","country":"IN","currency":"INR","locale":"en-IN","timeZone":"Asia/Kolkata"}`
	if w := call(h, "PUT", "/api/v1/owner/business", profile, cookie, ""); w.Code != 403 {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	if w := call(h, "PUT", "/api/v1/owner/business", profile, cookie, body.CSRFToken); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := call(h, "GET", "/api/v1/owner/business", "", cookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Campus") {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/v1/orders", `{}`, cookie, body.CSRFToken); w.Code != 503 {
		t.Fatalf("order gate bypassed: %d", w.Code)
	}
	if w := call(h, "POST", "/api/v1/owner/logout", `{}`, cookie, body.CSRFToken); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := call(h, "GET", "/api/v1/owner/session", "", cookie, ""); w.Code != 401 {
		t.Fatalf("logout did not revoke: %d", w.Code)
	}
}
func TestOwnerHTTPRejectsPublicAndCrossOriginRequests(t *testing.T) {
	h, _ := ownerHandler(t)
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.Host = "attacker.example" },
		func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1234" },
		func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.9") },
		func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		func(r *http.Request) { r.Header.Del("Origin") },
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/v1/setup/owner", strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:9000"
		r.Header.Set("Origin", "http://127.0.0.1:8080")
		r.Header.Set("Content-Type", "application/json")
		change(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 && w.Code != 415 {
			t.Fatalf("unsafe request not denied: %d", w.Code)
		}
	}
	if w := call(h, "POST", "/api/v1/setup/owner", `{"username":"owner","password":"a sufficiently long password","setupToken":"wrong"}`, nil, ""); w.Code != 403 {
		t.Fatalf("bad bootstrap accepted: %d", w.Code)
	}
	if w := call(h, "POST", "/api/v1/setup/owner", `{"unknown":true}`, nil, ""); w.Code != 400 {
		t.Fatalf("unknown JSON fields accepted: %d", w.Code)
	}
	if w := call(h, "POST", "/api/v1/setup/owner", strings.Repeat("x", 20000), nil, ""); w.Code != 400 {
		t.Fatalf("oversize: %d", w.Code)
	}
}
