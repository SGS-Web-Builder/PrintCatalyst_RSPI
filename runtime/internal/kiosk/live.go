package kiosk

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id, ok := s.receiptOrder(w, r)
	if !ok {
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		respond(w, 503, "unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(60 * time.Second))
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	end := time.NewTimer(45 * time.Second)
	defer end.Stop()
	last := ""
	heartbeat := time.Now()
	for {
		state, err := s.progressState(r.Context(), id)
		if err != nil {
			state = "assistance"
		}
		var countdown bool
		if e := s.db.QueryRowContext(r.Context(), "SELECT countdown_enabled FROM kiosk_control WHERE singleton=1").Scan(&countdown); e != nil {
			return
		}
		raw, _ := json.Marshal(map[string]any{"state": state, "countdownEnabled": countdown})
		if string(raw) != last {
			if _, e := fmt.Fprintf(w, "data: %s\n\n", raw); e != nil {
				return
			}
			f.Flush()
			last = string(raw)
		} else if time.Since(heartbeat) > 10*time.Second {
			if _, e := fmt.Fprint(w, ": heartbeat\n\n"); e != nil {
				return
			}
			f.Flush()
			heartbeat = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-end.C:
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.RawQuery != "" {
		respond(w, 405, "method_not_allowed")
		return
	}
	var in struct {
		Watching bool `json:"watching"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 128)).Decode(&in) != nil {
		respond(w, 400, "unavailable")
		return
	}
	var otp, countdown bool
	if e := s.db.QueryRowContext(r.Context(), "SELECT otp_enabled,countdown_enabled FROM kiosk_control WHERE singleton=1").Scan(&otp, &countdown); e != nil {
		respond(w, 503, "unavailable")
		return
	}
	if !in.Watching {
		var id string
		e := s.db.QueryRowContext(r.Context(), `SELECT p.order_id FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id JOIN kiosk_release_authorizations v ON v.order_id=o.id LEFT JOIN kiosk_releases rel ON rel.order_id=o.id WHERE (o.status IN ('paid','dispatched','completed') OR o.id IN (SELECT id FROM kiosk_eligible_orders)) AND (?=0 OR rel.release_mode='automatic') AND NOT EXISTS(SELECT 1 FROM kiosk_screen_finished f WHERE f.order_id=o.id) AND NOT EXISTS(SELECT 1 FROM remote_refunds WHERE order_id=o.id) ORDER BY o.created_at,o.id LIMIT 1`, otp).Scan(&id)
		if e == nil {
			s.accepted(w, r, id, "automatic")
			return
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			respond(w, 503, "assistance")
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"otpEnabled": otp, "countdownEnabled": countdown})
}
func (s *Server) dismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := s.receiptOrder(w, r)
	if !ok {
		return
	}
	_, e := s.db.ExecContext(r.Context(), "INSERT INTO kiosk_screen_finished VALUES(?,?) ON CONFLICT(order_id) DO NOTHING", id, s.now().Unix())
	if e != nil {
		respond(w, 503, "unavailable")
		return
	}
	respond(w, 200, "dismissed")
}
