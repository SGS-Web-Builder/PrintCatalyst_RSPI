package autodelete

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShopRetentionPreservesHistoryAndUnprintedPaidFiles(t *testing.T) {
	files, sw := newTestRig(t)
	ctx := context.Background()
	now := time.Now().Unix()
	files.MkdirAll("documents/order")
	if err := files.WriteAtomic("documents/order/test.pdf", strings.NewReader("PDF")); err != nil {
		t.Fatal(err)
	}
	_, err := sw.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at,submitted_at) VALUES('order','token','paid','INR',2,100,'Customer','',?,?,?)`, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sw.db.Exec(`INSERT INTO documents(id,order_id,original_filename,mime_type,size_bytes,page_count,sha256,storage_path,created_at,retention_until) VALUES('doc','order','test.pdf','application/pdf',3,1,'hash','documents/order/test.pdf',?,?)`, now, now-1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sw.db.Exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_end,unit_price_minor,line_total_minor) VALUES('line','order','doc','A4','A4','monochrome','one-sided',1,1,100,100)`)
	if err != nil {
		t.Fatal(err)
	}
	if n, e := sw.PurgeExpired(ctx); e != nil || n != 0 {
		t.Fatalf("deleted unprinted paid file: %d %v", n, e)
	}
	sw.db.Exec("UPDATE documents SET dispatched_at=?", now-3601)
	sw.db.Exec("UPDATE business_settings SET auto_delete_enabled=0")
	if n, e := sw.PurgeExpired(ctx); e != nil || n != 0 {
		t.Fatalf("spooled file deleted before completion: %d %v", n, e)
	}
	sw.db.Exec("UPDATE documents SET created_at=?", now-86400)
	if n, e := sw.PurgeExpired(ctx); e != nil || n != 0 {
		t.Fatalf("expired paid file deleted before pickup: %d %v", n, e)
	}
	if _, err := sw.db.Exec("UPDATE orders SET status='completed'"); err != nil {
		t.Fatal(err)
	}
	if n, e := sw.PurgeExpired(ctx); e != nil || n != 1 {
		t.Fatalf("expired file: %d %v", n, e)
	}
	if _, e := os.Stat(filepath.Join(files.DataRoot(), "documents", "order", "test.pdf")); !os.IsNotExist(e) {
		t.Fatal("blob still exists")
	}
	var count int
	sw.db.QueryRow("SELECT COUNT(*) FROM order_lines JOIN documents d ON d.id=document_id WHERE d.purged_at>0").Scan(&count)
	if count != 1 {
		t.Fatal("order history lost")
	}
	if n, e := sw.PurgeExpired(ctx); e != nil || n != 0 {
		t.Fatal("cleanup not idempotent")
	}
}
