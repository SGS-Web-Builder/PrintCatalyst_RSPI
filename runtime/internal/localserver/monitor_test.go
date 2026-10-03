package localserver_test

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMonitorIsDomainBoundReadOnlyAndSessionIsolated(t *testing.T) {
	ts, db, _ := portalFixtureWithData(t, localserver.WithPublicOrigin("https://print.example.com"))
	svc := tunnel.New(db.DB())
	if _, err := svc.SaveConfig(context.Background(), tunnel.Config{Provider: tunnel.ProviderCloudflared, PublicOrigin: "https://print.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("UPDATE tunnel_state SET status='online',last_verified_at=123"); err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path, body, origin string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Host = "print.example.com"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-For", "203.0.113.5")
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	check := func(res *http.Response, want int) {
		t.Helper()
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("status %d want %d: %s", res.StatusCode, want, data)
		}
	}
	check(call("GET", "/api/v1/monitor/orders", "", ""), 401)
	credentials := `{"username":"owner","password":"a sufficiently long password"}`
	check(call("POST", "/api/v1/monitor/login", credentials, "https://evil.example"), 403)
	res := call("POST", "/api/v1/monitor/login", credentials, "https://print.example.com")
	if res.StatusCode != 200 {
		check(res, 200)
		return
	}
	var login map[string]string
	if err := json.NewDecoder(res.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	csrf = login["csrfToken"]
	for _, c := range res.Cookies() {
		if c.Name == "__Secure-pc_monitor" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/v1/monitor/" {
		t.Fatal("unsafe monitor cookie")
	}
	check(call("GET", "/api/v1/monitor/orders", "", ""), 200)
	if _, err := db.DB().Exec("UPDATE monitor_sessions SET expires_at=0"); err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/v1/monitor/orders", "", ""), 401)
	if _, err := db.DB().Exec("UPDATE monitor_sessions SET expires_at=strftime('%s','now')+28800"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("UPDATE tunnel_state SET last_verified_at=0"); err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/v1/monitor/orders", "", ""), 403)
	if _, err := db.DB().Exec("UPDATE tunnel_state SET last_verified_at=123"); err != nil {
		t.Fatal(err)
	}
	check(call("POST", "/api/v1/monitor/orders", `{}`, "https://print.example.com"), 405)
	check(call("PUT", "/api/v1/owner/business", `{}`, "https://print.example.com"), 403)
	check(call("POST", "/api/v1/owner/orders/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/print", `{}`, "https://print.example.com"), 403)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/session", nil)
	req.AddCookie(&http.Cookie{Name: "pc_local_owner", Value: cookie.Value})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	check(res, 401)
	saved := csrf
	csrf = "wrong"
	check(call("POST", "/api/v1/monitor/logout", `{}`, "https://print.example.com"), 403)
	csrf = saved
	check(call("POST", "/api/v1/monitor/logout", `{}`, "https://print.example.com"), 200)
	check(call("GET", "/api/v1/monitor/orders", "", ""), 401)
	res, err = http.Get(ts.URL + "/api/v1/monitor/orders")
	if err != nil {
		t.Fatal(err)
	}
	check(res, 403)
}
