package kiosk

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"io"
	"mime"
	"net/http"
)

func (s *Server) accepted(w http.ResponseWriter, r *http.Request, order string, states ...string) {
	token, err := randomSecret(32)
	if err != nil {
		respond(w, 503, "assistance")
		return
	}
	hash := sha256.Sum256([]byte(token))
	if _, err = s.db.ExecContext(r.Context(), `INSERT INTO kiosk_screen_receipts VALUES(?,?,?)`, hash[:], order, s.now().Unix()+86400); err != nil {
		respond(w, 503, "assistance")
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM kiosk_screen_receipts WHERE expires_at<=?`, s.now().Unix())
	w.WriteHeader(202)
	state := "accepted"
	if len(states) > 0 {
		state = states[0]
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"state": state, "receipt": token})
}

// Progress is scoped to a random release receipt, never an enumerable order ID.
func (s *Server) receiptOrder(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != "POST" || r.URL.RawQuery != "" {
		respond(w, 405, "method_not_allowed")
		return "", false
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		respond(w, 415, "json_required")
		return "", false
	}
	var input struct {
		Receipt string `json:"receipt"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.Receipt) != 64 {
		respond(w, 400, "unavailable")
		return "", false
	}
	hash := sha256.Sum256([]byte(input.Receipt))
	var id string
	if err = s.db.QueryRowContext(r.Context(), `SELECT order_id FROM kiosk_screen_receipts WHERE token_hash=? AND expires_at>?`, hash[:], s.now().Unix()).Scan(&id); err != nil {
		respond(w, 404, "unavailable")
		return "", false
	}
	return id, true
}
func (s *Server) progress(w http.ResponseWriter, r *http.Request) {
	id, ok := s.receiptOrder(w, r)
	if !ok {
		return
	}
	state, err := s.progressState(r.Context(), id)
	if err != nil {
		respond(w, 503, "assistance")
		return
	}
	respond(w, 200, state)
}
func (s *Server) progressState(ctx context.Context, id string) (string, error) {
	var blocked bool
	if e := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM remote_refunds WHERE order_id=?)", id).Scan(&blocked); e != nil {
		return "", e
	}
	if blocked {
		return "assistance", nil
	}
	var prep, pick string
	var expires int64
	e := s.db.QueryRowContext(ctx, "SELECT preparation,state,expires_at FROM kiosk_pickups WHERE order_id=?", id).Scan(&prep, &pick, &expires)
	var hasWaiting bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_waiting_releases WHERE order_id=?)", id).Scan(&hasWaiting); err != nil {
		return "", err
	}
	if e == nil && pick == "active" && !hasWaiting {
		var otp bool
		if err := s.db.QueryRowContext(ctx, "SELECT otp_enabled FROM kiosk_control WHERE singleton=1").Scan(&otp); err != nil {
			return "", err
		}
		if otp {
			var waiting bool
			if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_waiting_releases WHERE order_id=?)", id).Scan(&waiting); err != nil {
				return "", err
			}
			if !waiting {
				return "otp_required", nil
			}
		}
		if prep == "failed" || (otp && expires <= s.now().Unix()) {
			return "assistance", nil
		}
		if prep != "ready" {
			return "preparing", nil
		}
	}
	state, err := orders.New(s.db, nil).PrintProgress(ctx, id)
	if err != nil {
		return "", errors.New("progress unavailable")
	}
	// A waiting receipt polls status only; it never guesses/re-enters an OTP.
	var waiting bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kiosk_waiting_releases WHERE order_id=?)`, id).Scan(&waiting); err != nil {
		return "", errors.New("progress unavailable")
	}
	if waiting {
		viewer, ok := s.pickup.(interface {
			ViewForOrder(context.Context, string) (pickup.View, error)
		})
		if !ok {
			return "", errors.New("progress unavailable")
		}
		view, e := viewer.ViewForOrder(ctx, id)
		if e != nil {
			return "", errors.New("progress unavailable")
		}
		if view.State != "claimed" && view.State != "completed" && view.State != "automatic_printing" {
			var current bool
			if e = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kiosk_waiting_releases w JOIN kiosk_pickups p ON p.order_id=w.order_id WHERE w.order_id=? AND w.code_hash=p.code_hash AND w.pickup_created_at=p.created_at)`, id).Scan(&current); e != nil {
				return "", errors.New("progress unavailable")
			}
			if !current || (view.State != "active" && view.State != "automatic") || view.Preparation == "failed" {
				return "assistance", nil
			}
			return "preparing", nil
		}
	}
	var review int
	if err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM print_submissions j JOIN order_lines l ON l.id=j.line_id WHERE l.order_id=? AND j.progress='review') + (SELECT COUNT(*) FROM separator_invoices WHERE order_id=? AND state<>'skipped' AND progress='review')`, id, id).Scan(&review); err != nil {
		return "", errors.New("progress unavailable")
	}
	if review > 0 {
		// A completed CUPS transmission for one line must not end a multi-job order.
		var unfinished int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
SELECT COALESCE(NULLIF(j.progress,''),j.state,'') AS state FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=?
UNION ALL SELECT COALESCE(NULLIF(progress,''),state) FROM separator_invoices WHERE order_id=? AND state<>'skipped'
) WHERE state NOT IN ('review','completed')`, id, id).Scan(&unfinished); err != nil {
			return "", errors.New("progress unavailable")
		}
		if unfinished == 0 {
			state = "assistance"
		}
	}
	return state, nil
}
