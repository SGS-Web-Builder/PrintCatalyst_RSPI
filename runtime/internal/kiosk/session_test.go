package kiosk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScreenSessionLifecycle(t *testing.T) {
	s, c := fixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	send := func(path, body string, bearer bool, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := kioskRequest(body)
		r.URL.Path = path
		if !bearer {
			r.Header.Del("Authorization")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	ticket := func() string {
		w := send("/api/v1/kiosk/pairing-ticket", "", true, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var v map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v["ticket"]
	}
	pair := func(code string) *httptest.ResponseRecorder {
		return send("/api/v1/kiosk/session", `{"ticket":"`+code+`"}`, false, nil)
	}
	if w := send("/api/v1/kiosk/pairing-ticket", "", false, nil); w.Code != 403 {
		t.Fatal("unauthorized ticket", w.Code)
	}
	first := ticket()
	second := ticket()
	if w := pair(first); w.Code != 403 {
		t.Fatal("replaced ticket accepted")
	}
	w := pair(second)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	old := cookies[0]
	restarted, err := New(s.db, c, testKey, s.check)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = s.now
	request := kioskRequest("")
	request.AddCookie(old)
	if !restarted.validSession(request) {
		t.Fatal("pairing lost on restart")
	}
	rotated, err := New(s.db, c, make([]byte, 32), s.check)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.validSession(request) {
		t.Fatal("rotated credential retained pairing")
	}
	if w := pair(second); w.Code != 403 {
		t.Fatal("ticket replay accepted")
	}
	if w := send("/api/v1/kiosk/claim", `{"code":"0007"}`, false, old); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	now = now.Add(time.Minute)
	w = pair(ticket())
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	current := w.Result().Cookies()[0]
	if w := send("/api/v1/kiosk/claim", `{"code":"0007"}`, false, old); w.Code != 403 {
		t.Fatal("old session accepted")
	}
	if w := send("/api/v1/kiosk/claim", `{"code":"0007"}`, false, current); w.Code != 202 {
		t.Fatal(w.Code)
	}
	now = now.Add(11 * 365 * 24 * time.Hour)
	if w := send("/api/v1/kiosk/claim", `{"code":"0007"}`, false, current); w.Code != 403 {
		t.Fatal("expired session accepted")
	}
	expired := ticket()
	now = now.Add(3 * time.Minute)
	if w := pair(expired); w.Code != 403 {
		t.Fatal("expired ticket accepted")
	}
	if c.calls.Load() != 2 {
		t.Fatal("unexpected claims", c.calls.Load())
	}
}

func TestScreenAssetsStayLocal(t *testing.T) {
	s, c := fixture(t)
	for _, path := range []string{"/", "/kiosk.js", "/kiosk.css"} {
		r := kioskRequest("")
		r.Method = "GET"
		r.URL.Path = path
		r.Header.Del("Origin")
		r.Header.Del("Authorization")
		r.Header.Set("Sec-Fetch-Site", "none")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal(path, w.Code)
		}
		r.Header.Set("CF-Connecting-IP", "127.0.0.1")
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("proxied asset accepted")
		}
	}
	if c.calls.Load() != 0 {
		t.Fatal("UI caused a claim")
	}
}
