package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestPrinterInvoiceSettings(t *testing.T) {
	ts, store := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	db := store.DB()
	for _, q := range []string{
		`INSERT INTO printers(id,backend,queue_name,created_at,last_seen_at) VALUES('invoice-printer','windows','Invoices',1,1)`,
		`INSERT INTO printer_capabilities(id,printer_id,fingerprint,captured_at,raw_attributes,normalized_json) VALUES('invoice-cap','invoice-printer','x',1,'{}','{"paperSizes":[{"key":"A4"},{"key":"A6"}],"trays":[{"key":"tray-2","rawLabel":"Tray 2"}]}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method string, body any, auth bool, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, ts.URL+"/api/v1/owner/printers/invoice-printer/invoice-settings", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", ts.URL)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if auth {
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d want %d: %s", method, res.StatusCode, want, data)
		}
		return data
	}
	request("GET", nil, false, 401)
	var saved struct {
		Enabled     bool
		Threshold   int
		Paper, Tray string
	}
	if err := json.Unmarshal(request("GET", nil, true, 200), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.Threshold != 4 {
		t.Fatalf("default: %+v", saved)
	}
	input := map[string]any{"enabled": true, "threshold": 4, "paper": "A6", "tray": "Tray 2"}
	request("PUT", input, false, 401)
	request("PUT", input, true, 200)
	if err := json.Unmarshal(request("GET", nil, true, 200), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Enabled || saved.Paper != "A6" || saved.Tray != "Tray 2" {
		t.Fatalf("saved: %+v", saved)
	}
	input["tray"] = "Invented tray"
	request("PUT", input, true, 400)
	input["tray"] = "Tray 2"
	input["paper"] = "A9"
	request("PUT", input, true, 400)
	input["paper"] = "A6"
	input["threshold"] = -1
	request("PUT", input, true, 400)
	input["threshold"] = 4.5
	request("PUT", input, true, 400)
	input["threshold"] = 4
	input["enabled"] = false
	input["tray"] = ""
	request("PUT", input, true, 200)
}
