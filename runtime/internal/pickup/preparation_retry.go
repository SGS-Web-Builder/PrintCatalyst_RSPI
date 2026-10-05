package pickup

import "context"

// RetryPreparation retains the verified payment and pickup requirement. It is
// forbidden once a release or any printer submission could have occurred.
func (s *Service) RetryPreparation(ctx context.Context, order string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE kiosk_preparations SET state='pending',plan_json='',error='',updated_at=0 WHERE order_id=? AND state='failed'
 AND EXISTS(SELECT 1 FROM orders o JOIN kiosk_pickups p ON p.order_id=o.id WHERE o.id=? AND o.status='paid' AND p.state='active' AND p.expires_at>?)
 AND NOT EXISTS(SELECT 1 FROM kiosk_releases WHERE order_id=?)
 AND NOT EXISTS(SELECT 1 FROM print_submissions j JOIN order_lines l ON l.id=j.line_id WHERE l.order_id=?)
 AND NOT EXISTS(SELECT 1 FROM separator_invoices WHERE order_id=? AND state IN ('submitting','submitted','failed'))`, order, order, s.now().Unix(), order, order, order)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnavailable
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kiosk_pickups SET preparation='pending',prepared_digest='' WHERE order_id=?`, order); err != nil {
		return err
	}
	return tx.Commit()
}
