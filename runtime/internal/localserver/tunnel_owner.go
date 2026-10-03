package localserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// probeTimeoutBudget caps a single owner-initiated verify probe so a stuck
// tunnel cannot lock the dashboard. The probe itself is bounded to 5s in the
// tunnel package; this ceiling is the HTTP-side budget.
const probeTimeoutBudget = 15 * time.Second

// registerOwnerTunnel wires the Phase 7 tunnel, custom domain and branded QR
// endpoints. The surface is owner-only: tunnel configuration is a merchant-
// wide decision and operators have no business changing the public origin
// or the tunnel token. The HTTP layer reads the configured snapshot, never
// the tunnel token; only the dashboard form posts the token, and the service
// stores a fingerprint only.
//
// Endpoints:
//
//	GET    /api/v1/owner/tunnel               — current snapshot
//	PUT    /api/v1/owner/tunnel               — save configuration
//	POST   /api/v1/owner/tunnel/verify        — actively probe the public origin
//	POST   /api/v1/owner/tunnel/disconnect    — mark the tunnel offline
//	GET    /api/v1/owner/tunnel/qr.svg        — render the branded QR (200)
//	GET    /api/v1/owner/tunnel/events        — recent tunnel_events rows
//
// When no tunnel.Service is wired (a build that disables the public surface),
// every endpoint returns 503 so the dashboard can render a clear "not
// available in this build" message instead of a 404.
func (s *Server) registerOwnerTunnel(mux *http.ServeMux) {
	protect := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.localOwnerRequest(w, r) {
				fn(w, r)
			}
		}
	}
	mux.HandleFunc("GET /api/v1/owner/tunnel", protect(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.ownerSession(w, r); !ok {
			return
		}
		s.tunnelSnapshotResponse(w, r)
	}))
	mux.HandleFunc("PUT /api/v1/owner/tunnel", protect(func(w http.ResponseWriter, r *http.Request) {
		_, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		if !requireOwner(w, subject) {
			return
		}
		if s.tunnel == nil {
			http.Error(w, "tunnel service is not enabled in this build", http.StatusServiceUnavailable)
			return
		}
		var cfg tunnel.Config
		if !decodeOwnerJSON(w, r, &cfg, 4<<10) {
			return
		}
		snap, err := s.tunnel.SaveConfig(r.Context(), cfg)
		if err != nil {
			tunnelError(w, err)
			return
		}
		if snap.Provider == tunnel.ProviderDirect && s.configReload != nil {
			if err := s.saveDirectOrigin(r.Context(), snap.PublicOrigin); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		writeJSON(w, snap)
	}))
	mux.HandleFunc("POST /api/v1/owner/tunnel/verify", protect(func(w http.ResponseWriter, r *http.Request) {
		_, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		if !requireOwner(w, subject) {
			return
		}
		if s.tunnel == nil {
			http.Error(w, "tunnel service is not enabled in this build", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), probeTimeoutBudget)
		defer cancel()
		snap, _, err := s.tunnel.ProbeAndRecord(ctx, tunnel.WithInstallation(s.connectionInstallationID()))
		if err != nil {
			tunnelError(w, err)
			return
		}
		if snap.Status == tunnel.StatusOnline && s.configReload != nil {
			if err := s.saveDirectOrigin(ctx, snap.PublicOrigin); err != nil {
				http.Error(w, "Domain verified but runtime configuration could not be applied: "+err.Error(), 500)
				return
			}
		}
		writeJSON(w, snap)
	}))
	mux.HandleFunc("POST /api/v1/owner/tunnel/disconnect", protect(func(w http.ResponseWriter, r *http.Request) {
		_, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		if !requireOwner(w, subject) {
			return
		}
		if s.tunnel == nil {
			http.Error(w, "tunnel service is not enabled in this build", http.StatusServiceUnavailable)
			return
		}
		snap, err := s.tunnel.SetStatus(r.Context(), tunnel.StatusOffline, "tunnel was disconnected by the owner")
		if err != nil {
			tunnelError(w, err)
			return
		}
		writeJSON(w, snap)
	}))
	mux.HandleFunc("GET /api/v1/owner/tunnel/qr.svg", protect(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.ownerSession(w, r); !ok {
			return
		}
		if s.tunnel == nil {
			http.Error(w, "tunnel service is not enabled in this build", http.StatusServiceUnavailable)
			return
		}
		snap, err := s.tunnel.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if snap.QRTargetURL == "" {
			http.Error(w, "public origin is not configured", http.StatusConflict)
			return
		}
		if snap.Provider != tunnel.ProviderDirect && snap.Status != tunnel.StatusOnline {
			http.Error(w, "tunnel is not verified online; the QR will be generated after the public origin responds successfully", http.StatusConflict)
			return
		}
		svg, err := s.themedQR(r.Context(), snap.QRTargetURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(svg))
	}))
	mux.HandleFunc("GET /api/v1/owner/tunnel/events", protect(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.ownerSession(w, r); !ok {
			return
		}
		if s.tunnel == nil {
			http.Error(w, "tunnel service is not enabled in this build", http.StatusServiceUnavailable)
			return
		}
		events, err := s.tunnel.Events(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"events": events})
	}))
}

// tunnelSnapshotResponse is the GET /api/v1/owner/tunnel handler body.
// It is broken out so the wiring stays readable.
func (s *Server) tunnelSnapshotResponse(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		http.Error(w, "tunnel service is not enabled in this build", http.StatusServiceUnavailable)
		return
	}
	snap, err := s.tunnel.Snapshot(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snap)
}

// requireOwner is a tiny helper that turns a non-owner subject into a 403.
// Operators do not get to change tunnel configuration or probe the public
// origin.
func requireOwner(w http.ResponseWriter, subject owner.Subject) bool {
	if subject.Role != owner.RoleOwner {
		http.Error(w, "owner role is required", http.StatusForbidden)
		return false
	}
	return true
}

// writeJSON encodes v as JSON with no-store caching. It writes the body
// even on an encoding error so partial responses are at least visible.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// tunnelError maps tunnel sentinel errors to HTTP status codes so the
// dashboard renders a clear, machine-readable message instead of a stack
// trace. The error string is forwarded to the body.
func tunnelError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, tunnel.ErrUnconfigured), errors.Is(err, tunnel.ErrNoPublic), errors.Is(err, tunnel.ErrNoToken):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}
