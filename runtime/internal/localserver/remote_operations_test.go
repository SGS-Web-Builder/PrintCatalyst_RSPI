package localserver_test

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRemoteOperationsMaintenanceAndPriceConflict(t *testing.T) {
	ts, db, _ := portalFixtureWithData(t, localserver.WithPublicOrigin("https://print.example.com"))
	svc := tunnel.New(db.DB())
	if _, e := svc.SaveConfig(context.Background(), tunnel.Config{Provider: tunnel.ProviderCloudflared, PublicOrigin: "https://print.example.com"}); e != nil {
		t.Fatal(e)
	}
	db.DB().Exec("UPDATE tunnel_state SET status='online',last_verified_at=123")
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path, body string) (int, []byte) {
		r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		r.Host = "print.example.com"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "203.0.113.5")
		r.Header.Set("Origin", "https://print.example.com")
		r.Header.Set("Content-Type", "application/json")
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
		if strings.HasSuffix(path, "/login") && res.StatusCode == 200 {
			cookie = res.Cookies()[0]
			var login map[string]string
			json.Unmarshal(data, &login)
			csrf = login["csrfToken"]
		}
		return res.StatusCode, data
	}
	check := func(code, want int, body []byte) {
		t.Helper()
		if code != want {
			t.Fatalf("got %d want %d: %s", code, want, body)
		}
	}
	code, body := call("GET", "/api/v1/remote/status", "")
	check(code, 401, body)
	code, body = call("POST", "/api/v1/remote/login", `{"username":"owner","password":"a sufficiently long password"}`)
	check(code, 200, body)

	code, body = call("GET", "/api/v1/remote/status", "")
	check(code, 200, body)
	if !strings.Contains(string(body), "availableBytes") {
		t.Fatal("missing storage telemetry", string(body))
	}
	code, body = call("POST", "/api/v1/remote/support/confirm-output/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", `{"confirm":false}`)
	check(code, 400, body)
	code, body = call("GET", "/api/v1/remote/kiosk-flow", "")
	check(code, 200, body)
	if !strings.Contains(string(body), `"otpEnabled":true`) {
		t.Fatal(string(body))
	}
	code, body = call("PUT", "/api/v1/remote/kiosk-flow", `{}`)
	check(code, 400, body)
	code, body = call("PUT", "/api/v1/remote/kiosk-flow", `{"otpEnabled":false,"countdownEnabled":false}`)
	check(code, 200, body)
	var otp, countdown bool
	if e := db.DB().QueryRow("SELECT otp_enabled,countdown_enabled FROM kiosk_control").Scan(&otp, &countdown); e != nil || otp || countdown {
		t.Fatal("flow setting did not persist", e)
	}
	code, body = call("PUT", "/api/v1/remote/kiosk-flow", `{"countdownEnabled":true}`)
	check(code, 200, body)
	if !strings.Contains(string(body), `"otpEnabled":false`) {
		t.Fatal("countdown edit changed OTP policy", string(body))
	}
	code, body = call("PUT", "/api/v1/remote/kiosk-flow", `{"otpEnabled":true,"countdownEnabled":true}`)
	check(code, 200, body)

	code, body = call("PUT", "/api/v1/remote/maintenance", `{"paused":true,"reason":"Paper refill"}`)
	check(code, 200, body)
	code, body = call("GET", "/api/v1/portal/options", "")
	check(code, 503, body)
	code, body = call("GET", "/api/v1/remote/orders", "")
	check(code, 200, body)
	code, body = call("PUT", "/api/v1/remote/maintenance", `{"paused":false,"reason":""}`)
	check(code, 200, body)
	code, body = call("GET", "/api/v1/portal/options", "")
	check(code, 200, body)
	code, body = call("PUT", "/api/v1/remote/pricing/grid/all", `{"expectedRevision":-1,"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":301}]}`)
	check(code, 409, body)
	code, body = call("POST", "/api/v1/remote/schedules", `{"scope":"all","dueAt":1800000000,"input":{"expectedRevision":0,"entries":[]}}`)
	check(code, 200, body)
	var scheduled map[string]any
	json.Unmarshal(body, &scheduled)
	code, body = call("DELETE", "/api/v1/remote/schedules/"+scheduled["id"].(string), "")
	check(code, 200, body)
	code, body = call("DELETE", "/api/v1/remote/schedules/"+scheduled["id"].(string), "")
	check(code, 409, body)
	code, body = call("GET", "/api/v1/remote/ads/campaigns", "")
	check(code, 200, body)
	code, body = call("GET", "/api/v1/remote/revenue", "")
	check(code, 200, body)
	code, body = call("GET", "/api/v1/remote/audit", "")
	check(code, 200, body)
	if !strings.Contains(string(body), "Paper refill") {
		t.Fatal("maintenance audit missing")
	}
	var revision int64
	if e := db.DB().QueryRow("SELECT revision FROM price_revision").Scan(&revision); e != nil {
		t.Fatal(e)
	}
	input := pricing.Input{ExpectedRevision: &revision, Entries: []pricing.Entry{{PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", UnitPriceMinor: 333}}}
	raw, _ := json.Marshal(input)
	for _, id := range []string{"first", "second"} {
		if _, e := db.DB().Exec("INSERT INTO price_schedules(id,scope,input_json,due_at,created_at) VALUES(?,'',?,1,1)", id, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	runner := localserver.New("127.0.0.1:0", "test", localserver.WithDB(db.DB()), localserver.WithPricing(pricing.New(db.DB())))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	runner.RunRemote(ctx)
	var first, second string
	db.DB().QueryRow("SELECT state FROM price_schedules WHERE id='first'").Scan(&first)
	db.DB().QueryRow("SELECT state FROM price_schedules WHERE id='second'").Scan(&second)
	if first != "applied" || second != "review" {
		t.Fatal("scheduled conflict handling", first, second)
	}
	book, e := pricing.New(db.DB()).GridBook(context.Background(), "")
	if e != nil || book.Entries[0].UnitPriceMinor != 333 {
		t.Fatal(book, e)
	}

}
