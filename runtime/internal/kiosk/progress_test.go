package kiosk

import (
	"context"
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

func TestInvoiceReviewRequiresAssistance(t *testing.T) {
	s, _ := fixture(t)
	_, err := s.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('private-order','secret','paid','INR',2,500,'Customer','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,progress,updated_at) VALUES('private-order','test','A4','',5,'submitted','review',1)`)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, kioskRequest(`{"code":"0007"}`))
	var receipt map[string]string
	json.Unmarshal(w.Body.Bytes(), &receipt)
	r := kioskRequest(`{"receipt":"` + receipt["receipt"] + `"}`)
	r.URL.Path = "/api/v1/kiosk/progress"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"assistance"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAdministratorPairPageIsLocalAndSeparate(t *testing.T) {
	s, _ := fixture(t)
	for _, path := range []string{"/admin/pair/", "/pair.js"} {
		r := httptest.NewRequest("GET", "http://"+Address+path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		r.RemoteAddr = "192.168.1.5:1234"
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("remote setup allowed")
		}
	}
}

func TestFirstJobReviewDoesNotEndRemainingDocuments(t *testing.T) {
	s, _ := fixture(t)
	must := func(q string) {
		t.Helper()
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('batch','secret','pending_payment','INR',2,500,'','',1,1)`)
	must(`INSERT INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,progress,updated_at) VALUES('batch','first','A4','',1,'submitted','review',1),('batch','second','A4','',1,'submitted','printing',1)`)
	state, err := s.progressState(context.Background(), "batch")
	if err != nil || state == "assistance" || state == "done" {
		t.Fatal(state, err)
	}
	must(`UPDATE separator_invoices SET progress='review' WHERE queue_name='second'`)
	state, err = s.progressState(context.Background(), "batch")
	if err != nil || state != "assistance" {
		t.Fatal(state, err)
	}
}
