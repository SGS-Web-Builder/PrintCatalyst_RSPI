package autodelete

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPaidPickupSurvivesExpiryUntilCompletion(t *testing.T) {
	for _, state := range []string{"paid", "expired-pickup", "failed-pickup"} {
		t.Run(state, func(t *testing.T) {
			files, s := newTestRig(t)
			ctx := context.Background()
			now := time.Now().Unix()
			status := "paid"
			if state == "failed-pickup" {
				status = "failed"
			}
			_, err := s.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('paid','token',?,'INR',2,500,'Customer','',?,?)`, status, now, now)
			if err != nil {
				t.Fatal(err)
			}
			if state != "paid" {
				_, err = s.db.Exec(`INSERT INTO kiosk_pickups(order_id,payment_reference,code_hash,code_cipher,state,expires_at,created_at) VALUES('paid','verified',X'01',X'02','expired',?,?)`, now-1, now-90000)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(files.DataRoot(), "documents", "doc.pdf")
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte("PDF"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec(`INSERT INTO documents(id,order_id,original_filename,mime_type,size_bytes,page_count,sha256,storage_path,created_at,retention_until) VALUES('doc','paid','doc.pdf','application/pdf',3,1,'hash','documents/doc.pdf',?,?)`, now-90000, now-1)
			if err != nil {
				t.Fatal(err)
			}
			n, err := s.PurgeExpired(ctx)
			if err != nil || n != 0 {
				t.Fatal("paid uncollected file purged", n, err)
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatal("paid file missing", err)
			}
			if _, err = s.db.Exec("UPDATE orders SET status='completed' WHERE id='paid'"); err != nil {
				t.Fatal(err)
			}
			n, err = s.PurgeExpired(ctx)
			if err != nil || n != 1 {
				t.Fatal("completed file not purged", n, err)
			}
			var count int
			if err = s.db.QueryRow("SELECT COUNT(*) FROM orders WHERE id='paid'").Scan(&count); err != nil || count != 1 {
				t.Fatal("order details removed", err)
			}
		})
	}
}
