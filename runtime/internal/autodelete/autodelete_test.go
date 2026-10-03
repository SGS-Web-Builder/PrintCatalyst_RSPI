package autodelete

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// newTestRig sets up an isolated database and local-files root with
// the runtime migrations applied.
func newTestRig(t *testing.T) (files *localfiles.Files, sw *Sweeper) {
	t.Helper()
	dir := t.TempDir()
	store, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(f, store.DB())
	return f, New(store.DB(), docs, DefaultSweeperConfig())
}

// TestPurgeAfterConfirmedCompletion ensures a dispatched document is removed
// on the next sweep call.
func TestPurgeAfterConfirmedCompletion(t *testing.T) {
	f, sw := newTestRig(t)
	dir := f.DataRoot()
	if err := os.MkdirAll(filepath.Join(dir, "documents", "order-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dir, "documents", "order-1", "doc.pdf")
	if err := os.WriteFile(blob, []byte("PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().Unix()
	db := sw.db
	if _, err := db.ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, submitted_at)
VALUES ('order-1', 'tok', 'completed', 'INR', 2, 100, 'C', '+91', '', '', ?, ?, ?)`,
		now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until, dispatched_at)
VALUES ('doc-1', 'order-1', 'doc.pdf', 'application/pdf', 3, 1, 'h', 'documents/order-1/doc.pdf', ?, ?, ?)`,
		now, now+3600, now-3601); err != nil {
		t.Fatal(err)
	}
	n, err := sw.PurgeExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Errorf("blob still exists after purge")
	}
}

// TestPurgeAfterRetentionExpires covers the fallback path where the
// retention window has passed and the order never transitioned to
// paid (e.g. customer abandoned the upload flow).
func TestPurgeAfterRetentionExpires(t *testing.T) {
	f, sw := newTestRig(t)
	dir := f.DataRoot()
	if err := os.MkdirAll(filepath.Join(dir, "documents", "order-stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dir, "documents", "order-stale", "doc.pdf")
	if err := os.WriteFile(blob, []byte("PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().Unix()
	db := sw.db
	if _, err := db.ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, submitted_at)
VALUES ('order-stale', 'tok', 'pending_payment', 'INR', 2, 100, 'C', '+91', '', '', ?, ?, 0)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES ('doc-stale', 'order-stale', 'doc.pdf', 'application/pdf', 3, 1, 'h', 'documents/order-stale/doc.pdf', ?, ?)`,
		now-86401, now+86400); err != nil {
		t.Fatal(err)
	}
	n, err := sw.PurgeExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}
}

// TestPurgePreservesActiveOrder ensures documents attached to a
// still-active order are NOT purged even when the retention window
// has not expired.
func TestPurgePreservesActiveOrder(t *testing.T) {
	f, sw := newTestRig(t)
	dir := f.DataRoot()
	if err := os.MkdirAll(filepath.Join(dir, "documents", "order-active"), 0o755); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dir, "documents", "order-active", "doc.pdf")
	if err := os.WriteFile(blob, []byte("PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().Unix()
	db := sw.db
	if _, err := db.ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, submitted_at)
VALUES ('order-active', 'tok', 'pending_payment', 'INR', 2, 100, 'C', '+91', '', '', ?, ?, 0)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES ('doc-active', 'order-active', 'doc.pdf', 'application/pdf', 3, 1, 'h', 'documents/order-active/doc.pdf', ?, ?)`,
		now, now+86400); err != nil {
		t.Fatal(err)
	}
	n, err := sw.PurgeExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("purged = %d, want 0 (active order should be preserved)", n)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Errorf("active blob vanished")
	}
}

// TestPurgeOrderImmediately is the dispatcher's "purge on success"
// path. The whole order is wiped in a single call.
func TestPurgeOrderImmediately(t *testing.T) {
	f, sw := newTestRig(t)
	dir := f.DataRoot()
	if err := os.MkdirAll(filepath.Join(dir, "documents", "order-now"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.pdf", "b.pdf"} {
		blob := filepath.Join(dir, "documents", "order-now", name)
		if err := os.WriteFile(blob, []byte("PDF"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	now := time.Now().Unix()
	db := sw.db
	if _, err := db.ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, submitted_at)
VALUES ('order-now', 'tok', 'paid', 'INR', 2, 100, 'C', '+91', '', '', ?, ?, ?)`,
		now, now, now); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, 'order-now', ?, 'application/pdf', 3, 1, 'h', ?, ?, ?)`,
			"doc-"+name, name+".pdf", "documents/order-now/"+name+".pdf", now, now+86400); err != nil {
			t.Fatal(err)
		}
	}
	n, err := sw.PurgeOrder(ctx, "order-now")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("purged = %d, want 2", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "documents", "order-now", "a.pdf")); !os.IsNotExist(err) {
		t.Errorf("a.pdf still exists after purge")
	}
}

func TestRetentionPolicyKeepsRecordsAndNeverConfusesSpoolingWithCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, status, progress string
		age                    time.Duration
		purge                  bool
	}{
		{"unpaid completed", "pending_payment", "completed", time.Minute, true},
		{"spooled only", "dispatched", "printing", time.Hour, false},
		{"recent failed", "failed", "blocked", 23 * time.Hour, false},
		{"expired failed", "failed", "blocked", 25 * time.Hour, true},
		{"expired paid awaits pickup", "paid", "pending", 25 * time.Hour, false},
		{"expired pending", "pending_payment", "", 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, sw := newTestRig(t)
			ctx := context.Background()
			now := time.Now().Unix()
			_, err := sw.db.Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,customer_email,customer_notes,created_at,updated_at,submitted_at) VALUES('o','token',?,'INR',2,500,'Customer','','','',?,?,?)`, tc.status, now, now, now)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.DataRoot(), "documents", "o.pdf")
			if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte("PDF"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = sw.db.Exec(`INSERT INTO documents(id,order_id,original_filename,mime_type,size_bytes,page_count,sha256,storage_path,created_at,retention_until,dispatched_at) VALUES('d','o','o.pdf','application/pdf',3,1,'h','documents/o.pdf',?,?,?)`, now-int64(tc.age/time.Second), now+604800, now-100)
			if err != nil {
				t.Fatal(err)
			}
			_, err = sw.db.Exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,unit_price_minor,line_total_minor) VALUES('l','o','d','A4','A4','monochrome','one-sided',1,1,1,500,500)`)
			if err != nil {
				t.Fatal(err)
			}
			_, err = sw.db.Exec(`INSERT INTO print_submissions(line_id,state,queue_name,updated_at,progress) VALUES('l','submitted','Printer',?,?)`, now, tc.progress)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sw.db.Exec(`UPDATE business_settings SET auto_delete_enabled=0,auto_delete_minutes=43200`); err != nil {
				t.Fatal(err)
			}
			n, err := sw.PurgeExpired(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if (n == 1) != tc.purge {
				t.Fatalf("purged %d", n)
			}
			_, err = os.Stat(path)
			if os.IsNotExist(err) != tc.purge {
				t.Fatalf("file state %v", err)
			}
			var records int
			if err = sw.db.QueryRow(`SELECT COUNT(*) FROM orders o JOIN order_lines l ON l.order_id=o.id JOIN documents d ON d.id=l.document_id WHERE o.customer_name='Customer' AND o.total_minor=500`).Scan(&records); err != nil || records != 1 {
				t.Fatalf("records lost: %d %v", records, err)
			}
			n, err = sw.PurgeExpired(ctx)
			if err != nil || n != 0 {
				t.Fatalf("repeated purge %d %v", n, err)
			}
		})
	}
}
