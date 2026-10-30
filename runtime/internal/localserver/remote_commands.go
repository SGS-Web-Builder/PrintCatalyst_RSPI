package localserver

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"net/http"
	"time"
)

func (s *Server) registerRemoteCommands(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/remote/commands", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		s.remoteRows(w, r, "SELECT id,kind,state,detail,created_at,updated_at FROM remote_commands ORDER BY created_at DESC LIMIT 30")
	}))
	mux.HandleFunc("POST /api/v1/remote/restart", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		if !remoteRestartAvailable() {
			http.Error(w, "Restart requires the installed Linux systemd service", 409)
			return
		}
		if err := s.restartReady(r.Context()); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		id, err := portalRandomID()
		if err != nil {
			ownerError(w, err)
			return
		}
		res, err := s.db.ExecContext(r.Context(), `INSERT INTO remote_commands(id,kind,state,created_at,updated_at) SELECT ?,'restart','pending',?,? WHERE NOT EXISTS(SELECT 1 FROM remote_commands WHERE state IN ('pending','restarting'))`, id, time.Now().Unix(), time.Now().Unix())
		if err != nil {
			ownerError(w, err)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			http.Error(w, "A restart is already pending", 409)
			return
		}
		w.WriteHeader(202)
		writeJSON(w, map[string]string{"id": id, "state": "pending"})
	}))
}
func (s *Server) restartReady(ctx context.Context) error {
	var paused bool
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT paused FROM kiosk_control WHERE singleton=1").Scan(&paused); err != nil {
		return err
	}
	if !paused {
		return errors.New("pause new orders before restart")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders o WHERE submitted_at>0 AND status NOT IN ('completed','cancelled')
 AND NOT EXISTS(SELECT 1 FROM remote_refunds f WHERE f.order_id=o.id AND f.state='processed')
 AND NOT(o.status IN ('paid','dispatched') AND EXISTS(SELECT 1 FROM order_lines WHERE order_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=o.id AND (j.progress IS NULL OR j.progress<>'completed' OR j.progress_detail<>'Printed output confirmed by merchant'))
 AND NOT EXISTS(SELECT 1 FROM separator_invoices i WHERE i.order_id=o.id AND i.state<>'skipped' AND (i.progress<>'completed' OR i.progress_detail<>'Printed output confirmed by merchant')))`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("resolve all submitted orders before restart")
	}
	fleet, err := s.printers.List(ctx)
	if err != nil {
		return err
	}
	backend := dispatch.NewCUPSBackend()
	for _, p := range fleet {
		if !p.Enabled {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		h, err := backend.PrinterHealth(probe, p.QueueName)
		cancel()
		if err != nil || h.Queued == nil || *h.Queued != 0 || h.State != "idle" {
			return errors.New("printer queue is busy or its status is unknown")
		}
	}
	return nil
}
func (s *Server) applyRemoteCommands(ctx context.Context) {
	var id string
	if s.db.QueryRowContext(ctx, "SELECT id FROM remote_commands WHERE state='pending' ORDER BY created_at LIMIT 1").Scan(&id) != nil {
		return
	}
	if err := s.restartReady(ctx); err != nil {
		s.db.ExecContext(ctx, "UPDATE remote_commands SET state='failed',detail=?,updated_at=? WHERE id=?", err.Error(), time.Now().Unix(), id)
		return
	}
	res, err := s.db.ExecContext(ctx, `UPDATE remote_commands SET state='restarting',updated_at=? WHERE id=? AND state='pending' AND EXISTS(SELECT 1 FROM kiosk_control WHERE paused=1)`, time.Now().Unix(), id)
	if err != nil {
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return
	}
	if err = remoteRestart(); err != nil {
		s.db.ExecContext(ctx, "UPDATE remote_commands SET state='failed',detail='Service restart unavailable',updated_at=? WHERE id=?", time.Now().Unix(), id)
	}
}
