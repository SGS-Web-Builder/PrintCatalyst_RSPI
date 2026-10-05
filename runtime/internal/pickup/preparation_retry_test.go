package pickup

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparationRetryKeepsPickupGate(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db.DB(), []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('one','secret','paid','INR',2,500,'Test','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueVerified(ctx, "one", "verified-test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`INSERT INTO kiosk_preparations(order_id,plan_json,state,updated_at,error) VALUES('one','[]','failed',1,'error')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RetryPreparation(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, code); !errors.Is(err, ErrPreparing) {
		t.Fatal("retry bypassed preparation", err)
	}
	if err = s.RetryPreparation(ctx, "one"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("nonfailed retried", err)
	}
	if err = s.MarkPrepared(ctx, "one", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, code); err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`UPDATE kiosk_preparations SET state='failed' WHERE order_id='one'`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RetryPreparation(ctx, "one"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("claimed job retried", err)
	}
}
