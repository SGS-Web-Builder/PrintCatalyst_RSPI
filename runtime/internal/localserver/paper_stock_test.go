package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestPaperStockAccounting(t *testing.T) {
	ts, db := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.DB().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO printers(id,backend,queue_name,created_at,last_seen_at) VALUES('stock-printer','mock','stock-queue',1,1)`)
	seed := func(id, selected, sides, progress string, pages, copies, nup int) {
		t.Helper()
		exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,customer_email,customer_notes,created_at,updated_at,portal_gate) VALUES(?,?,'dispatched','INR',2,0,'','','','',1,1,0)`, id, id)
		exec(`INSERT INTO documents(id,order_id,original_filename,mime_type,size_bytes,page_count,sha256,storage_path,created_at,retention_until) VALUES(?,?,'test.pdf','application/pdf',1,?,'hash','unused',1,0)`, id, id, pages)
		exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,unit_price_minor,line_total_minor,pages_per_sheet,selected_pages_json) VALUES(?,?,?,'A4','A4','monochrome',?,?,1,?,0,0,?,?)`, id, id, id, sides, copies, pages, nup, selected)
		exec(`INSERT INTO print_submissions(line_id,state,queue_name,updated_at,progress) VALUES(?,'submitted','stock-queue',1,?)`, id, progress)
	}
	request := func(method string, body map[string]any, auth bool, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, ts.URL+"/api/v1/owner/paper-stock", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", ts.URL)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if auth {
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s got %d want %d: %s", method, response.StatusCode, want, data)
		}
		return data
	}
	check := func(inserted, printed, remaining int) {
		t.Helper()
		var items []struct {
			PrinterID                    string
			Inserted, Printed, Remaining int
		}
		if err := json.Unmarshal(request("GET", nil, true, 200), &items); err != nil {
			t.Fatal(err)
		}
		for _, v := range items {
			if v.PrinterID == "stock-printer" {
				if v.Inserted != inserted || v.Printed != printed || v.Remaining != remaining {
					t.Fatalf("unexpected stock: %+v", v)
				}
				return
			}
		}
		t.Fatal("printer missing")
	}
	seed("old", "null", "one-sided", "completed", 10, 1, 1)
	exec(`INSERT INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,progress,sheets,updated_at) VALUES('old','stock-queue','A6','Tray 2',5,'submitted','completed',2,1)`)
	request("GET", nil, false, 401)
	check(0, 0, 0)
	refill := map[string]any{"printerId": "stock-printer", "sheets": 100, "requestId": "refill-request-0001"}
	request("POST", refill, false, 401)
	request("POST", refill, true, 200)
	request("POST", refill, true, 200)
	check(100, 0, 100)
	seed("new", "null", "one-sided", "completed", 5, 2, 1)                     // 10 sheets
	seed("duplex", "[1,3,5,7,9]", "two-sided-long-edge", "completed", 9, 3, 2) // ceil(5/4)*3 = 6
	seed("pending", "null", "one-sided", "printing", 7, 1, 1)
	check(100, 16, 84)
	check(100, 16, 84)
	exec(`UPDATE print_submissions SET progress='completed' WHERE line_id='pending'`)
	check(100, 23, 77)
	refill["sheets"] = 50
	request("POST", refill, true, 409)
	refill["requestId"] = "refill-request-0002"
	request("POST", refill, true, 200)
	check(150, 23, 127)
	refill["sheets"] = 0
	request("POST", refill, true, 400)
	exec(`INSERT INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,progress,sheets,updated_at) VALUES('new','stock-queue','A6','Tray 2',5,'submitted','printing',2,1)`)
	check(150, 23, 127)
	exec(`UPDATE separator_invoices SET progress='completed' WHERE order_id='new'`)
	check(150, 25, 125)
	check(150, 25, 125)
}
