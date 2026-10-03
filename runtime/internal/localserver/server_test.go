package localserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// net_listenForTest binds an ephemeral loopback listener the test
// can use to occupy a port so a follow-up bind to the same address
// fails. Splitting the helper out keeps the bind-failure test below
// readable.
func net_listenForTest(t *testing.T) (net.Listener, error) {
	t.Helper()
	return net.Listen("tcp", "127.0.0.1:0")
}

// recordingDiagnostic is a minimal eventlog.Writer that captures every
// line so a regression test can assert that the localserver emits the
// startup banner when the listener binds successfully.
type recordingDiagnostic struct {
	mu     sync.Mutex
	levels []string
	lines  []string
}

func (r *recordingDiagnostic) Info(message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels = append(r.levels, "INFO")
	r.lines = append(r.lines, message)
	return nil
}

func (r *recordingDiagnostic) Warning(message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels = append(r.levels, "WARN")
	r.lines = append(r.lines, message)
	return nil
}

func (r *recordingDiagnostic) Error(message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels = append(r.levels, "ERROR")
	r.lines = append(r.lines, message)
	return nil
}

func (r *recordingDiagnostic) Close() error { return nil }

func (r *recordingDiagnostic) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

func TestStartupLogsListenAddress(t *testing.T) {
	// The service handler on Windows and the supervisor on every
	// platform both rely on Start emitting a clear "listening on"
	// message — otherwise an operator with an MSI that successfully
	// installed has no documented place to read the dashboard URL.
	recorder := &recordingDiagnostic{}
	server := New("127.0.0.1:0", "installation-123", WithEventLog(recorder))
	t.Cleanup(func() {
		if err := server.Stop(context.Background()); err != nil {
			t.Fatalf("stop: %v", err)
		}
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	lines := recorder.snapshot()
	found := false
	for _, line := range lines {
		if strings.Contains(line, "local server listening on http://") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'local server listening on http://' banner, got %v", lines)
	}
}

func TestStartupLogsBindFailure(t *testing.T) {
	// Bind a listener first to claim the port, then start the
	// server on the same address. The error path must emit an
	// ERROR-level diagnostic so the Windows Event Log shows the
	// occupied-port failure instead of failing silently.
	first, err := net_listenForTest(t)
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	recorder := &recordingDiagnostic{}
	server := New(first.Addr().String(), "installation-123", WithEventLog(recorder))
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("expected start to fail on an occupied port")
	}
	found := false
	for _, line := range recorder.snapshot() {
		if strings.Contains(line, "failed to bind") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected bind-failure diagnostic, got %v", recorder.snapshot())
	}
}

func TestSetupAssetsHaveBrowserSecurityHeaders(t *testing.T) {
	server := New("127.0.0.1:8080", "installation-123")
	for _, path := range []string{"/", "/setup.js", "/client.mjs", "/setup.css"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("asset %s: status %d, headers %v", path, response.Code, response.Header())
		}
	}
}

func TestHealthHandlerReportsInstallationReadiness(t *testing.T) {
	server := New("127.0.0.1:0", "installation-123")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["installationId"] != "installation-123" {
		t.Fatalf("body = %#v", body)
	}
}

func TestReadinessStaysClosedUntilProvisioningCompletes(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "print-catalyst.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	setup := provisioning.New(database.DB())
	server := New("127.0.0.1:0", "installation-123", WithStoreHealth(database), WithProvisioning(setup))

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}

	statusResponse := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	statusRequest.RemoteAddr = "127.0.0.1:54321"
	server.Handler().ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("setup status = %d", statusResponse.Code)
	}
	var status provisioning.StatusResult
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Next != provisioning.GateLicence || status.ProductionReady {
		t.Fatalf("setup status = %#v", status)
	}

	for _, gate := range provisioning.OrderedGates {
		if err := setup.CompleteGate(context.Background(), gate, "verified"); err != nil {
			t.Fatal(err)
		}
	}
	readyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(readyResponse, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", readyResponse.Code)
	}
}

func TestSetupStatusRejectsNonLoopbackClients(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "print-catalyst.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := New("127.0.0.1:0", "installation-123", WithProvisioning(provisioning.New(database.DB())))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	request.RemoteAddr = "203.0.113.20:443"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
}

func TestServerImplementsSupervisorLifecycle(t *testing.T) {
	server := New("127.0.0.1:0", "installation-123")
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteProvisioningDoesNotExposeOrderIntakeAfterRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "print-catalyst.sqlite")
	first, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	setup := provisioning.New(first.DB())
	if err := setup.CompleteGate(context.Background(), provisioning.GateLicence, "licence verified"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	restartedSetup := provisioning.New(second.DB())
	server := New("127.0.0.1:0", "installation-123", WithStoreHealth(second), WithProvisioning(restartedSetup))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
	request.RemoteAddr = "198.51.100.20:443"
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("order intake status = %d, want 503", response.Code)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("order intake did not advertise Retry-After")
	}
}

func TestDatabaseFailureClosesReadinessAndOrderIntake(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "print-catalyst.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	setup := provisioning.New(database.DB())
	server := New("127.0.0.1:0", "installation-123", WithStoreHealth(database), WithProvisioning(setup))
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/readyz", "/api/v1/orders"} {
		response := httptest.NewRecorder()
		method := http.MethodGet
		if path == "/api/v1/orders" {
			method = http.MethodPost
		}
		server.Handler().ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", path, response.Code)
		}
	}
}

func TestDatabaseOpenFailureDoesNotCreateUsableRuntime(t *testing.T) {
	directory := t.TempDir()
	blockedPath := filepath.Join(directory, "database-is-a-directory")
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if database, err := store.Open(context.Background(), blockedPath); err == nil {
		database.Close()
		t.Fatal("store.Open() succeeded with a directory as the database path")
	}
}
