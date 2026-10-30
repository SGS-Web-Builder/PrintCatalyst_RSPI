package localserver

import (
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (s *Server) remoteProtect(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.monitorGuard(w, r) {
			return
		}
		if _, ok := s.remoteSession(w, r); !ok {
			return
		}
		fn(w, r)
	}
}
func (s *Server) admissionReason(ctx context.Context) string {
	if s.db == nil {
		return "Kiosk unavailable"
	}
	var paused bool
	var reason string
	if err := s.db.QueryRowContext(ctx, "SELECT paused,reason FROM kiosk_control WHERE singleton=1").Scan(&paused, &reason); err != nil {
		return "Kiosk status unavailable"
	}
	if paused {
		return "Kiosk is temporarily closed for new orders. " + reason
	}
	return ""
}
func (s *Server) registerRemoteOperations(mux *http.ServeMux) {

	mux.HandleFunc("GET /api/v1/remote/refund-preview/{id}", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		var amount int64
		var currency string
		var units int
		err := s.db.QueryRowContext(r.Context(), `SELECT o.total_minor,o.currency,o.currency_minor_units FROM orders o JOIN kiosk_verified_payments v ON v.order_id=o.id WHERE o.id=? AND o.status='paid'`, r.PathValue("id")).Scan(&amount, &currency, &units)
		if err != nil {
			http.Error(w, "Verified paid order unavailable", 409)
			return
		}
		writeJSON(w, map[string]any{"amount": amount, "currency": currency, "minorUnits": units})
	}))

	mux.HandleFunc("POST /api/v1/remote/refunds/{id}", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason  string `json:"reason"`
			Confirm bool   `json:"confirm"`
		}
		if !decodeOwner(w, r, &in) {
			return
		}
		if !in.Confirm {
			http.Error(w, "Confirm the full refund", 400)
			return
		}
		if s.payments == nil {
			http.Error(w, "Payments unavailable", 503)
			return
		}
		result, err := s.payments.RefundUnreleased(r.Context(), r.PathValue("id"), in.Reason)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, result)
	}))
	mux.HandleFunc("GET /api/v1/remote/refunds", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		s.remoteRows(w, r, "SELECT order_id,amount,currency,gateway_id,state,reason,updated_at FROM remote_refunds ORDER BY updated_at DESC LIMIT 100")
	}))
	mux.HandleFunc("GET /api/v1/remote/providers", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		s.remoteRows(w, r, "SELECT id,display_name FROM payment_providers WHERE kind='razorpay_merchant' AND enabled=1")
	}))
	mux.HandleFunc("GET /api/v1/remote/settlements/{id}", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		if s.payments == nil {
			http.Error(w, "Payments unavailable", 503)
			return
		}
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		items, err := s.payments.RemoteSettlements(r.Context(), r.PathValue("id"), skip)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, items)
	}))
	mux.HandleFunc("GET /api/v1/remote/kiosk-flow", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		var otp, countdown bool
		if e := s.db.QueryRowContext(r.Context(), "SELECT otp_enabled,countdown_enabled FROM kiosk_control WHERE singleton=1").Scan(&otp, &countdown); e != nil {
			ownerError(w, e)
			return
		}
		writeJSON(w, map[string]bool{"otpEnabled": otp, "countdownEnabled": countdown})
	}))
	mux.HandleFunc("PUT /api/v1/remote/kiosk-flow", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OTP       *bool `json:"otpEnabled"`
			Countdown *bool `json:"countdownEnabled"`
		}
		if !decodeOwner(w, r, &in) {
			return
		}
		if in.OTP == nil && in.Countdown == nil {
			http.Error(w, "Choose at least one setting", 400)
			return
		}
		tx, e := s.db.BeginTx(r.Context(), nil)
		if e != nil {
			ownerError(w, e)
			return
		}
		defer tx.Rollback()
		_, e = tx.ExecContext(r.Context(), "UPDATE kiosk_control SET otp_enabled=COALESCE(?,otp_enabled),countdown_enabled=COALESCE(?,countdown_enabled),updated_at=? WHERE singleton=1", in.OTP, in.Countdown, time.Now().Unix())
		if e == nil {
			raw, _ := json.Marshal(in)
			_, e = tx.ExecContext(r.Context(), "INSERT INTO remote_audit(action,detail,occurred_at) VALUES('kiosk.flow',?,?)", string(raw), time.Now().Unix())
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			ownerError(w, e)
			return
		}
		var otp, countdown bool
		if e = s.db.QueryRowContext(r.Context(), "SELECT otp_enabled,countdown_enabled FROM kiosk_control WHERE singleton=1").Scan(&otp, &countdown); e != nil {
			ownerError(w, e)
			return
		}
		if s.pickup != nil {
			s.pickup.NotifyPrepared()
		}
		writeJSON(w, map[string]bool{"otpEnabled": otp, "countdownEnabled": countdown})
	}))

	mux.HandleFunc("GET /api/v1/remote/status", s.remoteProtect(s.remoteStatus))
	mux.HandleFunc("PUT /api/v1/remote/maintenance", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool   `json:"paused"`
			Reason string `json:"reason"`
		}
		if !decodeOwner(w, r, &in) {
			return
		}
		in.Reason = strings.TrimSpace(in.Reason)
		if len(in.Reason) > 240 {
			http.Error(w, "Reason too long", 400)
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			ownerError(w, err)
			return
		}
		defer tx.Rollback()
		if !in.Paused {
			var pending bool
			if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM remote_commands WHERE state IN ('pending','restarting'))").Scan(&pending); err != nil || pending {
				http.Error(w, "Wait for the restart to finish before reopening", 409)
				return
			}
		}
		_, err = tx.ExecContext(r.Context(), "UPDATE kiosk_control SET paused=?,reason=?,updated_at=? WHERE singleton=1", in.Paused, in.Reason, time.Now().Unix())
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO remote_audit(action,detail,occurred_at) VALUES('maintenance',?,?)", in.Reason, time.Now().Unix())
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			ownerError(w, err)
			return
		}
		writeJSON(w, in)
	}))
	mux.HandleFunc("GET /api/v1/remote/schedules", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		s.remoteRows(w, r, "SELECT id,scope,due_at,state,detail FROM price_schedules ORDER BY created_at DESC LIMIT 100")
	}))
	mux.HandleFunc("POST /api/v1/remote/schedules", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope string        `json:"scope"`
			DueAt int64         `json:"dueAt"`
			Input pricing.Input `json:"input"`
		}
		if !decodeOwnerJSON(w, r, &in, pricingBodyLimit) {
			return
		}
		if in.DueAt <= time.Now().Unix() || in.DueAt > time.Now().AddDate(1, 0, 0).Unix() || in.Input.ExpectedRevision == nil || len(in.Input.Entries) > pricing.MaxEntries || len(in.Scope) > 128 {
			http.Error(w, "Choose a future time within one year and reload pricing first", 400)
			return
		}
		if in.Scope == "all" {
			in.Scope = ""
		}
		if _, err := s.pricing.GridBook(r.Context(), in.Scope); err != nil {
			ownerError(w, err)
			return
		}
		id, err := portalRandomID()
		if err != nil {
			ownerError(w, err)
			return
		}
		raw, err := json.Marshal(in.Input)
		if err == nil {
			_, err = s.db.ExecContext(r.Context(), "INSERT INTO price_schedules(id,scope,input_json,due_at,created_at) VALUES(?,?,?,?,?)", id, in.Scope, string(raw), in.DueAt, time.Now().Unix())
		}
		if err != nil {
			ownerError(w, err)
			return
		}
		writeJSON(w, map[string]any{"id": id, "scheduled": true})
	}))
	mux.HandleFunc("DELETE /api/v1/remote/schedules/{id}", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		res, err := s.db.ExecContext(r.Context(), "UPDATE price_schedules SET state='cancelled' WHERE id=? AND state='pending'", r.PathValue("id"))
		if err != nil {
			ownerError(w, err)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			http.Error(w, "Schedule already applied or unavailable", 409)
			return
		}
		writeJSON(w, map[string]bool{"cancelled": true})
	}))
	mux.HandleFunc("GET /api/v1/remote/audit", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		s.remoteRows(w, r, "SELECT action,detail,occurred_at FROM remote_audit ORDER BY id DESC LIMIT 100")
	}))
	mux.HandleFunc("GET /api/v1/remote/revenue", s.remoteProtect(func(w http.ResponseWriter, r *http.Request) {
		s.remoteRows(w, r, `SELECT currency,currency_minor_units,COUNT(*) AS orders,COALESCE(SUM(total_minor),0) AS gross_minor FROM orders WHERE status IN ('paid','dispatched','completed') AND submitted_at>0 GROUP BY currency,currency_minor_units`)
	}))
}

