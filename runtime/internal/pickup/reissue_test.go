package pickup

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReissueExpiredCannotReleaseOrReplay(t *testing.T) {
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
	old, err := s.IssueVerified(ctx, "one", "verified-test")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkPrepared(ctx, "one", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(25 * time.Hour)
	s.now = func() time.Time { return future }
	if err = s.ReissueExpired(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	view, err := s.ViewForOrder(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if view.Code == old || view.Code == "" || view.Preparation != "ready" {
		t.Fatal("replacement lost ready state or code", view.State)
	}
	if err = s.ReissueExpired(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	again, err := s.ViewForOrder(ctx, "one")
	if err != nil || again.Code != view.Code {
		t.Fatal("retry rotated code", err)
	}
	if _, err = s.Claim(ctx, old); !errors.Is(err, ErrUnavailable) {
		t.Fatal("old code accepted", err)
	}
	var releases int
	if err = db.DB().QueryRow(`SELECT COUNT(*) FROM kiosk_releases`).Scan(&releases); err != nil || releases != 0 {
		t.Fatal("reissue released job", err)
	}
	if _, err = s.Claim(ctx, view.Code); err != nil {
		t.Fatal(err)
	}
	if err = s.ReissueExpired(ctx, "one"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("claimed order reissued", err)
	}
	if err = s.ReissueExpired(ctx, "missing"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing order reissued", err)
	}
}
