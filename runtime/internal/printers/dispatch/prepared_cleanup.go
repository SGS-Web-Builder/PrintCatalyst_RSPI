package dispatch

import (
	"context"
	"errors"
	"time"
)

func (d *Dispatcher) runPreparedCleanup(ctx context.Context) {
	// Recover abandoned staging files once per service run, never active bundles.
	if d.config.Prepared != nil {
		if err := d.config.Prepared.CleanupAbandoned(time.Now()); err != nil {
			d.setLastError(err)
		}
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		if err := d.cleanupPrepared(ctx); err != nil && ctx.Err() == nil {
			d.setLastError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (d *Dispatcher) cleanupPrepared(ctx context.Context) error {
	if d.config.Prepared == nil {
		return nil
	}
	// Every document and every selected invoice must have confirmed completion.
	// Payment, submission, cancelled jobs and expired codes are not completion.
	rows, err := d.db.QueryContext(ctx, `SELECT k.order_id,k.prepared_digest FROM kiosk_preparations k WHERE k.state='ready' AND k.purged_at=0
 AND EXISTS(SELECT 1 FROM order_lines WHERE order_id=k.order_id)
 AND NOT EXISTS(SELECT 1 FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=k.order_id AND (COALESCE(j.state,'')<>'submitted' OR COALESCE(j.progress,'')<>'completed'))
 AND NOT EXISTS(SELECT 1 FROM separator_invoices i WHERE i.order_id=k.order_id AND i.state<>'skipped' AND (i.state<>'submitted' OR i.progress<>'completed')) LIMIT 50`)
	if err != nil {
		return err
	}
	type bundle struct{ order, digest string }
	var bundles []bundle
	for rows.Next() {
		var b bundle
		if err = rows.Scan(&b.order, &b.digest); err != nil {
			break
		}
		bundles = append(bundles, b)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, b := range bundles {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = d.config.Prepared.DeleteCompleted(b.order, b.digest); err != nil {
			failures = append(failures, errors.New("completed prepared-file cleanup requires attention"))
			continue
		}
		if _, err = d.db.ExecContext(ctx, `UPDATE kiosk_preparations SET purged_at=? WHERE order_id=? AND prepared_digest=?`, time.Now().Unix(), b.order, b.digest); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