// Fixed queries only; do not expose a browser-selected SQL or filesystem path.
func (s *Server) remoteRows(w http.ResponseWriter, r *http.Request, query string) {
	rows, err := s.db.QueryContext(r.Context(), query)
	if err != nil {
		ownerError(w, err)
		return
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		ownerError(w, err)
		return
	}
	out := []map[string]any{}
	for rows.Next() {
		v := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range v {
			ptr[i] = &v[i]
		}
		if err = rows.Scan(ptr...); err != nil {
			ownerError(w, err)
			return
		}
		item := map[string]any{}
		for i, k := range cols {
			item[k] = v[i]
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		ownerError(w, err)
		return
	}
	writeJSON(w, out)
}
func (s *Server) remoteStatus(w http.ResponseWriter, r *http.Request) {
	var paused bool
	var reason string
	if err := s.db.QueryRowContext(r.Context(), "SELECT paused,reason FROM kiosk_control WHERE singleton=1").Scan(&paused, &reason); err != nil {
		ownerError(w, err)
		return
	}
	fleet, err := s.printers.List(r.Context())
	if err != nil {
		ownerError(w, err)
		return
	}
	telemetry := map[string]json.RawMessage{}
	rows, err := s.db.QueryContext(r.Context(), "SELECT printer_id,payload FROM printer_telemetry")
	if err != nil {
		ownerError(w, err)
		return
	}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		telemetry[id] = json.RawMessage(raw)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		ownerError(w, err)
		return
	}
	var storage any
	if s.files != nil {
		free, reserve, e := s.files.Capacity()
		if e == nil {
			storage = map[string]any{"availableBytes": free, "reserveBytes": reserve, "low": free < reserve}
		}
	}
	writeJSON(w, map[string]any{"paused": paused, "reason": reason, "checkedAt": time.Now().Unix(), "printers": fleet, "telemetry": telemetry, "platform": runtime.GOOS + "/" + runtime.GOARCH, "ers": "unconfigured", "storage": storage})
}
func (s *Server) RunRemote(ctx context.Context) {
	s.db.ExecContext(ctx, "UPDATE remote_commands SET state='applied',detail='Service started after restart request',updated_at=? WHERE state='restarting'", time.Now().Unix())
	// A crash between price persistence and acknowledgement is never replayed silently.
	s.db.ExecContext(ctx, "UPDATE price_schedules SET state='review',detail='Service restarted during application; inspect current prices' WHERE state='applying'")
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		s.applyRemoteCommands(ctx)
		s.applyPriceSchedules(ctx)
		s.pollPrinterHealth(ctx)
		s.sendFleetHeartbeat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *Server) applyPriceSchedules(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,scope,input_json FROM price_schedules WHERE state='pending' AND due_at<=? ORDER BY due_at,id LIMIT 10", time.Now().Unix())
	if err != nil {
		return
	}
	type item struct{ id, scope, raw string }
	list := []item{}
	for rows.Next() {
		var i item
		if rows.Scan(&i.id, &i.scope, &i.raw) == nil {
			list = append(list, i)
		}
	}
	rows.Close()
	for _, i := range list {
		res, err := s.db.ExecContext(ctx, "UPDATE price_schedules SET state='applying' WHERE id=? AND state='pending'", i.id)
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			continue
		}
		var in pricing.Input
		err = json.Unmarshal([]byte(i.raw), &in)
		if err == nil {
			_, err = s.pricing.SaveGrid(ctx, i.scope, in)
		}
		state, detail := "applied", "Prices saved"
		if err != nil {
			state = "review"
			detail = err.Error()
		}
		s.db.ExecContext(ctx, "UPDATE price_schedules SET state=?,detail=? WHERE id=?", state, detail, i.id)
	}
}
func (s *Server) pollPrinterHealth(ctx context.Context) {
	if s.printers == nil {
		return
	}
	fleet, err := s.printers.List(ctx)
	if err != nil {
		return
	}
	backend := dispatch.NewCUPSBackend()
	for _, p := range fleet {
		if !p.Enabled {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		h, err := backend.PrinterHealth(probe, p.QueueName)
		cancel()
		if err != nil {
			h.State = "unavailable"
			h.Message = "Local printer status unavailable"
		}
		raw, _ := json.Marshal(h)
		s.db.ExecContext(ctx, "INSERT INTO printer_telemetry VALUES(?,?,?) ON CONFLICT(printer_id) DO UPDATE SET payload=excluded.payload,checked_at=excluded.checked_at", p.ID, string(raw), time.Now().Unix())
	}
}
