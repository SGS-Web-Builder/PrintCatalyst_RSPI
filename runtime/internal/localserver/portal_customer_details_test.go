package localserver_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCustomerDetailsPolicyCheckout(t *testing.T) {
	ts, db := portalFixture(t)
	var initial string
	if err := db.DB().QueryRow("SELECT settings FROM portal_customer_details WHERE singleton=1").Scan(&initial); err != nil {
		t.Fatal(err)
	}
	var defaults struct{ Enabled bool }
	if err := json.Unmarshal([]byte(initial), &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.Enabled {
		t.Fatal("customer details should be disabled by default")
	}
	cookie, csrf := signedInAsOwner(t, ts.URL)
	path := "/api/v1/owner/customer-details"
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("settings exposed without authentication")
	}
	save := func(body string, want int) {
		t.Helper()
		r := putJSON(t, ts.URL, path, cookie, csrf, []byte(body))
		defer r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("save: %d want %d", r.StatusCode, want)
		}
	}
	save(`{"enabled":true,"fields":{"customerName":"hidden","customerPhone":"hidden","customerEmail":"required","customerNotes":"optional"}}`, 200)
	save(`{"enabled":true,"fields":{"customerName":"invalid"}}`, 400)
	response, err := http.Get(ts.URL + "/api/v1/portal/options")
	if err != nil {
		t.Fatal(err)
	}
	var options struct {
		Details struct {
			Enabled bool
			Fields  map[string]string
		} `json:"customerDetails"`
	}
	json.NewDecoder(response.Body).Decode(&options)
	response.Body.Close()
	if !options.Details.Enabled || options.Details.Fields["customerEmail"] != "required" {
		t.Fatal("policy missing from portal")
	}
	body, kind := uploadMultipart(t, []uploadFile{{Name: "test.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	response, err = http.Post(ts.URL+"/api/v1/portal/uploads", kind, body)
	if err != nil {
		t.Fatal(err)
	}
	var upload struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			ID string `json:"documentId"`
		} `json:"files"`
	}
	json.NewDecoder(response.Body).Decode(&upload)
	response.Body.Close()
	if len(upload.Files) != 1 {
		t.Fatal("upload failed")
	}
	request := map[string]any{"orderId": upload.OrderID, "customerName": "Do not store", "customerPhone": "1234567890", "lines": []any{map[string]any{"documentId": upload.Files[0].ID, "paperSize": "A4", "colourMode": "monochrome", "sides": "one-sided", "copies": 1, "pageRangeStart": 1, "pageRangeEnd": 3}}}
	checkoutJSON(t, ts.URL+"/api/v1/portal/orders", request, 400)
	save(`{"enabled":false,"fields":{"customerName":"required","customerPhone":"required","customerEmail":"required","customerNotes":"optional"}}`, 200)
	checkoutJSON(t, ts.URL+"/api/v1/portal/orders", request, 201)
	var name, phone, email string
	if err := db.DB().QueryRow("SELECT customer_name,customer_phone,customer_email FROM orders WHERE id=?", upload.OrderID).Scan(&name, &phone, &email); err != nil {
		t.Fatal(err)
	}
	if name != "" || phone != "" || email != "" {
		t.Fatal("disabled customer details were stored")
	}
}
