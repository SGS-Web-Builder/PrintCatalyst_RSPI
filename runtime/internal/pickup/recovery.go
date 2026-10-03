package pickup

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Recover processes the durable verified-capture queue; it never infers payment
// verification merely from an order or intent status, and never dispatches.
func (s *Service) Recover(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT v.order_id,v.payment_reference FROM kiosk_verified_payments v
 JOIN orders o ON o.id=v.order_id JOIN payment_intents i ON i.id=v.intent_id
 WHERE o.status='paid' AND i.status='captured' AND i.gateway_payment_id=v.payment_reference
 AND i.amount_minor=o.total_minor AND i.currency=o.currency AND i.currency_minor_units=o.currency_minor_units
 AND NOT EXISTS(SELECT 1 FROM kiosk_pickups p WHERE p.order_id=v.order_id)
 ORDER BY v.verified_at,v.order_id LIMIT 100`)
	if err != nil {
		return err
	}
	type work struct{ order, reference string }
	var items []work
	for rows.Next() {
		var w work
		if err = rows.Scan(&w.order, &w.reference); err != nil {
			break
		}
		items = append(items, w)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, w := range items {
		if _, err = s.IssueVerified(ctx, w.order, w.reference); err != nil {
			failures = append(failures, err)
			// Store only a generic condition, never code/key material or gateway payloads.
			_, saveErr := s.db.ExecContext(ctx, "UPDATE kiosk_verified_payments SET last_error='assistance' WHERE order_id=?", w.order)
			if saveErr != nil {
				failures = append(failures, saveErr)
			}
		} else {
			_, saveErr := s.db.ExecContext(ctx, "UPDATE kiosk_verified_payments SET last_error='' WHERE order_id=?", w.order)
			if saveErr != nil {
				failures = append(failures, saveErr)
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Service) Run(ctx context.Context) error {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		// Pending work remains in SQLite on failure and is retried next tick.
		_ = s.Recover(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type View struct {
	State       string `json:"state"`
	Code        string `json:"code,omitempty"`
	Preparation string `json:"preparation,omitempty"`
	ExpiresAt   int64  `json:"expiresAt,omitempty"`
}

// ViewForOrder is privileged: the HTTP caller must validate the order's secret.
func (s *Service) ViewForOrder(ctx context.Context, order string) (View, error) {
	var v View
	var raw []byte
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT p.state,p.preparation,p.expires_at,p.code_cipher,o.status FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id WHERE p.order_id=?`, order).Scan(&v.State, &v.Preparation, &v.ExpiresAt, &raw, &status)
	if err == sql.ErrNoRows {
		var failure string
		e := s.db.QueryRowContext(ctx, "SELECT last_error FROM kiosk_verified_payments WHERE order_id=?", order).Scan(&failure)
		if e == nil {
			if failure != "" {
				return View{State: "assistance"}, nil
			}
			return View{State: "preparing_code"}, nil
		}
		if e != sql.ErrNoRows {
			return View{}, e
		}
		return View{State: "waiting_payment"}, nil
	}
	if err != nil {
		return View{}, err
	}
	if status == "completed" {
		return View{State: "completed"}, nil
	}
	if status == "cancelled" {
		return View{State: "unavailable"}, nil
	}
	if v.State == "active" && v.ExpiresAt <= s.now().Unix() {
		v.State = "expired"
	}
	if v.State == "active" && status == "paid" {
		v.Code, err = s.decrypt(order, raw)
	}
	return v, err
}
