package autodelete

import (
	"context"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"strings"
	"testing"
	"time"
)

func TestPartialOrderRetainsEverySource(t *testing.T) {
	for _, scenario := range []string{"second-document-failed", "cups-unconfirmed", "checkout-awaiting-payment", "invoice-incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			files, sw := newTestRig(t)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := sw.db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().Unix()
			status := "paid"
			if scenario == "checkout-awaiting-payment" {
				status = "pending_payment"
			}
			exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at,submitted_at) VALUES('o','t',?,'INR',2,200,'','',?,?,?)`, status, now, now, now)
			for i := 0; i < 2; i++ {
				id := fmt.Sprint(i)
				path := "documents/" + id + ".pdf"
				if err := files.MkdirAll("documents"); err != nil {
					t.Fatal(err)
				}
				if err := files.WriteAtomic(path, strings.NewReader("synthetic PDF")); err != nil {
					t.Fatal(err)
				}
				exec(`INSERT INTO documents(id,order_id,original_filename,mime_type,size_bytes,page_count,sha256,storage_path,created_at,retention_until) VALUES(?,'o','test.pdf','application/pdf',13,3,'hash',?,?,?)`, id, path, now-90000, now-1)
				exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_end,unit_price_minor,line_total_minor) VALUES(?,'o',?,'A4','A4','monochrome','one-sided',1,3,100,100)`, id, id)
				if scenario != "checkout-awaiting-payment" {
					progress := "completed"
					if scenario == "second-document-failed" && i == 1 {
						progress = "review"
					}
					jobID := "test:" + id
					if scenario == "cups-unconfirmed" {
						jobID = "cups:" + id
					}
					exec(`INSERT INTO print_submissions(line_id,state,queue_name,job_id,progress,updated_at) VALUES(?,'submitted','shop',?,?,?)`, id, jobID, progress, now)
				}
			}
			if scenario == "invoice-incomplete" {
				exec(`INSERT INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,updated_at) VALUES('o','shop','A4','',1,'failed',?)`, now)
			}
			if n, err := sw.PurgeExpired(context.Background()); err != nil || n != 0 {
				t.Fatalf("recovery files deleted: count=%d err=%v", n, err)
			}
			for i := 0; i < 2; i++ {
				f, err := files.Open("documents/" + fmt.Sprint(i) + ".pdf")
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
			if scenario == "cups-unconfirmed" {
				if err := orders.New(sw.db, nil).ConfirmPrintDone(context.Background(), "o", "test-owner"); err != nil {
					t.Fatal(err)
				}
				if n, err := sw.PurgeExpired(context.Background()); err != nil || n != 2 {
					t.Fatal("confirmed order not cleaned", n, err)
				}
			}
		})
	}
}
