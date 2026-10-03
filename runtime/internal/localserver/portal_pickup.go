package localserver

import (
	"crypto/subtle"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"net/http"
)

func WithPickup(service *pickup.Service) Option { return func(s *Server) { s.pickup = service } }
func (s *Server) registerPortalPickup(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/portal/orders/{id}/pickup", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if s.pickup == nil || s.db == nil {
			portalJSONError(w, "pickup service unavailable", 503)
			return
		}
		var secret string
		err := s.db.QueryRowContext(r.Context(), "SELECT share_token FROM orders WHERE id=?", r.PathValue("id")).Scan(&secret)
		token := r.Header.Get("X-Order-Token")
		if err != nil || secret == "" || token == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(token)) != 1 {
			portalJSONError(w, "order not found", 404)
			return
		}
		view, err := s.pickup.ViewForOrder(r.Context(), r.PathValue("id"))
		if err != nil {
			portalJSONError(w, "pickup information unavailable", 503)
			return
		}
		writeJSON(w, view)
	})
}
