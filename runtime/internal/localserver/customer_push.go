package localserver

import (
	"crypto/subtle"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/webnotify"
	webpush "github.com/SherClockHolmes/webpush-go"
	"net/http"
)

func WithCustomerPush(p *webnotify.Service) Option { return func(s *Server) { s.customerPush = p } }
func (s *Server) registerCustomerPush(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/portal/notifications/key", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		if s.customerPush == nil {
			portalJSONError(w, "Notifications unavailable", 503)
			return
		}
		writeJSON(w, map[string]string{"publicKey": s.customerPush.PublicKey()})
	})
	for _, method := range []string{"PUT", "DELETE"} {
		mux.HandleFunc(method+" /api/v1/portal/orders/{id}/notifications", func(w http.ResponseWriter, r *http.Request) {
			if !s.portalGuard(w, r, true) {
				return
			}
			if s.customerPush == nil || s.db == nil {
				portalJSONError(w, "Notifications unavailable", 503)
				return
			}
			var token string
			err := s.db.QueryRowContext(r.Context(), `SELECT share_token FROM orders WHERE id=?`, r.PathValue("id")).Scan(&token)
			supplied := r.Header.Get("X-Order-Token")
			if err != nil || token == "" || supplied == "" || subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
				portalJSONError(w, "order not found", 404)
				return
			}
			if r.Method == "DELETE" {
				err = s.customerPush.Unsubscribe(r.Context(), r.PathValue("id"))
			} else {
				var sub webpush.Subscription
				if !portalDecode(w, r, &sub, 4096) {
					return
				}
				if err = webnotify.Validate(sub); err != nil {
					portalJSONError(w, err.Error(), 400)
					return
				}
				err = s.customerPush.Subscribe(r.Context(), r.PathValue("id"), sub)
			}
			if err != nil {
				portalJSONError(w, "Could not save notification preference", 503)
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
		})
	}
}
