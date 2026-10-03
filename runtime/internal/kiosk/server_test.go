package kiosk

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClaimer struct{ calls atomic.Int32 }

func (c *testClaimer) Claim(context.Context, string) (string, error) {
	c.calls.Add(1)
	return "private-order", nil
}

var testKey = bytes.Repeat([]byte{7}, 32)

func fixture(t *testing.T) (*Server, *testClaimer) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "kiosk.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &testClaimer{}
	s, err := New(db.DB(), c, testKey, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}
func kioskRequest(body string) *http.Request {
	r := httptest.NewRequest("POST", "http://"+Address+"/api/v1/kiosk/claim", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:51234"
	r.Header.Set("Origin", "http://"+Address)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(testKey))
	return r
}

func TestKioskBoundaryRejectsUntrustedRequests(t *testing.T) {
	s, c := fixture(t)
	for _, name := range []string{"remote", "origin", "missing-origin", "host", "forwarded", "forwarded-empty", "cloudflare", "real-ip", "fetch-site", "credential", "duplicate-auth", "merchant-route", "query", "method"} {
		t.Run(name, func(t *testing.T) {
			r := kioskRequest(`{"code":"0007"}`)
			switch name {
			case "remote":
				r.RemoteAddr = "192.168.1.4:1234"
			case "origin":
				r.Header.Set("Origin", "https://shop.example")
			case "missing-origin":
				r.Header.Del("Origin")
			case "host":
				r.Host = "attacker.example:8081"
			case "forwarded":
				r.Header.Set("X-Forwarded-Host", Address)
			case "forwarded-empty":
				r.Header["Forwarded"] = []string{""}
			case "cloudflare":
				r.Header.Set("CF-Connecting-IP", "127.0.0.1")
			case "real-ip":
				r.Header.Set("X-Real-IP", "127.0.0.1")
			case "fetch-site":
				r.Header.Set("Sec-Fetch-Site", "same-site")
			case "credential":
				r.Header.Set("Authorization", "Bearer wrong")
			case "duplicate-auth":
				r.Header.Add("Authorization", r.Header.Get("Authorization"))
			case "merchant-route":
				r.URL.Path = "/api/v1/owner/orders"
			case "query":
				r.URL.RawQuery = "code=0007"
			case "method":
				r.Method = "GET"
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatal("boundary accepted", name)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cached response")
			}
		})
	}
	if c.calls.Load() != 0 {
		t.Fatal("untrusted request reached claim")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM kiosk_throttle").Scan(&count); err != nil || count != 0 {
		t.Fatal("unauthenticated traffic consumed quota", err)
	}
}

func TestKioskThrottlePersistsAndEscalates(t *testing.T) {
	s, c := fixture(t)
	now := time.Unix(10000, 0)
	s.now = func() time.Time { return now }
	send := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, kioskRequest(`{"code":"0007"}`))
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
		if strings.Contains(w.Body.String(), "0007") || strings.Contains(w.Body.String(), "private-order") {
			t.Fatal("private data disclosed")
		}
	}
	for i := 0; i < 5; i++ {
		send(202)
	}
	send(429)
	restarted, err := New(s.db, c, testKey, s.check)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = s.now
	s = restarted
	send(429)
	now = now.Add(30 * time.Second)
	for i := 0; i < 5; i++ {
		send(202)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, kioskRequest(`{"code":"0007"}`))
	if w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatal("cooldown did not escalate")
	}
	now = now.Add(-time.Hour)
	send(429)
}

func TestKioskGlobalLimitAndConcurrentReservations(t *testing.T) {
	s, _ := fixture(t)
	now := time.Unix(10000, 0)
	s.now = func() time.Time { return now }
	for round := 0; round < 6; round++ {
		for i := 0; i < 5; i++ {
			if wait, err := s.reserve(context.Background()); err != nil || wait != 0 {
				t.Fatal(wait, err)
			}
		}
		now = now.Add(30 * time.Second)
	}
	if wait, err := s.reserve(context.Background()); err != nil || wait != 60 {
		t.Fatal("global limit missing", wait, err)
	}
	now = now.Add(time.Hour)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait, err := s.reserve(context.Background())
			if err != nil {
				t.Error(err)
			}
			if wait == 0 && err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("concurrent reservations %d", accepted.Load())
	}
}

func TestKioskPreparingAndClaimAreSeparate(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('order','private','paid','INR',2,500,'Customer','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pickup.New(s.db, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.pickup = p
	code, err := p.IssueVerified(ctx, "order", "verified-test")
	if err != nil {
		t.Fatal(err)
	}
	send := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, kioskRequest(`{"code":"`+code+`"}`))
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
	}
	send(409)
	if err = p.MarkPrepared(ctx, "order", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	s.check = func(context.Context) error { return errors.New("inactive") }
	send(503)
	s.check = func(context.Context) error { return nil }
	send(202)
	send(400)
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM kiosk_releases").Scan(&count); err != nil || count != 1 {
		t.Fatal("release count", count, err)
	}
}

func TestMalformedRequestsConsumeBudgetAndStorageFailureBlocksClaims(t *testing.T) {
	s, c := fixture(t)
	for _, body := range []string{`{}`, `{"code":1234}`, `{"code":"0007","extra":true}`, `{"code":"0007"}{}`, strings.Repeat("x", 200)} {
		r := kioskRequest(body)
		// Empty input is well-formed JSON; exercise a missing content type too.
		if body == `{}` {
			r.Header.Del("Content-Type")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatal("malformed request accepted")
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, kioskRequest(`{"code":"0007"}`))
	if w.Code != 429 {
		t.Fatal("malformed requests bypassed quota")
	}
	if c.calls.Load() != 0 {
		t.Fatal("malformed request reached claim")
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, kioskRequest(`{"code":"0007"}`))
	if w.Code != 503 || c.calls.Load() != 0 {
		t.Fatal("database failure bypassed throttle")
	}
}
