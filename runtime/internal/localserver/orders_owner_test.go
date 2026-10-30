package localserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
)

func TestPickupReissueRequiresSessionAndCSRF(t *testing.T) {
	ts, _ := portalFixture(t)
	url := ts.URL + "/api/v1/owner/orders/abc12345abc12345abc12345abc12345/pickup/reissue"
	request := func(path string, cookie *http.Cookie, csrf, body string) *http.Response {
		req, err := http.NewRequest("POST", path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		req.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	unauth := request(url, nil, "", "{}")
	unauth.Body.Close()
	if unauth.StatusCode != 401 {
		t.Fatalf("unauthenticated status %d", unauth.StatusCode)
	}
	login := request(ts.URL+"/api/v1/owner/login", nil, "", `{"username":"owner","password":"a sufficiently long password"}`)
	defer login.Body.Close()
	if login.StatusCode != 200 {
		t.Fatalf("login %d", login.StatusCode)
	}
	csrf := readCSRF(t, login)
	cookie := login.Cookies()[0]
	noCSRF := request(url, cookie, "", "{}")
	noCSRF.Body.Close()
	if noCSRF.StatusCode != 403 {
		t.Fatalf("missing CSRF status %d", noCSRF.StatusCode)
	}
	valid := request(url, cookie, csrf, "{}")
	valid.Body.Close()
	if valid.StatusCode != 503 {
		t.Fatalf("non-kiosk route status %d", valid.StatusCode)
	}
}

// TestOwnerOrdersListRequiresOwnerAuth verifies that the owner orders endpoint
// refuses unauthenticated callers.
func TestOwnerOrdersListRequiresOwnerAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	// Loopback peers are accepted by localOwnerRequest, but no session cookie is
	// present, so the orders list should be 401.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatalf("status = 200, want non-200 (no auth)")
	}
}

