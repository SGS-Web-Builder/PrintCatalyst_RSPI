//go:build windows

package localserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func TestSoftwareLicenseBlocksNewWorkButAllowsHistoryAndReconciliation(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gate, err := licensegate.New(db.DB(), "test", "https://licenses.example", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{licenseGate: gate}
	h := s.enforceSoftwareLicense(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/portal/uploads", "", 402},
		{"POST", "/api/v1/orders", "", 402},
		{"POST", "/api/v1/owner/orders/one/print", "", 402},
		{"POST", "/api/v1/owner/payments/intents", "", 402},
		{"POST", "/api/v1/owner/orders/one/status", `{"status":"paid"}`, 402},
		{"POST", "/api/v1/owner/orders/one/status", `{"status":"print_completed"}`, 204},
		{"GET", "/api/v1/owner/orders", "", 204},
		{"POST", "/api/v1/owner/licence/activate", "", 204},
		{"POST", "/api/v1/payments/webhook", "", 204},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Errorf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}

func TestProductionMissingLicenseGateBlocksWork(t *testing.T) {
	if !licensegate.Required {
		t.Skip("production policy")
	}
	s := &Server{}
	h := s.enforceSoftwareLicense(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, path := range []string{"/api/v1/portal/uploads", "/api/v1/owner/orders/a/print", "/api/v1/owner/id-cards", "/api/v1/owner/passports"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 402 {
			t.Fatalf("missing gate accepted %s: %d", path, w.Code)
		}
	}
}
