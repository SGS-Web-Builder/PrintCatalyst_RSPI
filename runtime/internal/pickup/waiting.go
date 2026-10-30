package pickup

import (
	"context"
	"time"
)

// NotifyPrepared is nonblocking; durable database state is the source of truth.
func (s *Service) NotifyPrepared() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// ReleaseWaiting only consumes prior trusted OTP entries for the same pickup
// generation. The normal claim transaction rechecks payment, expiry and digest.
func (s *Service) ReleaseWaiting(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.order_id,p.code_cipher FROM kiosk_waiting_releases w JOIN kiosk_pickups p ON p.order_id=w.order_id JOIN orders o ON o.id=p.order_id WHERE p.state='active' AND p.preparation='ready' AND o.id IN (SELECT id FROM kiosk_eligible_orders) AND p.expires_at>? AND p.code_hash=w.code_hash AND p.created_at=w.pickup_created_at LIMIT 100`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	type item struct {
		order  string
		cipher []byte
	}
	var items []item
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.order, &v.cipher); err != nil {
			break
		}
		items = append(items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range items {
		code, e := s.decrypt(v.order, v.cipher)
		if e != nil {
			return n, e
		}
		if _, e = s.claim(ctx, code, v.order); e == nil {
			n++
		} else if e != ErrUnavailable && e != ErrPreparing {
			return n, e
		}
	}
	return n, nil
}

func (s *Service) RunWaiting(ctx context.Context, check func(context.Context) error, wake func()) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		if check(ctx) == nil {
			n, _ := s.ReleaseWaiting(ctx)
			automatic, _ := s.ReleaseAutomatic(ctx)
			n += automatic
			if n > 0 {
				wake()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.ready:
		case <-timer.C:
		}
	}
}

// Auto release is an explicit owner policy; every claim rechecks it under the
// same write lock used by remote settings, refunds and physical claims.
func (s *Service) ReleaseAutomatic(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.order_id,p.code_cipher FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id JOIN kiosk_release_authorizations v ON v.order_id=p.order_id WHERE p.state='active' AND p.preparation='ready' AND o.id IN (SELECT id FROM kiosk_eligible_orders) AND EXISTS(SELECT 1 FROM kiosk_control WHERE otp_enabled=0) AND NOT EXISTS(SELECT 1 FROM remote_refunds WHERE order_id=p.order_id) ORDER BY o.created_at,o.id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id     string
		cipher []byte
	}
	list := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.cipher); err != nil {
			break
		}
		list = append(list, i)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, i := range list {
		code, e := s.decrypt(i.id, i.cipher)
		if e != nil {
			return n, e
		}
		if _, e = s.claimMode(ctx, code, i.id, true); e == nil {
			n++
		} else if e != ErrUnavailable && e != ErrPreparing {
			return n, e
		}
	}
	return n, nil
}
