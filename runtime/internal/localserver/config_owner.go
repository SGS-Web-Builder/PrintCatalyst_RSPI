package localserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/configparser"
)

// registerOwnerConfig wires the owner-only configuration endpoints.
// The reload endpoint reads config.env, validates every required
// key and swaps the public-origin / bootstrap flags on the running
// server. It does not move the database file or restart the
// listener; the runtime is bound to the loopback address so a
// reload is cheap and does not interrupt in-flight uploads beyond
// the brief mutex hold.
func (s *Server) registerOwnerConfig(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/owner/config", s.handleOwnerConfigCollection)
	mux.HandleFunc("/api/v1/owner/config/reload", s.handleOwnerConfigReload)
}

// handleOwnerConfigCollection exposes the current runtime view of
// the configuration so the dashboard can render the values the
// service is actually using. Sensitive values such as the
// installation id are masked; bootstrap state is shown as a
// boolean so the operator can see whether the local setup wizard
// is still required.
func (s *Server) handleOwnerConfigCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(s.handleOwnerConfigView)(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerConfigView(w http.ResponseWriter, r *http.Request) {
	if s.configView == nil {
		http.Error(w, "configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	view := s.configView()
	_ = json.NewEncoder(w).Encode(view)
}

// handleOwnerConfigReload re-reads config.env and applies the
// values to the running server. The endpoint is gated by owner
// authentication and only mutates the public-origin / bootstrap
// flags on the running process — it does not move the database,
// rotate the device key, or restart the listener. The handler is
// explicit about what changed in the response payload so the
// dashboard can render a confirmation screen.
func (s *Server) handleOwnerConfigReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if s.configReload == nil {
			http.Error(w, "configuration reload unavailable", http.StatusServiceUnavailable)
			return
		}
		previous, next, err := s.configReload(r.Context())
		if err != nil {
			if s.diagnostic != nil {
				_ = s.diagnostic.Warning(fmt.Sprintf("config reload failed: %v", err))
			}
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if s.diagnostic != nil {
			_ = s.diagnostic.Info(fmt.Sprintf(
				"config reloaded; previous_bootstrap=%t next_bootstrap=%t previous_origin=%q next_origin=%q",
				previous.Bootstrap, next.Bootstrap, maskOrigin(previous.PublicOrigin), maskOrigin(next.PublicOrigin),
			))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"previous": previous,
			"current":  next,
			"reloaded": true,
		})
	})(w, r)
}

// maskOrigin returns a redacted view of an origin so the diagnostic
// log does not leak the merchant's full tunnel hostname on every
// reload. The path / query / fragment are stripped by httpsOrigin
// during validation; only the host survives here.
func maskOrigin(origin string) string {
	if origin == "" || strings.EqualFold(origin, configparser.PlaceholderPublicOrigin) {
		return origin
	}
	if idx := strings.Index(origin, "://"); idx >= 0 {
		return origin[:idx+3] + "***"
	}
	return "***"
}

// ConfigView is the projection served by /api/v1/owner/config. The
// dashboard renders it as the operator-facing summary of what the
// runtime is currently using.
type ConfigView struct {
	InstallationID     string `json:"installationId"`
	DataDirectory      string `json:"dataDirectory"`
	BindHost           string `json:"bindHost"`
	Port               int    `json:"port"`
	PublicOrigin       string `json:"publicOrigin"`
	ControlPlaneOrigin string `json:"controlPlaneOrigin"`
	DatabasePath       string `json:"databasePath"`
	Bootstrap          bool   `json:"bootstrap"`
}

// ConfigViewFunc returns the current configuration view.
type ConfigViewFunc func() ConfigView

// ConfigReloadFunc re-reads config.env, validates it and returns
// the previous and next configurations so the caller can render a
// diff in the dashboard. A returned error keeps the previous
// configuration in place and propagates the cause to the operator.
type ConfigReloadFunc func(ctx context.Context) (previous ConfigView, next ConfigView, err error)

// WithConfigView wires a callback that exposes the current
// configuration view. Used by main() to surface the running values
// through /api/v1/owner/config.
func WithConfigView(view ConfigViewFunc) Option {
	return func(server *Server) {
		if view != nil {
			server.configView = view
		}
	}
}

// WithConfigReload wires the config-reload callback used by
// /api/v1/owner/config/reload. The callback must atomically swap
// the public-origin / bootstrap flags so concurrent requests see a
// consistent view.
func WithConfigReload(reload ConfigReloadFunc) Option {
	return func(server *Server) {
		if reload != nil {
			server.configReload = reload
		}
	}
}