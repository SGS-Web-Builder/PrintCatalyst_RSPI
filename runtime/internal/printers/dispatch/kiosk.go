package dispatch

import (
	"context"
	"errors"
)

// checkPickup is independent of payment status and inherited auto-print settings.
// Only the trusted kiosk claim transaction may insert a release row.
func (d *Dispatcher) checkPickup(ctx context.Context, id string) error {
	if !d.config.RequirePickup {
		return nil
	}
	var allowed bool
	err := d.db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM kiosk_releases r JOIN kiosk_pickups p ON p.order_id=r.order_id
 JOIN orders o ON o.id=r.order_id
 WHERE r.order_id=? AND p.state='claimed' AND p.preparation='ready'
 AND p.prepared_digest=r.prepared_digest AND r.prepared_digest<>'' AND o.status='paid')`, id).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("physical kiosk pickup is required before printing")
	}
	return nil
}
