package localserver_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCheckoutRechecksFilesAfterQuoteBeforePayment(t *testing.T) {
	ts, db := portalFixture(t)
	body, kind := uploadMultipart(t, []uploadFile{{Name: "synthetic.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	resp, err := http.Post(ts.URL+"/api/v1/portal/uploads", kind, body)
	if err != nil {
		t.Fatal(err)
	}
	var up map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatal(up)
	}
	doc := up["files"].([]any)[0].(map[string]any)["documentId"]
	// Deterministically simulate expiry between the initial checks and commit.
	if _, err = db.DB().Exec(`CREATE TRIGGER expire_at_checkout AFTER UPDATE OF submitted_at ON orders WHEN NEW.submitted_at>0 AND OLD.submitted_at=0 BEGIN UPDATE documents SET purged_at=1 WHERE order_id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"orderId": up["orderId"], "customerName": "Synthetic", "customerPhone": "0000000000", "lines": []any{map[string]any{"documentId": doc, "paperSize": "A4", "colourMode": "monochrome", "sides": "one-sided", "copies": 1, "pageRangeStart": 1, "pageRangeEnd": 1}}}
	checkoutJSON(t, ts.URL+"/api/v1/portal/orders", payload, 409)
	for _, q := range []string{`SELECT COUNT(*) FROM orders WHERE submitted_at>0`, `SELECT COUNT(*) FROM order_lines`, `SELECT COUNT(*) FROM payment_intents`} {
		var n int
		if err = db.DB().QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatal("checkout/payment was committed without files", n, err)
		}
	}
}
