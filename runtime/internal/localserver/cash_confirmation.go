package localserver

import (
	"net/http"
	"time"
)

// Cash is confirmed only by an authenticated owner, never by the public receipt.
func (s *Server) registerCashConfirmation(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/owner/orders/{id}/cash-confirm", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Confirm bool `json:"confirm"`
		}
		if !decodeOwnerJSON(w, r, &input, 1024) {
			return
		}
		if !input.Confirm {
			http.Error(w, "Confirm receipt of the full cash amount", 400)
			return
		}
		actor, ok := s.subjectName(w, r)
		if !ok || actor == "" {
			http.Error(w, "Owner required", 403)
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "Cash confirmation unavailable", 503)
			return
		}
		defer tx.Rollback()
		// Serialize confirmations and bind evidence to the submitted server-priced order.
		if _, err = tx.ExecContext(r.Context(), "UPDATE kiosk_control SET paused=paused WHERE singleton=1"); err != nil {
			http.Error(w, "Cash confirmation unavailable", 503)
			return
		}
		id := r.PathValue("id")
		var existing bool
		if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM kiosk_cash_payments WHERE order_id=?)", id).Scan(&existing); err != nil {
			http.Error(w, "Cash confirmation unavailable", 503)
			return
		}
		if existing {
			writeJSON(w, map[string]bool{"confirmed": true})
			return
		}
		result, err := tx.ExecContext(r.Context(), `INSERT INTO kiosk_cash_payments(order_id,amount_minor,currency,currency_minor_units,approved_by,approved_at)
   SELECT id,total_minor,currency,currency_minor_units,?,? FROM orders o
   WHERE id=? AND status='pending_payment' AND submitted_at>0 AND payment_method='cash_on_counter'
   AND NOT EXISTS(SELECT 1 FROM kiosk_releases WHERE order_id=o.id)
   AND NOT EXISTS(SELECT 1 FROM remote_refunds WHERE order_id=o.id)
   AND NOT EXISTS(SELECT 1 FROM print_submissions j JOIN order_lines l ON l.id=j.line_id WHERE l.order_id=o.id)
   AND NOT EXISTS(SELECT 1 FROM separator_invoices WHERE order_id=o.id AND state IN ('submitting','submitted'))
   AND NOT EXISTS(SELECT 1 FROM payment_intents WHERE order_id=o.id AND status='captured')`, actor, time.Now().Unix(), id)
		if err != nil {
			http.Error(w, "Could not record cash receipt", 500)
			return
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			http.Error(w, "Only an unpaid submitted cash order can be confirmed", 409)
			return
		}
		if _, err = tx.ExecContext(r.Context(), "UPDATE orders SET status='paid',updated_at=? WHERE id=?", time.Now().Unix(), id); err != nil {
			http.Error(w, "Could not record cash receipt", 500)
			return
		}
		if err = tx.Commit(); err != nil {
			http.Error(w, "Could not record cash receipt", 500)
			return
		}
		// The durable pickup worker issues the code; preparation and normal release
		// guards remain mandatory, including after a process restart.
		writeJSON(w, map[string]bool{"confirmed": true})
	}))
}
