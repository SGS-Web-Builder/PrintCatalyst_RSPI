package localserver

import (
	"net/http"
)

func (s *Server) registerRemoteSupport(mux *http.ServeMux) {
	for _, action := range []string{"retry-preparation", "reissue-code", "confirm-output"} {
		action := action
		mux.HandleFunc("POST /api/v1/remote/support/"+action+"/{id}", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Confirm bool `json:"confirm"`
			}
			if !decodeOwner(w, r, &in) {
				return
			}
			if !in.Confirm {
				http.Error(w, "Explicit confirmation required", 400)
				return
			}
			id := r.PathValue("id")
			if !hexIDPattern.MatchString(id) {
				http.Error(w, "Invalid order ID", 400)
				return
			}
			var refund bool
			if err := s.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM remote_refunds WHERE order_id=?)", id).Scan(&refund); err != nil {
				ownerError(w, err)
				return
			}
			if refund {
				http.Error(w, "Resolve the refund through the payment gateway before any further order action", 409)
				return
			}
			var err error
			switch action {
			case "retry-preparation":
				if s.pickup == nil {
					http.Error(w, "Pickup unavailable", 503)
					return
				}
				err = s.pickup.RetryPreparation(r.Context(), id)
			case "reissue-code":
				if s.pickup == nil {
					http.Error(w, "Pickup unavailable", 503)
					return
				}
				err = s.pickup.ReissueExpired(r.Context(), id)
			case "confirm-output":
				err = s.orders.ConfirmPrintDone(r.Context(), id, "remote-owner")
			}
			if err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			writeJSON(w, map[string]string{"status": "applied"})
		}))
	}
}
