package localserver

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/webnotify"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomerPushRequiresOrderTokenAndSameOrigin(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('order','correct','pending_payment','INR',2,500,'Test','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	push, err := webnotify.New(db.DB())
	if err != nil {
		t.Fatal(err)
	}
	s := New("", "test")
	s.db = db.DB()
	s.customerPush = push
	mux := http.NewServeMux()
	s.registerCustomerPush(mux)
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"endpoint": "https://fcm.googleapis.com/fcm/send/test", "keys": map[string]string{"auth": base64.RawURLEncoding.EncodeToString(make([]byte, 16)), "p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}})
	for _, tc := range []struct {
		method, token, origin string
		want                  int
	}{
		{"PUT", "", "http://localhost:8080", 404}, {"PUT", "wrong", "http://localhost:8080", 404}, {"PUT", "correct", "https://evil.example", 403}, {"PUT", "correct", "http://localhost:8080", 200}, {"DELETE", "wrong", "http://localhost:8080", 404}, {"DELETE", "correct", "http://localhost:8080", 200},
	} {
		r := httptest.NewRequest(tc.method, "http://localhost:8080/api/v1/portal/orders/order/notifications", strings.NewReader(string(raw)))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Order-Token", tc.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}
