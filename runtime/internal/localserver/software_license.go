package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"io"
	"net/http"
	"strings"
)

func WithSoftwareLicense(g *licensegate.Client) Option { return func(s *Server) { s.licenseGate = g } }
func (s *Server) registerSoftwareLicense(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/licence", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.softwareLicenseView(r)) }))
	mux.HandleFunc("POST /api/v1/owner/licence/activate", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			LicenseKey string `json:"licenseKey"`
		}
		if !decodeOwnerJSON(w, r, &input, 2048) {
			return
		}
		if err := s.licenseGate.Activate(r.Context(), input.LicenseKey); err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		writeJSON(w, s.softwareLicenseView(r))
	}))
	mux.HandleFunc("POST /api/v1/owner/licence/refresh", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if err := s.licenseGate.Refresh(r.Context()); err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		writeJSON(w, s.softwareLicenseView(r))
	}))
	mux.HandleFunc("GET /api/v1/owner/licence/events", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, []any{}) }))
	for _, path := range []string{"revoke", "transfer"} {
		mux.HandleFunc("POST /api/v1/owner/licence/"+path, s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Licence revocation and device transfers are managed by the provider at licenses.printcatalyst.in", 403)
		}))
	}
}
func (s *Server) softwareLicenseView(r *http.Request) map[string]any {
	v := s.licenseGate.Status(r.Context())
	return map[string]any{"permanentLicense": true, "active": v.Active, "configured": v.Configured, "unconfigured": !v.Active, "installationId": s.installationID, "licenseId": v.LicenseID, "checkUntil": v.CheckUntil, "message": v.Message, "entitlements": []string{"All software features"}}
}
func (s *Server) enforceSoftwareLicense(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (s.licenseGate == nil && !licensegate.Required) || r.Method == "GET" || r.Method == "HEAD" {
			next.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		commercial := path == "/api/v1/owner/id-cards" || path == "/api/v1/owner/passports" || strings.HasPrefix(path, "/api/v1/portal/") || strings.HasPrefix(path, "/api/v1/owner/id-cards/") || strings.HasPrefix(path, "/api/v1/owner/passports/") || strings.HasPrefix(path, "/api/v1/owner/payments/intents") || path == "/api/v1/orders" || strings.HasSuffix(path, "/test-print") || strings.HasSuffix(path, "/test-page")
		// Reconciliation/webhooks remain available. Prevent manual payment-state changes
		// from bypassing the new-order gate while allowing confirmation of actual output.
		if strings.HasPrefix(path, "/api/v1/owner/orders/") && (strings.HasSuffix(path, "/status") || strings.HasSuffix(path, "/print")) {
			commercial = true
		}
		if commercial {
			var check func(context.Context) error
			if s.licenseGate != nil {
				check = s.licenseGate.Check
			}
			if err := licensegate.CheckRequired(r.Context(), check); err != nil {
				if strings.HasSuffix(path, "/status") && strings.HasPrefix(path, "/api/v1/owner/orders/") {
					raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
					if e == nil && len(raw) <= 4096 {
						r.Body = io.NopCloser(bytes.NewReader(raw))
						var v struct {
							Status string `json:"status"`
						}
						if json.Unmarshal(raw, &v) == nil && (v.Status == "print_completed" || v.Status == "cancelled") {
							next.ServeHTTP(w, r)
							return
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
