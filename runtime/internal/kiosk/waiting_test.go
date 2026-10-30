package kiosk

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreparingReceiptTracksReadyReleaseWithoutAnotherOTP(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('o','t','paid','INR',2,100,'','',1,1)`); err != nil {
		t.Fatal(err)
	}
	p, err := pickup.New(s.db, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.pickup = p
	code, err := p.IssueVerified(ctx, "o", "verified")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, kioskRequest(`{"code":"`+code+`"}`))
	var reply map[string]string
	if err = json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 202 || reply["state"] != "preparing" || len(reply["receipt"]) != 64 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	poll := func(want string) {
		t.Helper()
		r := kioskRequest(`{"receipt":"` + reply["receipt"] + `"}`)
		r.URL.Path = "/api/v1/kiosk/progress"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"`+want+`"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for i := 0; i < 10; i++ {
		poll("preparing")
	}
	var attempts int
	if err = s.db.QueryRow(`SELECT attempts FROM kiosk_throttle WHERE scope='physical-kiosk'`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("polling used OTP attempt budget", attempts, err)
	}
	if err = p.MarkPrepared(ctx, "o", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if n, e := p.ReleaseWaiting(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	poll("processing")
	if n, e := p.ReleaseWaiting(ctx); e != nil || n != 0 {
		t.Fatal("duplicate release", n, e)
	}
}
