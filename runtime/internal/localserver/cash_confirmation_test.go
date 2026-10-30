package localserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"net/http"
	"strings"
	"testing"
)

func TestKioskCashConfirmationAndRelease(t *testing.T) {
	ts, db, _ := portalFixtureWithData(t, localserver.WithPickup(&pickup.Service{}))
	resp, err := http.Get(ts.URL + "/api/v1/portal/options")
	if err != nil {
		t.Fatal(err)
	}
	var options struct {
		Cash   bool `json:"cashOnCounterReady"`
		Online bool `json:"razorpayReady"`
	}
	json.NewDecoder(resp.Body).Decode(&options)
	resp.Body.Close()
	if !options.Cash || options.Online {
		t.Fatalf("cash without Razorpay: %+v", options)
	}
	cookie, csrf := signedInAsOwner(t, ts.URL)
	p, err := pickup.New(db.DB(), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"cash", "cash-otp", "online", "draft", "cancelled"} {
		_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,customer_email,customer_notes,payment_method,created_at,updated_at,submitted_at,portal_gate) VALUES(?,?,'pending_payment','INR',2,800,'Sample','','','','cash_on_counter',1,1,1,0)`, id, "token-"+id)
		if err != nil {
			t.Fatal(err)
		}
	}
	db.DB().Exec("UPDATE orders SET payment_method='razorpay' WHERE id='online'")
	db.DB().Exec("UPDATE orders SET submitted_at=0 WHERE id='draft'")
	db.DB().Exec("UPDATE orders SET status='cancelled' WHERE id='cancelled'")
	call := func(id string, auth bool, token, body string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/orders/"+id+"/cash-confirm", strings.NewReader(body))
		req.Header.Set("Origin", ts.URL)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", token)
		if auth {
			req.AddCookie(cookie)
		}
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if n := call("cash", false, "", `{"confirm":true}`); n != 401 {
		t.Fatal("unauth", n)
	}
	if n := call("cash", true, "", `{"confirm":true}`); n != 403 {
		t.Fatal("csrf", n)
	}
	if n := call("cash", true, csrf, `{}`); n != 400 {
		t.Fatal("confirmation", n)
	}
	for _, id := range []string{"online", "draft", "cancelled"} {
		if n := call(id, true, csrf, `{"confirm":true}`); n != 409 {
			t.Fatal(id, n)
		}
	}
	if err = p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_pickups").Scan(&count)
	if count != 0 {
		t.Fatal("unpaid pickup issued")
	}
	for i := 0; i < 2; i++ {
		if n := call("cash", true, csrf, `{"confirm":true}`); n != 200 {
			t.Fatal("cash", n)
		}
	}
	db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_cash_payments").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate cash", count)
	}
	db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_verified_payments").Scan(&count)
	if count != 0 {
		t.Fatal("cash mislabelled gateway verified")
	}
	if err = p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := p.ViewForOrder(ctx, "cash")
	if err != nil || len(v.Code) != 4 {
		t.Fatal(v, err)
	}
	if _, err = p.Claim(ctx, v.Code); err == nil {
		t.Fatal("unprepared print released")
	}
	if err = p.MarkPrepared(ctx, "cash", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	db.DB().Exec("UPDATE kiosk_control SET otp_enabled=0")
	n, err := p.ReleaseAutomatic(ctx)
	if err != nil || n != 1 {
		t.Fatal("automatic cash release", n, err)
	}
	n, err = p.ReleaseAutomatic(ctx)
	if err != nil || n != 0 {
		t.Fatal("duplicate release", n, err)
	}
	if n := call("cash-otp", true, csrf, `{"confirm":true}`); n != 200 {
		t.Fatal(n)
	}
	db.DB().Exec("UPDATE kiosk_control SET otp_enabled=1")
	p, err = pickup.New(db.DB(), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	v, err = p.ViewForOrder(ctx, "cash-otp")
	if err != nil || len(v.Code) != 4 {
		t.Fatal(v, err)
	}
	if err = p.MarkPrepared(ctx, "cash-otp", strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if id, e := p.Claim(ctx, v.Code); e != nil || id != "cash-otp" {
		t.Fatal(id, e)
	}
	if _, e := p.Claim(ctx, v.Code); e == nil {
		t.Fatal("duplicate cash OTP accepted")
	}

}

func TestKioskCashPortalQuoteAndOrder(t *testing.T) {
	ts, database, _ := portalFixtureWithData(t, localserver.WithPickup(&pickup.Service{}))
	uploadBody, uploadCT := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", uploadCT, uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	var upload struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	lines := []map[string]any{{
		"documentId":     upload.Files[0].DocumentID,
		"paperSize":      "A4",
		"colourMode":     "monochrome",
		"sides":          "one-sided",
		"copies":         2,
		"pageRangeStart": 1,
		"pageRangeEnd":   3,
	}}
	quoteBody, _ := json.Marshal(map[string]any{"lines": lines})
	qResp, err := http.Post(ts.URL+"/api/v1/portal/quote", "application/json", bytes.NewReader(quoteBody))
	if err != nil {
		t.Fatal(err)
	}
	defer qResp.Body.Close()
	if qResp.StatusCode != 200 {
		t.Fatalf("quote status = %d, body = %s", qResp.StatusCode, readAllBody(t, qResp))
	}
	var quote struct {
		TotalMinor int64 `json:"totalMinor"`
		Lines      []struct {
			Sheets         int64 `json:"sheets"`
			UnitPriceMinor int64 `json:"unitPriceMinor"`
			LineTotalMinor int64 `json:"lineTotalMinor"`
		} `json:"lines"`
	}
	if err := json.NewDecoder(qResp.Body).Decode(&quote); err != nil {
		t.Fatal(err)
	}
	if quote.Lines[0].Sheets != 6 {
		t.Fatalf("sheets = %d, want 6 (3 pages × 2 copies)", quote.Lines[0].Sheets)
	}
	if quote.Lines[0].LineTotalMinor != 6*250 {
		t.Fatalf("line total = %d, want %d", quote.Lines[0].LineTotalMinor, 6*250)
	}
	if quote.TotalMinor != 6*250 {
		t.Fatalf("total = %d, want %d", quote.TotalMinor, 6*250)
	}

	// Now place the order.
	orderBody, _ := json.Marshal(map[string]any{
		"orderId":       upload.OrderID,
		"lines":         lines,
		"customerName":  "Ravi Sharma",
		"customerPhone": "+919876543210",
		"customerEmail": "ravi@example.com",
	})
	oResp, err := http.Post(ts.URL+"/api/v1/portal/orders", "application/json", bytes.NewReader(orderBody))
	if err != nil {
		t.Fatal(err)
	}
	defer oResp.Body.Close()
	if oResp.StatusCode != 201 {
		t.Fatalf("place order status = %d, body = %s", oResp.StatusCode, readAllBody(t, oResp))
	}
	var placed struct {
		OrderID    string `json:"orderId"`
		ShareToken string `json:"shareToken"`
		TotalMinor int64  `json:"totalMinor"`
		Status     string `json:"status"`
	}
	if err := json.NewDecoder(oResp.Body).Decode(&placed); err != nil {
		t.Fatal(err)
	}
	if placed.OrderID != upload.OrderID {
		t.Fatalf("orderId = %s, want %s", placed.OrderID, upload.OrderID)
	}
	if placed.TotalMinor != 6*250 {
		t.Fatalf("total = %d, want %d", placed.TotalMinor, 6*250)
	}
	if placed.Status != orders.StatusPendingPayment {
		t.Fatalf("status = %s", placed.Status)
	}

	// And persist check.
	row := database.DB().QueryRow("SELECT COUNT(*) FROM orders WHERE id=? AND status=?", placed.OrderID, orders.StatusPendingPayment)
	var n int
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("order persistence: %d %v", n, err)
	}
	row = database.DB().QueryRow("SELECT COUNT(*) FROM order_lines WHERE order_id=?", placed.OrderID)
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("order lines persistence: %d %v", n, err)
	}
}

func TestUnpaidCashOTPInOrderReceivedResponse(t *testing.T) {
	holder := &pickup.Service{}
	ts, database, _ := portalFixtureWithData(t, localserver.WithPickup(holder))
	actual, e := pickup.New(database.DB(), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if e != nil {
		t.Fatal(e)
	}
	*holder = *actual
	if _, e = database.DB().Exec("UPDATE business_settings SET auto_print_mode='all_documents'"); e != nil {
		t.Fatal(e)
	}
	uploadBody, uploadCT := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", uploadCT, uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	var upload struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	lines := []map[string]any{{
		"documentId":     upload.Files[0].DocumentID,
		"paperSize":      "A4",
		"colourMode":     "monochrome",
		"sides":          "one-sided",
		"copies":         2,
		"pageRangeStart": 1,
		"pageRangeEnd":   3,
	}}
	quoteBody, _ := json.Marshal(map[string]any{"lines": lines})
	qResp, err := http.Post(ts.URL+"/api/v1/portal/quote", "application/json", bytes.NewReader(quoteBody))
	if err != nil {
		t.Fatal(err)
	}
	defer qResp.Body.Close()
	if qResp.StatusCode != 200 {
		t.Fatalf("quote status = %d, body = %s", qResp.StatusCode, readAllBody(t, qResp))
	}
	var quote struct {
		TotalMinor int64 `json:"totalMinor"`
		Lines      []struct {
			Sheets         int64 `json:"sheets"`
			UnitPriceMinor int64 `json:"unitPriceMinor"`
			LineTotalMinor int64 `json:"lineTotalMinor"`
		} `json:"lines"`
	}
	if err := json.NewDecoder(qResp.Body).Decode(&quote); err != nil {
		t.Fatal(err)
	}
	if quote.Lines[0].Sheets != 6 {
		t.Fatalf("sheets = %d, want 6 (3 pages × 2 copies)", quote.Lines[0].Sheets)
	}
	if quote.Lines[0].LineTotalMinor != 6*250 {
		t.Fatalf("line total = %d, want %d", quote.Lines[0].LineTotalMinor, 6*250)
	}
	if quote.TotalMinor != 6*250 {
		t.Fatalf("total = %d, want %d", quote.TotalMinor, 6*250)
	}

	// Now place the order.
	orderBody, _ := json.Marshal(map[string]any{
		"orderId":       upload.OrderID,
		"lines":         lines,
		"customerName":  "Ravi Sharma",
		"customerPhone": "+919876543210",
		"customerEmail": "ravi@example.com",
	})
	oResp, err := http.Post(ts.URL+"/api/v1/portal/orders", "application/json", bytes.NewReader(orderBody))
	if err != nil {
		t.Fatal(err)
	}
	defer oResp.Body.Close()
	if oResp.StatusCode != 201 {
		t.Fatalf("place order status = %d, body = %s", oResp.StatusCode, readAllBody(t, oResp))
	}
	var placed struct {
		Pickup     pickup.View `json:"pickup"`
		OrderID    string      `json:"orderId"`
		ShareToken string      `json:"shareToken"`
		TotalMinor int64       `json:"totalMinor"`
		Status     string      `json:"status"`
	}
	if err := json.NewDecoder(oResp.Body).Decode(&placed); err != nil {
		t.Fatal(err)
	}
	if len(placed.Pickup.Code) != 4 || !placed.Pickup.PaymentPending {
		t.Fatalf("missing unpaid receipt OTP: %+v", placed.Pickup)
	}
	if placed.OrderID != upload.OrderID {
		t.Fatalf("orderId = %s, want %s", placed.OrderID, upload.OrderID)
	}
	if placed.TotalMinor != 6*250 {
		t.Fatalf("total = %d, want %d", placed.TotalMinor, 6*250)
	}
	if placed.Status != orders.StatusPendingPayment {
		t.Fatalf("status = %s", placed.Status)
	}

	// And persist check.
	row := database.DB().QueryRow("SELECT COUNT(*) FROM orders WHERE id=? AND status=?", placed.OrderID, orders.StatusPendingPayment)
	var n int
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("order persistence: %d %v", n, err)
	}
	row = database.DB().QueryRow("SELECT COUNT(*) FROM order_lines WHERE order_id=?", placed.OrderID)
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("order lines persistence: %d %v", n, err)
	}
}
