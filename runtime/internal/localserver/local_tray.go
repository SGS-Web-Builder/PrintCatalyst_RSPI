package localserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
)

// registerTrayLocal wires the loopback-only endpoints the
// companion print-catalyst-on-premise-tray binary depends on.
// Every endpoint in this file shares three properties:
//
//   1. It listens on the same loopback port as the rest of the
//      runtime; nothing is exposed to off-machine peers.
//   2. It does NOT require owner authentication — the tray
//      launcher runs as the same Windows user as the merchant's
//      signed-in session, so a token would only get in the way.
//   3. It does NOT forward through the public origin guard —
//      the tray talks to localhost, so the same-origin check is
//      trivially satisfied.
//
// The endpoints are intentionally narrow: they exist to support
// the tray's documented UX (open the dashboard, print a test
// page, start/stop the service). Anything else belongs in the
// owner / portal surface.
func (s *Server) registerTrayLocal(mux *http.ServeMux) {
	// GET /api/v1/local/status — service + dashboard status snapshot.
	// The tray polls this every refreshTick so the menu labels
	// reflect the live runtime state without a Win32 dependency
	// in the tray binary. The endpoint is read-only and returns
	// a small JSON document; the tray never parses anything
	// beyond the documented fields.
	mux.HandleFunc("GET /api/v1/local/status", s.trayLocalGuard(func(w http.ResponseWriter, r *http.Request) {
		status := struct {
			DashboardReady bool   `json:"dashboardReady"`
			Bootstrap      bool   `json:"bootstrap"`
			InstallationID string `json:"installationId"`
		}{
			DashboardReady: s.notReadyReason(r.Context()) == "",
			Bootstrap:      s.bootstrap,
			InstallationID: s.installationID,
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(status)
	}))

	// POST /api/v1/local/test-print — submit a small test page to
	// the merchant's default printer. The tray calls this when
	// the operator selects "Print Test Page…" from the menu,
	// letting the operator verify the printer connection without
	// leaving the notification area.
	mux.HandleFunc("POST /api/v1/local/test-print", s.trayLocalGuard(func(w http.ResponseWriter, r *http.Request) {
		if s.dispatcher == nil {
			writeTrayError(w, "print dispatcher is not configured", http.StatusServiceUnavailable)
			return
		}
		// The Submit call can take a few seconds on a stalled
		// spooler; cap at 15 seconds so a hung printer cannot
		// keep the Win32 message pump waiting forever.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		result, err := s.dispatcher.TestPrint(ctx)
		if err != nil {
			switch {
			case errors.Is(err, dispatch.ErrTestPrintNoBackend):
				writeTrayError(w, "no printer backend is configured on this platform", http.StatusServiceUnavailable)
			case errors.Is(err, dispatch.ErrTestPrintNoQueue):
				writeTrayError(w, "no default printer is configured; set one through the dashboard first", http.StatusConflict)
			default:
				writeTrayError(w, err.Error(), http.StatusBadGateway)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"jobId": result.JobID,
			"queue": result.Queue,
		})
	}))
}

// trayLocalGuard enforces the loopback-only contract the tray
// endpoints share. The guard is intentionally minimal — there
// is no authentication, no same-origin check, and no CSRF
// token — because the only realistic caller is a process on the
// same Windows user session. A non-loopback peer is rejected
// outright so a compromised local web page cannot probe the
// tray endpoints through a fetch().
func (s *Server) trayLocalGuard(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !isLoopbackClient(r.RemoteAddr) {
			http.Error(w, `{"error":"loopback access only"}`, http.StatusForbidden)
			return
		}
		// Reject forwarded headers outright: a malicious proxy
		// on the same machine could otherwise re-write the
		// source. The runtime binds to loopback only, so any
		// forwarded header is unsolicited.
		for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			if strings.TrimSpace(r.Header.Get(header)) != "" {
				http.Error(w, `{"error":"forwarded requests are forbidden"}`, http.StatusForbidden)
				return
			}
		}
		fn(w, r)
	}
}

// writeTrayError is the JSON error helper for the tray endpoints.
// The shape is intentionally a single {"error": "..."} object
// because the tray surfaces the message verbatim in a balloon
// notification; any richer structure would force the tray to
// maintain a parser for a response it will never use.
func writeTrayError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
