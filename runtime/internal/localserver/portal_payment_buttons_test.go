package localserver_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPortalPaymentButtonSettings(t *testing.T) {
	ts, db := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	path := "/api/v1/owner/portal-payment-buttons"
	for _, tc := range []struct {
		body         string
		status       int
		cash, online bool
	}{
		{`{"cashEnabled":false,"onlineEnabled":true}`, 200, false, true},
		{`{"cashEnabled":false,"onlineEnabled":false}`, 400, false, true},
		{`{"cashEnabled":true,"onlineEnabled":false}`, 200, true, false},
	} {
		resp := putJSON(t, ts.URL, path, cookie, csrf, []byte(tc.body))
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("save = %d, want %d", resp.StatusCode, tc.status)
		}
		var cash, online bool
		if err := db.DB().QueryRow("SELECT cash_enabled,online_enabled FROM portal_payment_buttons").Scan(&cash, &online); err != nil {
			t.Fatal(err)
		}
		if cash != tc.cash || online != tc.online {
			t.Fatal("incorrect persisted options")
		}
		resp, err := http.Get(ts.URL + "/api/v1/portal/options")
		if err != nil {
			t.Fatal(err)
		}
		var options struct {
			Cash   bool `json:"cashOnCounterReady"`
			Online bool `json:"razorpayReady"`
		}
		err = json.NewDecoder(resp.Body).Decode(&options)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if options.Cash != tc.cash || (!tc.online && options.Online) {
			t.Fatal("portal ignored settings")
		}
	}
}
