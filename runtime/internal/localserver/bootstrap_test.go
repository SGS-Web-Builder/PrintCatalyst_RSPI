package localserver

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// bootstrapServer wires the bare minimum server in bootstrap mode —
// no provisioning gates satisfied, no owner, no licence, and the
// installation id is the installer placeholder. The test then
// exercises the protected setup endpoints and asserts that every
// other public-facing surface returns a clear 503 instead of
// silently accepting traffic with placeholder values.
func bootstrapServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap.sqlite")
	dataRoot := t.TempDir()
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	files, err := localfiles.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(listener.Addr().String(), "replace-before-first-start",
		WithStoreHealth(database),
		WithProvisioning(provisioning.New(database.DB())),
		WithOwner(owner.New(database.DB()), "test-bootstrap-token"),
		WithPricing(pricing.New(database.DB())),
		WithOrders(orders.New(database.DB(), pricing.New(database.DB()))),
		WithNotifications(notifications.New(database.DB(), nil)),
		WithDB(database.DB()),
		WithFiles(files),
		WithBootstrap(true),
	)
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.Handler()}}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, database
}

// TestBootstrapGatesPublicIntake asserts the brief's "Keep public
// uploads, payment and printing blocked until the required gates are
// satisfied" rule. With every provisioning gate still open, the
// portal endpoints must return 503 rather than silently accepting
// customer uploads.
func TestBootstrapGatesPublicIntake(t *testing.T) {
	ts, _ := bootstrapServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/portal/options")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/portal/options status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "unconfigured") {
		t.Fatalf("GET /api/v1/portal/options body = %s, want a clear bootstrap message", body)
	}
}

// TestBootstrapReadyz returns 503 so a load balancer / monitoring
// probe refuses to send customer traffic to a fresh installation.
func TestBootstrapReadyzReturns503(t *testing.T) {
	ts, _ := bootstrapServer(t)
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz status = %d, want 503", resp.StatusCode)
	}
}

// TestBootstrapHealthzStillReachable ensures the health endpoint
// stays 200 in bootstrap mode so an operator can probe a freshly
// installed service and see it is running, even though public
// traffic is rejected. The /healthz payload exposes the
// installation id so the operator can copy it into their setup
// wizard.
func TestBootstrapHealthzStillReachable(t *testing.T) {
	ts, _ := bootstrapServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		InstallationID string `json:"installationId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode /healthz: %v body=%s", err, body)
	}
	if payload.InstallationID == "" {
		t.Fatalf("installationId missing from /healthz: %s", body)
	}
}

// TestBootstrapSetupStatusStillReachable locks in the brief's
// "Allow only protected local setup before configuration is complete"
// rule. The setup endpoint remains reachable so the operator can
// read what is missing without a circular dependency.
func TestBootstrapSetupStatusStillReachable(t *testing.T) {
	ts, _ := bootstrapServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/setup/status", nil)
	req.Header.Set("Origin", "http://"+ts.Listener.Addr().String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/setup/status status = %d, want 200", resp.StatusCode)
	}
}

// TestBootstrapNotReadyReasonIsStable locks the contract used by
// both the /readyz and portal endpoints — they both rely on
// notReadyReason returning a non-empty string in bootstrap mode.
func TestBootstrapNotReadyReasonIsStable(t *testing.T) {
	ts, _ := bootstrapServer(t)
	url, err := neturlParse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Make a POST request that goes through portalGuard + portalReady.
	body := strings.NewReader(`{"lines":[]}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/quote", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", url)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/v1/portal/quote status = %d, want 503", resp.StatusCode)
	}
}