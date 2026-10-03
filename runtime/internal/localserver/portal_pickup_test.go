package localserver

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortalPickupRequiresMatchingOrderSecret(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('one','secret-one','paid','INR',2,500,'Customer','',1,1),('two','secret-two','paid','INR',2,500,'Other','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pickup.New(db.DB(), []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	code, err := p.IssueVerified(ctx, "one", "verified-test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db.DB(), pickup: p}
	mux := http.NewServeMux()
	s.registerPortalPickup(mux)
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/api/v1/portal/orders/one/pickup", "", 404},
		{"/api/v1/portal/orders/one/pickup?token=secret-one", "", 404},
		{"/api/v1/portal/orders/one/pickup", "secret-two", 404},
		{"/api/v1/portal/orders/missing/pickup", "secret-one", 404},
		{"/api/v1/portal/orders/one/pickup", "secret-one", 200},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.Header.Set("X-Order-Token", tc.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("sensitive response cached")
		}
		if tc.status == 200 {
			var view pickup.View
			if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.Code != code {
				t.Fatal("wrong recovery", err)
			}
		} else if strings.Contains(w.Body.String(), code) {
			t.Fatal("code leaked")
		}
	}
}
