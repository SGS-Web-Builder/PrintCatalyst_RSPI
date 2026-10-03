package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
)

// signedInAsOwner is a helper that signs in the owner and returns the session
// cookie and CSRF token from the bootstrap fixture.
func signedInAsOwner(t *testing.T, baseURL string) (*http.Cookie, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "owner", "password": "a sufficiently long password"})
	req, _ := http.NewRequest("POST", baseURL+"/api/v1/owner/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", baseURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var parsed map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	csrf := parsed["csrfToken"]
	if csrf == "" {
		t.Fatal("missing csrfToken")
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie")
	}
	return cookies[0], csrf
}

// placeAnOrder creates a paid order through the portal path and returns the
// order id. It uses the same fixture backing the other portal tests.
func placeAnOrder(t *testing.T, baseURL string) string {
	t.Helper()
	body, contentType := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	uploadResp, err := http.Post(baseURL+"/api/v1/portal/uploads", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	var upload struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	uploadResp.Body.Close()

	lines := []map[string]any{{
		"documentId":     upload.Files[0].DocumentID,
		"paperSize":      "A4",
		"colourMode":     "monochrome",
		"sides":          "one-sided",
		"copies":         1,
		"pageRangeStart": 1,
		"pageRangeEnd":   3,
	}}
	orderBody, _ := json.Marshal(map[string]any{
		"orderId":       upload.OrderID,
		"lines":         lines,
		"customerName":  "Ravi",
		"customerPhone": "+919876543210",
	})
	oResp, err := http.Post(baseURL+"/api/v1/portal/orders", "application/json", bytes.NewReader(orderBody))
	if err != nil {
		t.Fatal(err)
	}
	defer oResp.Body.Close()
	if oResp.StatusCode != 201 {
		bs, _ := io.ReadAll(oResp.Body)
		t.Fatalf("order status = %d, body = %s", oResp.StatusCode, string(bs))
	}
	return upload.OrderID
}

func TestOwnerStatusTransitionRequiresOwner(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	orderID := placeAnOrder(t, ts.URL)

	// Status transitions: pending_payment → paid (owner only).
	putBody, _ := json.Marshal(map[string]string{"status": orders.StatusPaid})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/orders/"+orderID+"/status", bytes.NewReader(putBody))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("transition status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != orders.StatusPaid {
		t.Fatalf("status = %s, want paid", got.Status)
	}
}

func TestOwnerStatusTransitionRejectsIllegalTransition(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	orderID := placeAnOrder(t, ts.URL)

	// Try to transition pending_payment directly to completed (illegal).
	putBody, _ := json.Marshal(map[string]string{"status": orders.StatusCompleted})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/orders/"+orderID+"/status", bytes.NewReader(putBody))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("illegal status = %d, want 409, body = %s", resp.StatusCode, string(bs))
	}
}

func TestOwnerIssueInvoiceAndReadItBack(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	// This invoice checks customer details, so explicitly enable their collection.
	settings := putJSON(t, ts.URL, "/api/v1/owner/customer-details", cookie, csrf, []byte(`{"enabled":true,"fields":{"customerName":"required","customerPhone":"required","customerEmail":"optional","customerNotes":"optional"}}`))
	settings.Body.Close()
	if settings.StatusCode != http.StatusOK {
		t.Fatalf("enable customer details: %d", settings.StatusCode)
	}
	orderID := placeAnOrder(t, ts.URL)

	// pending_payment → paid.
	putBody, _ := json.Marshal(map[string]string{"status": orders.StatusPaid})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/orders/"+orderID+"/status", bytes.NewReader(putBody))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Issue invoice.
	issueReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/orders/"+orderID+"/invoice", nil)
	issueReq.AddCookie(cookie)
	issueReq.Header.Set("X-CSRF-Token", csrf)
	issueReq.Header.Set("Origin", ts.URL)
	issueReq.Header.Set("Content-Type", "application/json")
	issueResp, err := http.DefaultClient.Do(issueReq)
	if err != nil {
		t.Fatal(err)
	}
	defer issueResp.Body.Close()
	if issueResp.StatusCode != 201 {
		bs, _ := io.ReadAll(issueResp.Body)
		t.Fatalf("issue status = %d, body = %s", issueResp.StatusCode, string(bs))
	}
	var inv struct {
		Number       int64  `json:"number"`
		TotalMinor   int64  `json:"totalMinor"`
		Currency     string `json:"currency"`
		CustomerName string `json:"customerName"`
	}
	if err := json.NewDecoder(issueResp.Body).Decode(&inv); err != nil {
		t.Fatal(err)
	}
	if inv.Number != 1 {
		t.Fatalf("number = %d, want 1", inv.Number)
	}
	if inv.TotalMinor <= 0 {
		t.Fatalf("total = %d, want > 0", inv.TotalMinor)
	}
	if inv.CustomerName != "Ravi" {
		t.Fatalf("customer = %s, want Ravi", inv.CustomerName)
	}

	// Read it back.
	readReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders/"+orderID+"/invoice", nil)
	readReq.AddCookie(cookie)
	readReq.Header.Set("X-CSRF-Token", csrf)
	readResp, err := http.DefaultClient.Do(readReq)
	if err != nil {
		t.Fatal(err)
	}
	defer readResp.Body.Close()
	if readResp.StatusCode != 200 {
		bs, _ := io.ReadAll(readResp.Body)
		t.Fatalf("read invoice status = %d, body = %s", readResp.StatusCode, string(bs))
	}

	// List invoices.
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/invoices", nil)
	listReq.AddCookie(cookie)
	listReq.Header.Set("X-CSRF-Token", csrf)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != 200 {
		bs, _ := io.ReadAll(listResp.Body)
		t.Fatalf("list invoices status = %d, body = %s", listResp.StatusCode, string(bs))
	}
	var listed struct {
		Invoices []struct {
			Number int64 `json:"number"`
		} `json:"invoices"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Invoices) != 1 {
		t.Fatalf("invoices = %d, want 1", len(listed.Invoices))
	}
}

func TestOwnerIssueInvoiceRejectsDuplicate(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	orderID := placeAnOrder(t, ts.URL)

	// Move to paid and issue invoice once.
	putBody, _ := json.Marshal(map[string]string{"status": orders.StatusPaid})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/orders/"+orderID+"/status", bytes.NewReader(putBody))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil { resp.Body.Close() }

	issueReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/orders/"+orderID+"/invoice", nil)
	issueReq.AddCookie(cookie)
	issueReq.Header.Set("X-CSRF-Token", csrf)
	issueReq.Header.Set("Origin", ts.URL)
	issueReq.Header.Set("Content-Type", "application/json")
	first, _ := http.DefaultClient.Do(issueReq)
	if first != nil { first.Body.Close() }
	if first.StatusCode != 201 {
		t.Fatalf("first issue = %d, want 201", first.StatusCode)
	}

	// Second attempt should be 400 or 409 because an invoice already exists.
	issueReq2, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/orders/"+orderID+"/invoice", nil)
	issueReq2.AddCookie(cookie)
	issueReq2.Header.Set("X-CSRF-Token", csrf)
	issueReq2.Header.Set("Origin", ts.URL)
	issueReq2.Header.Set("Content-Type", "application/json")
	second, err := http.DefaultClient.Do(issueReq2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != 400 && second.StatusCode != 409 {
		bs, _ := io.ReadAll(second.Body)
		t.Fatalf("second issue = %d, body = %s", second.StatusCode, string(bs))
	}
}
