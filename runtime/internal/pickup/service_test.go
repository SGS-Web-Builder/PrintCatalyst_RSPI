package pickup

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPickupLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := []byte(strings.Repeat("a", 32))
	lookup := []byte(strings.Repeat("b", 32))
	s, err := New(db.DB(), key, lookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"paid", "unpaid", "expired"} {
		status := "paid"
		if id == "unpaid" {
			status = "pending_payment"
		}
		_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES(?,?,?,'INR',2,500,'Test','',1,1)`, id, id, status)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.IssueVerified(ctx, "unpaid", "gateway-payment"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unpaid: %v", err)
	}
	code, err := s.IssueVerified(ctx, "paid", "gateway-payment")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.IssueVerified(ctx, "paid", "gateway-payment")
	if err != nil || again != code {
		t.Fatal("duplicate changed code", err)
	}
	if _, err = s.Claim(ctx, code); !errors.Is(err, ErrPreparing) {
		t.Fatalf("unprepared claim: %v", err)
	}
	if err = s.MarkPrepared(ctx, "paid", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	// Model a service restart before entry using the same protected keys/database.
	s, err = New(db.DB(), key, lookup)
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, e := s.Claim(ctx, code)
			if e == nil {
				if id != "paid" {
					t.Error("wrong order")
				}
				wins.Add(1)
			} else if !errors.Is(e, ErrUnavailable) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims=%d", wins.Load())
	}
	var count int
	if err = db.DB().QueryRow("SELECT COUNT(*) FROM kiosk_releases").Scan(&count); err != nil || count != 1 {
		t.Fatal("release not durable", count, err)
	}
	expired, err := s.IssueVerified(ctx, "expired", "gateway-2")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, err = s.Claim(ctx, expired); !errors.Is(err, ErrUnavailable) {
		t.Fatal("expired accepted", err)
	}
	var status string
	db.DB().QueryRow("SELECT status FROM orders WHERE id='expired'").Scan(&status)
	if status != "paid" {
		t.Fatal("expiry changed payment")
	}
	for _, invalid := range []string{"123", "12345", "12x4", "１２３４"} {
		if _, err = s.Claim(ctx, invalid); !errors.Is(err, ErrUnavailable) {
			t.Fatal("bad format accepted")
		}
	}
}

func TestEncryptedCodesAndLeadingZeros(t *testing.T) {
	s, err := New(nil, []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, s.aead.NonceSize())
	sealed := s.aead.Seal(nonce, nonce, []byte("0007"), []byte("order"))
	code, err := s.decrypt("order", sealed)
	if err != nil || code != "0007" {
		t.Fatal(code, err)
	}
	if _, err = s.decrypt("other-order", sealed); err == nil {
		t.Fatal("ciphertext portable across orders")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = s.decrypt("order", sealed); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
