package localserver

import (
	"context"
	"net/http"
)

type portalPaymentButtons struct {
	CashEnabled   bool `json:"cashEnabled"`
	OnlineEnabled bool `json:"onlineEnabled"`
}

func (s *Server) readPortalPaymentButtons(ctx context.Context) (portalPaymentButtons, error) {
	settings := portalPaymentButtons{true, true}
	if s.db == nil {
		return settings, nil
	}
	err := s.db.QueryRowContext(ctx, "SELECT cash_enabled, online_enabled FROM portal_payment_buttons WHERE singleton=1").Scan(&settings.CashEnabled, &settings.OnlineEnabled)
	return settings, err
}

func (s *Server) registerPortalPaymentButtons(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/portal-payment-buttons", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.readPortalPaymentButtons(r.Context())
		if err != nil {
			http.Error(w, "Could not load payment button settings", 500)
			return
		}
		writeJSON(w, settings)
	}))
	mux.HandleFunc("PUT /api/v1/owner/portal-payment-buttons", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var settings portalPaymentButtons
		if !decodeOwnerJSON(w, r, &settings, 1024) {
			return
		}
		if !settings.CashEnabled && !settings.OnlineEnabled {
			http.Error(w, "Enable at least one payment button", 400)
			return
		}
		if s.db == nil {
			http.Error(w, "Settings storage unavailable", 503)
			return
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE portal_payment_buttons SET cash_enabled=?, online_enabled=? WHERE singleton=1", settings.CashEnabled, settings.OnlineEnabled); err != nil {
			http.Error(w, "Could not save payment button settings", 500)
			return
		}
		writeJSON(w, settings)
	}))
}
