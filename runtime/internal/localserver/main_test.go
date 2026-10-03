package localserver

import (
	"net/http"
	"os"
	"testing"
)

// TestMain runs all tests in this package. After m.Run() it closes
// idle HTTP connections held by http.DefaultClient so that readLoop
// goroutines spawned by the many http.DefaultClient.Do calls across
// individual test files do not bleed into the test binary's final
// goroutine-leak scan.
func TestMain(m *testing.M) {
	code := m.Run()
	// Drain the default transport's idle connection pool so that
	// readLoop goroutines exit before the test binary's goroutine
	// leak check runs.
	if t := http.DefaultTransport; t != nil {
		if tr, ok := t.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
	os.Exit(code)
}