// TestOwnerOrdersListWithAuthenticatedSession signs the owner in, then exercises
// the orders list endpoint with the session cookie and CSRF token. It also
// inserts a synthetic portal order to verify the list returns real rows.
func TestOwnerOrdersListWithAuthenticatedSession(t *testing.T) {
	ts, db, dataRoot := portalFixtureWithData(t)
	// Insert a synthetic portal order so the list is non-empty.
	now := int64(1700000000)
	orderID := "abc12345abc12345abc12345abc12345"
	if _, err := db.DB().Exec(`
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		orderID, "share-token-aaaaaaaaaaaaaaaaaaaaaaaaaaaa", orders.StatusPendingPayment, "INR", 2, 2500,
		"Ravi", "+919876543210", "", "", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"doc12345doc12345doc12345doc12345", orderID, "x.pdf", "application/pdf", 1024, 1,
		"deadbeef", "documents/"+orderID+"/x.pdf", now, now+604800); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, unit_price_minor, line_total_minor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"line1234line1234line1234line12345", orderID, "doc12345doc12345doc12345doc12345",
		"A4", "A4", "monochrome", "one-sided", 1, 1, 1, 250, 250); err != nil {
		t.Fatal(err)
	}

	if _, err := db.DB().Exec("UPDATE orders SET submitted_at=?,print_requested=1 WHERE id=?", now, orderID); err != nil {
		t.Fatal(err)
	}
	files := mustNewFiles(t, dataRoot)
	if err := files.WriteAtomic("documents/"+orderID+"/x.pdf", bytes.NewReader([]byte("%PDF-test-preview"))); err != nil {
		t.Fatal(err)
	}

	// Sign in.
	body, _ := json.Marshal(map[string]string{"username": "owner", "password": "a sufficiently long password"})
	loginReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/login", bytes.NewReader(body))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Sec-Fetch-Site", "same-origin")
	loginReq.Header.Set("Origin", ts.URL)
	loginResp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != 200 {
		bs, _ := io.ReadAll(loginResp.Body)
		t.Fatalf("login status = %d, body = %s", loginResp.StatusCode, string(bs))
	}
	csrfToken := readCSRF(t, loginResp)
	cookies := loginResp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie set")
	}
	session := cookies[0]

	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders", nil)
	listReq.AddCookie(session)
	listReq.Header.Set("X-CSRF-Token", csrfToken)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != 200 {
		bs, _ := io.ReadAll(listResp.Body)
		t.Fatalf("orders list status = %d, body = %s", listResp.StatusCode, string(bs))
	}
	var listed struct {
		Orders []struct {
			ID             string `json:"id"`
			CustomerName   string `json:"customerName"`
			TotalMinor     int64  `json:"totalMinor"`
			PrintRequested bool   `json:"printRequested"`
			Documents      []struct {
				Filename  string `json:"filename"`
				PaperSize string `json:"paperSize"`
				Copies    int    `json:"copies"`
			} `json:"documents"`
		} `json:"orders"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range listed.Orders {
		if o.ID == orderID {
			found = true
			if !o.PrintRequested || len(o.Documents) != 1 || o.Documents[0].Filename != "x.pdf" || o.Documents[0].PaperSize != "A4" || o.Documents[0].Copies != 1 {
				t.Fatalf("queue settings missing: %+v", o)
			}
			if o.CustomerName != "Ravi" {
				t.Fatalf("customer name = %s, want Ravi", o.CustomerName)
			}
			if o.TotalMinor != 2500 {
				t.Fatalf("total = %d, want 2500", o.TotalMinor)
			}
		}
	}
	if !found {
		t.Fatalf("order %s not in list", orderID)
	}

	for _, tt := range []struct {
		id   string
		auth bool
		want int
	}{{orderID, true, 200}, {orderID, false, 401}, {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", true, 404}} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders/"+tt.id+"/documents/doc12345doc12345doc12345doc12345", nil)
		if tt.auth {
			req.AddCookie(session)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tt.want {
			t.Fatalf("preview status %d want %d: %s", res.StatusCode, tt.want, data)
		}
		if tt.want == 200 && string(data) != "%PDF-test-preview" {
			t.Fatal("wrong preview document")
		}
	}

	// Detail endpoint.
	detailReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders/"+orderID, nil)
	detailReq.AddCookie(session)
	detailReq.Header.Set("X-CSRF-Token", csrfToken)
	detailResp, err := http.DefaultClient.Do(detailReq)
	if err != nil {
		t.Fatal(err)
	}
	defer detailResp.Body.Close()
	if detailResp.StatusCode != 200 {
		bs, _ := io.ReadAll(detailResp.Body)
		t.Fatalf("detail status = %d, body = %s", detailResp.StatusCode, string(bs))
	}

	// Bad ID is rejected.
	badReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders/0", nil)
	badReq.AddCookie(session)
	badReq.Header.Set("X-CSRF-Token", csrfToken)
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != 400 {
		t.Fatalf("bad id status = %d, want 400", badResp.StatusCode)
	}

	// Both customer and dashboard see completion without recording cash payment.
	if _, err := db.DB().Exec(`INSERT INTO print_submissions(line_id,state,queue_name,job_id,updated_at,progress) VALUES('line1234line1234line1234line12345','submitted','Test','winspool-job-1',1,'printing')`); err != nil {
		t.Fatal(err)
	}
	res := putJSON(t, ts.URL, "/api/v1/owner/orders/"+orderID+"/status", session, csrfToken, []byte(`{"status":"print_completed"}`))
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("confirm print: %d %s", res.StatusCode, raw)
	}
	portalResp, err := http.Get(ts.URL + "/api/v1/portal/orders/" + orderID)
	if err != nil {
		t.Fatal(err)
	}
	var progress struct {
		Status      string
		PrintStatus string
	}
	err = json.NewDecoder(portalResp.Body).Decode(&progress)
	portalResp.Body.Close()
	if err != nil || progress.Status != "pending_payment" || progress.PrintStatus != "done" {
		t.Fatalf("portal progress: %+v %v", progress, err)
	}
	progressListReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/orders", nil)
	progressListReq.AddCookie(session)
	progressListResp, err := http.DefaultClient.Do(progressListReq)
	if err != nil {
		t.Fatal(err)
	}
	var updated struct {
		Orders []struct {
			ID        string
			Status    string
			Documents []struct {
				PrintState     string
				UnitPriceMinor int64
			}
		}
	}
	err = json.NewDecoder(progressListResp.Body).Decode(&updated)
	progressListResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, o := range updated.Orders {
		if o.ID == orderID {
			seen = true
			if o.Status != "pending_payment" || len(o.Documents) != 1 || o.Documents[0].PrintState != "completed" || o.Documents[0].UnitPriceMinor != 250 {
				t.Fatalf("dashboard progress: %+v", o)
			}
		}
	}
	if !seen {
		t.Fatal("completed cash order missing")
	}

	// Suppress unused-import warning for orders when the file is the only one
	// referencing it.
	_ = orders.StatusPendingPayment
	_ = pricing.ColourMonochrome
	_ = context.Background
}
