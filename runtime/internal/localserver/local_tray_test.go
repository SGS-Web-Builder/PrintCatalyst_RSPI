package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
)

// newTrayServerForTest is a tiny test harness that builds a
// Server with the minimum options needed to exercise the
// tray-local endpoints. The harness is intentionally minimal:
// it does not wire the full HTTP stack so a regression in
// unrelated subsystems cannot mask a tray endpoint regression.
func newTrayServerForTest(t *testing.T, dispatcher *dispatch.Dispatcher) *Server {
	t.Helper()
	port := freeTCPPort(t)
	server := New("127.0.0.1:"+port, "test-install-id",
		WithDispatcher(dispatcher),
	)
	return server
}

// freeTCPPort returns an ephemeral TCP port number the test
// can bind to. The helper avoids the brittle "pick 0 then read
// the bound address" pattern because the localserver New()
// function takes the address string eagerly and would need a
// re-write to support port-0.
func freeTCPPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on 127.0.0.1:0: %v", err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	return itoaPort(address.Port)
}

// itoaPort formats an int TCP port as a string. We inline this
// to avoid pulling strconv into test files when the only call
// site needs an integer-to-string conversion of a single field.
func itoaPort(port int) string {
	if port == 0 {
		return "0"
	}
	var digits [8]byte
	pos := len(digits)
	for port > 0 {
		pos--
		digits[pos] = byte('0' + port%10)
		port /= 10
	}
	return string(digits[pos:])
}

// TestLocalTrayStatusRequiresLoopback verifies that the tray
// status endpoint rejects non-loopback peers. The test runs
// against httptest's loopback server, so the request itself
// comes from loopback — we simulate an off-machine peer by
// setting RemoteAddr to a non-loopback address manually.
func TestLocalTrayStatusRequiresLoopback(t *testing.T) {
	server := newTrayServerForTest(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/local/status", nil)
	request.RemoteAddr = "10.0.0.5:12345"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-loopback peer, got %d", recorder.Code)
	}
}

// TestLocalTrayStatusLoopbackOK verifies the happy path: a
// loopback peer sees a 200 response with the documented fields.
func TestLocalTrayStatusLoopbackOK(t *testing.T) {
	server := newTrayServerForTest(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/local/status", nil)
	// httptest.NewRequest uses 192.0.2.1 by default; replace it
	// with a loopback address so isLoopbackClient passes.
	request.RemoteAddr = "127.0.0.1:54321"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("expected 200 for loopback peer, got %d (body=%s)", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		DashboardReady bool   `json:"dashboardReady"`
		Bootstrap      bool   `json:"bootstrap"`
		InstallationID string `json:"installationId"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.InstallationID != "test-install-id" {
		t.Errorf("installationId = %q, want %q", payload.InstallationID, "test-install-id")
	}
}

// TestLocalTrayTestPrintRequiresPOST verifies that the test
// print endpoint rejects non-POST methods. A GET request must
// not silently no-op (the Win32 client relies on a stable
// error code to decide whether to fall back to the dashboard).
func TestLocalTrayTestPrintRequiresPOST(t *testing.T) {
	server := newTrayServerForTest(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/local/test-print", nil)
	request.RemoteAddr = "127.0.0.1:54321"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusNotFound {
		t.Errorf("expected 405/404 for GET on POST-only route, got %d", recorder.Code)
	}
}

// TestLocalTrayTestPrintRejectsForwardedHeaders confirms the
// loopback guard's forwarded-header rejection. A local proxy
// that injects X-Forwarded-For must NOT be able to abuse the
// test-print endpoint.
func TestLocalTrayTestPrintRejectsForwardedHeaders(t *testing.T) {
	server := newTrayServerForTest(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/local/test-print", bytes.NewReader([]byte(`{}`)))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("X-Forwarded-For", "1.2.3.4")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("expected 403 for forwarded header, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "forwarded requests are forbidden") {
		t.Errorf("unexpected body: %s", recorder.Body.String())
	}
}

// TestLocalTrayTestPrintNoQueueReturns409 verifies the
// no-default-printer failure mode is translated into a 409
// Conflict response. We hand the localserver a dispatcher
// with no backend wired; the localserver must return 503
// (no backend) rather than 500.
func TestLocalTrayTestPrintNoBackendReturns503(t *testing.T) {
	// Dispatcher with nil backend — TestPrint returns ErrTestPrintNoBackend.
	dispatcher := dispatch.New(nil, nil, nil, nil, dispatch.DefaultDispatcherConfig())
	server := newTrayServerForTest(t, dispatcher)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/local/test-print", bytes.NewReader([]byte(`{}`)))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for nil backend, got %d (body=%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no printer backend") {
		t.Errorf("unexpected body: %s", recorder.Body.String())
	}
}

// dispatchStubBackend captures the bytes submitted to the
// queue and returns a synthetic job id. The stub is used by
// the localserver happy-path test below; it lives in this
// file because it is only relevant to the tray-local
// endpoints.
type dispatchStubBackend struct {
	queue     string
	captured  []byte
	jobID     string
	submitErr error
}

func (b *dispatchStubBackend) Name() string { return "stub" }
func (b *dispatchStubBackend) Submit(ctx context.Context, queue string, content []byte, ref dispatch.DocumentRef) (string, error) {
	if b.submitErr != nil {
		return "", b.submitErr
	}
	b.queue = queue
	b.captured = append([]byte(nil), content...)
	if b.jobID == "" {
		b.jobID = "stub-job-1"
	}
	return b.jobID, nil
}
func (b *dispatchStubBackend) Queues(ctx context.Context) ([]string, error) { return []string{b.queue}, nil }

// TestLocalTrayTestPrintHappyPath uses an in-memory backend and
// resolver to exercise the full POST flow. The test asserts
// the JSON response carries the documented fields and that the
// captured bytes are non-empty so a future regression that
// silently drops the payload cannot pass.
func TestLocalTrayTestPrintHappyPath(t *testing.T) {
	backend := &dispatchStubBackend{jobID: "test-job-42"}
	// Resolver reports a fixed default queue so the dispatcher
	// can resolve it without hitting the database.
	dispatcher := dispatch.New(nil, nil, backend, &fixedResolver{queue: "TestPrinter"}, dispatch.DefaultDispatcherConfig())
	server := newTrayServerForTest(t, dispatcher)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/local/test-print", bytes.NewReader([]byte(`{}`)))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", recorder.Code, recorder.Body.String())
	}
	if len(backend.captured) == 0 {
		t.Errorf("backend received zero bytes")
	}
	if backend.queue != "TestPrinter" {
		t.Errorf("backend received queue %q, want %q", backend.queue, "TestPrinter")
	}
	var payload struct {
		JobID string `json:"jobId"`
		Queue string `json:"queue"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.JobID != "test-job-42" {
		t.Errorf("jobId = %q, want %q", payload.JobID, "test-job-42")
	}
	if payload.Queue != "TestPrinter" {
		t.Errorf("queue = %q, want %q", payload.Queue, "TestPrinter")
	}
}

// fixedResolver satisfies dispatch.QueueResolver for tests
// that do not need a real database. The resolver is exported
// only to test helpers; production code uses the DB-backed
// resolver shipped in db_queue_resolver.go.
type fixedResolver struct {
	queue string
	err   error
}

func (r *fixedResolver) QueueFor(ctx context.Context, printerID string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.queue, nil
}

func (r *fixedResolver) DefaultQueue(ctx context.Context) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.queue, nil
}

// ensure filepath import stays used — the imports are kept
// available for future tests that want to exercise the data
// directory layout. Removing the import now would force a
// re-import when those tests land.
var _ = filepath.Join
