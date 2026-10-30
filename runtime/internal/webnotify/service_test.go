package webnotify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	webpush "github.com/SherClockHolmes/webpush-go"
	"path/filepath"
	"testing"
	"time"
)

func testSubscription(t *testing.T) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return webpush.Subscription{Endpoint: "https://fcm.googleapis.com/fcm/send/test", Keys: webpush.Keys{Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}}
}
func TestPushValidation(t *testing.T) {
	sub := testSubscription(t)
	if err := Validate(sub); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://fcm.googleapis.com/fcm/send/test", "https://127.0.0.1/fcm/send/test", "https://fcm.googleapis.com.evil.test/fcm/send/test", "https://user@fcm.googleapis.com/fcm/send/test", "https://fcm.googleapis.com:444/fcm/send/test"} {
		s := sub
		s.Endpoint = endpoint
		if Validate(s) == nil {
			t.Fatal("accepted", endpoint)
		}
	}
	sub.Keys.Auth = "bad"
	if Validate(sub) == nil {
		t.Fatal("invalid key accepted")
	}
}
func TestPushOnlyVerifiedActivePaidAndRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db.DB())
	if err != nil {
		t.Fatal(err)
	}
	again, err := New(db.DB())
	if err != nil || again.PublicKey() != s.PublicKey() {
		t.Fatal("VAPID key changed", err)
	}
	now := time.Now().Unix()
	for _, id := range []string{"pending", "unverified", "expired", "paid"} {
		status := "paid"
		if id == "pending" {
			status = "pending_payment"
		}
		_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES(?,?,?,'INR',2,500,'Test','',1,1)`, id, "secret-"+id, status)
		if err != nil {
			t.Fatal(err)
		}
		if id != "pending" {
			ref := "verified"
			if id == "unverified" {
				ref = ""
			}
			expiry := now + 3600
			if id == "expired" {
				expiry = now - 1
			}
			_, err = db.DB().Exec(`INSERT INTO kiosk_pickups(order_id,payment_reference,code_hash,code_cipher,state,expires_at,created_at) VALUES(?,?,?,?,'active',?,?)`, id, ref, []byte(id), []byte("cipher"), expiry, now)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = s.Subscribe(ctx, id, testSubscription(t)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	response := 503
	s.send = func(ctx context.Context, body []byte, sub *webpush.Subscription, ttl int) (int, error) {
		calls++
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["orderId"] != "paid" || payload["token"] != "secret-paid" || payload["code"] != nil {
			t.Fatal("unsafe payload", payload)
		}
		if ttl <= 0 || ttl > 300 {
			t.Fatal("TTL", ttl)
		}
		return response, nil
	}
	if err = s.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("calls", calls)
	}
	if err = s.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("retry not delayed")
	}
	if _, err = db.DB().Exec("UPDATE webpush_orders SET next_attempt=0 WHERE order_id='paid'"); err != nil {
		t.Fatal(err)
	}
	response = 201
	if err = s.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("retry missing")
	}
	if err = s.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("duplicate send")
	}
	if err = s.Unsubscribe(ctx, "paid"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.DB().QueryRow("SELECT COUNT(*) FROM webpush_orders WHERE order_id='paid'").Scan(&n); err != nil || n != 0 {
		t.Fatal("unsubscribe", n, err)
	}
}
