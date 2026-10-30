package pickup

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Recover processes durable gateway evidence or explicit owner cash receipts.
// It never infers authorization merely from paid status, and never dispatches.
func (s *Service) Recover(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT v.order_id,v.payment_reference FROM kiosk_release_authorizations v
 JOIN orders o ON o.id=v.order_id WHERE o.id IN (SELECT id FROM kiosk_eligible_orders)
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
			_, cashErr := s.db.ExecContext(ctx, "UPDATE kiosk_cash_payments SET last_error=? WHERE order_id=?", "assistance", w.order)
			if cashErr != nil {
				failures = append(failures, cashErr)
			}
			if saveErr != nil {
				failures = append(failures, saveErr)
			}
		} else {
			_, saveErr := s.db.ExecContext(ctx, "UPDATE kiosk_verified_payments SET last_error='' WHERE order_id=?", w.order)
			_, cashErr := s.db.ExecContext(ctx, "UPDATE kiosk_cash_payments SET last_error='' WHERE order_id=?", w.order)
			if cashErr != nil {
				failures = append(failures, cashErr)
			}
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
	State          string `json:"state"`
	PaymentPending bool   `json:"paymentPending,omitempty"`
	Code           string `json:"code,omitempty"`
	Preparation    string `json:"preparation,omitempty"`
	ExpiresAt      int64  `json:"expiresAt,omitempty"`
}

// ViewForOrder is privileged: the HTTP caller must validate the order's secret.
func (s *Service) ViewForOrder(ctx context.Context, order string) (View, error) {
	var refund string
	err := s.db.QueryRowContext(ctx, "SELECT state FROM remote_refunds WHERE order_id=?", order).Scan(&refund)
	if err != nil && err != sql.ErrNoRows {
		return View{}, err
	}
	if err == nil {
		state := "refund_review"
		if refund == "processed" {
			state = "refunded"
		} else if refund == "pending" {
			state = "refund_pending"
		}
		return View{State: state}, nil
	}
	var otpEnabled bool
	if err = s.db.QueryRowContext(ctx, "SELECT otp_enabled FROM kiosk_control WHERE singleton=1").Scan(&otpEnabled); err != nil {
		return View{}, err
	}
	var v View
	var raw []byte
	var status string
	var created int64
	err = s.db.QueryRowContext(ctx, `SELECT p.state,p.preparation,p.expires_at,p.code_cipher,o.status,p.created_at FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id WHERE p.order_id=?`, order).Scan(&v.State, &v.Preparation, &v.ExpiresAt, &raw, &status, &created)
	if err == sql.ErrNoRows {
		var failure string
		e := s.db.QueryRowContext(ctx, "SELECT last_error FROM kiosk_verified_payments WHERE order_id=? UNION ALL SELECT last_error FROM kiosk_cash_payments WHERE order_id=?", order, order).Scan(&failure)
		if e == nil {
			if failure != "" {
				return View{State: "assistance"}, nil
			}
			if !otpEnabled {
				return View{State: "automatic"}, nil
			}
			return View{State: "preparing_code"}, nil
		}
		if e != sql.ErrNoRows {
			return View{}, e
		}
		var unpaidAllowed bool
		if e = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_release_authorizations WHERE order_id=? AND payment_reference=?)", order, "unpaid-cash:"+order).Scan(&unpaidAllowed); e != nil {
			return View{}, e
		}
		if unpaidAllowed {
			if !otpEnabled {
				return View{State: "automatic", PaymentPending: true}, nil
			}
			return View{State: "preparing_code", PaymentPending: true}, nil
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
	var eligible bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_eligible_orders WHERE id=?)", order).Scan(&eligible); err != nil {
		return View{}, err
	}
	v.PaymentPending = status == "pending_payment"
	if v.State == "active" && !eligible {
		return View{State: "waiting_payment", PaymentPending: v.PaymentPending}, nil
	}
	if !otpEnabled && eligible && v.State == "active" {
		if v.Preparation == "failed" {
			return View{State: "assistance", PaymentPending: v.PaymentPending}, nil
		}
		return View{State: "automatic", Preparation: v.Preparation, PaymentPending: v.PaymentPending}, nil
	}
	if v.State == "claimed" {
		var automatic bool
		if e := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_releases WHERE order_id=? AND release_mode='automatic')", order).Scan(&automatic); e != nil {
			return View{}, e
		}
		if automatic {
			return View{State: "automatic_printing", PaymentPending: v.PaymentPending}, nil
		}
	}
	end, e := dayEnd(ctx, s.db, time.Unix(created, 0))
	if e != nil {
		return View{}, e
	}
	if end < v.ExpiresAt {
		v.ExpiresAt = end
	}
	if v.State == "active" && v.ExpiresAt <= s.now().Unix() {
		v.State = "expired"
	}
	if v.State == "active" && eligible {
		v.Code, err = s.decrypt(order, raw)
	}
	return v, err
}
