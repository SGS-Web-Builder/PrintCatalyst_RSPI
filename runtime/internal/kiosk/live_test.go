package kiosk

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveProgressStreamPrivacyAndSettings(t *testing.T) {
	s, _ := fixture(t)
	_, e := s.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('private-order','secret','paid','INR',2,500,'Private customer','123',1,1)`)
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, kioskRequest(`{"code":"0007"}`))
	var accepted map[string]string
	json.Unmarshal(w.Body.Bytes(), &accepted)
	r := kioskRequest(`{"receipt":"` + accepted["receipt"] + `"}`)
	r.URL.Path = "/api/v1/kiosk/events"
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r = r.WithContext(ctx)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(w.Body.String(), `"countdownEnabled":true`) || strings.Contains(w.Body.String(), "Private customer") {
		t.Fatal(w.Code, w.Body.String())
	}
	r = kioskRequest(`{"receipt":"` + accepted["receipt"] + `"}`)
	r.URL.Path = "/api/v1/kiosk/events"
	r.Header.Del("Authorization")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
