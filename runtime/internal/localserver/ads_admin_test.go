package localserver_test

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteAdManagementIsolationAndUpload(t *testing.T) {
	ts, db, root := portalFixtureWithData(t, localserver.WithPublicOrigin("https://print.example.com"))
	svc := tunnel.New(db.DB())
	if _, e := svc.SaveConfig(context.Background(), tunnel.Config{Provider: tunnel.ProviderCloudflared, PublicOrigin: "https://print.example.com"}); e != nil {
		t.Fatal(e)
	}
	db.DB().Exec("UPDATE tunnel_state SET status='online',last_verified_at=123")
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path, body, kind, origin string) (int, []byte, []*http.Cookie) {
		r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		r.Host = "print.example.com"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "203.0.113.5")
		r.Header.Set("Content-Type", kind)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, data, res.Cookies()
	}
	origin := "https://print.example.com"
	base := "/api/v1/ads-admin/"
	check := func(got, want int, data []byte) {
		t.Helper()
		if got != want {
			t.Fatalf("%d want %d: %s", got, want, data)
		}
	}
	code, data, _ := call("GET", base+"campaigns", "", "application/json", origin)
	check(code, 401, data)
	creds := `{"username":"owner","password":"a sufficiently long password"}`
	code, data, _ = call("POST", base+"login", creds, "application/json", "https://evil.example")
	check(code, 403, data)
	code, data, cookies := call("POST", base+"login", creds, "application/json", origin)
	check(code, 200, data)
	var login map[string]string
	json.Unmarshal(data, &login)
	csrf = login["csrfToken"]
	for _, c := range cookies {
		if c.Name == "pc_ad_admin" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/api/v1/ads-admin/" {
		t.Fatal("cookie")
	}
	saved := csrf
	csrf = "wrong"
	code, data, _ = call("POST", base+"media", "invalid", "image/png", origin)
	check(code, 403, data)
	csrf = saved
	code, data, _ = call("POST", base+"media", "not an image", "image/png", origin)
	check(code, 415, data)
	// Small valid PNG header, recognized before the byte stream is committed.
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)
	code, data, _ = call("POST", base+"media", png, "image/png", origin)
	check(code, 201, data)
	var uploaded map[string]string
	json.Unmarshal(data, &uploaded)
	id := uploaded["id"]
	if _, e := os.Stat(filepath.Join(root, "ads", id+".png")); e != nil {
		t.Fatal(e)
	}
	settings := `{"name":"Default poster","schedule":{"enabled":true,"default":true,"start":"2026-10-19T14:00","zone":"Asia/Kolkata","duration":1,"unit":"months","repeat":"none","slotMinutes":60,"imageSeconds":10}}`
	code, data, _ = call("PUT", base+"campaigns/"+id, settings, "application/json", origin)
	check(code, 200, data)
	code, data, _ = call("GET", base+"campaigns", "", "application/json", origin)
	check(code, 200, data)
	if !strings.Contains(string(data), "Default poster") {
		t.Fatal(string(data))
	}
	code, data, _ = call("POST", "/api/v1/monitor/orders", "{}", "application/json", origin)
	check(code, 405, data)
	code, data, _ = call("PUT", "/api/v1/owner/business", "{}", "application/json", origin)
	check(code, 403, data)
	code, data, _ = call("POST", base+"logout", "{}", "application/json", origin)
	check(code, 200, data)
	code, data, _ = call("GET", base+"campaigns", "", "application/json", origin)
	check(code, 401, data)
}
