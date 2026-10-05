package kiosk

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProgressReceiptIsPrivateAndExpires(t *testing.T) {
	s, _ := fixture(t)
	_, err := s.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('private-order','secret','paid','INR',2,500,'Private customer','123',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	r := kioskRequest(`{"code":"0007"}`)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var v map[string]string
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 202 || len(v["receipt"]) != 64 {
		t.Fatal(w.Code, w.Body.String())
	}
	send := func(token string, auth bool) *httptest.ResponseRecorder {
		r := kioskRequest(`{"receipt":"` + token + `"}`)
		r.URL.Path = "/api/v1/kiosk/progress"
		if !auth {
			r.Header.Del("Authorization")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	w = send(v["receipt"], true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "customer") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = send(v["receipt"], false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = send(strings.Repeat("a", 64), true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	future := time.Now().Add(25 * time.Hour)
	s.now = func() time.Time { return future }
	if w = send(v["receipt"], true); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
