package orders

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// PrintProgress deliberately does not interpret unpaid as unprinted.
func (s *Service) PrintProgress(ctx context.Context, id string) (string, error) {
	var status string
	var requested bool
	if err := s.db.QueryRowContext(ctx, "SELECT status,print_requested FROM orders WHERE id=?", id).Scan(&status, &requested); err != nil {
		return "", err
	}
	if status == StatusCancelled {
		return "rejected", nil
	}
	if status == StatusCompleted {
		return "done", nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(NULLIF(j.progress,''),j.state,'') FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=? UNION ALL SELECT COALESCE(NULLIF(progress,''),state) FROM separator_invoices WHERE order_id=? AND state<>'skipped'`, id, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	count, done := 0, 0
	failed, processing, printing := status == StatusFailed, requested, false
	for rows.Next() {
		var state string
		if err = rows.Scan(&state); err != nil {
			return "", err
		}
		count++
		switch state {
		case "completed":
			done++
		case "failed", "blocked":
			failed = true
		case "waiting", "submitting", "processing", "review":
			processing = true
		case "submitted", "printing":
			printing = true
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if count > 0 && done == count {
		return "done", nil
	}
	if failed {
		return "failed", nil
	}
	if processing {
		return "processing", nil
	}
	if printing {
		return "printing", nil
	}
	return "pending", nil
}

// ConfirmPrintDone confirms output independently of payment (including cash
// orders printed before payment). It never releases or retries a Windows job.
func (s *Service) ConfirmPrintDone(ctx context.Context, id, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM orders o WHERE o.id=? AND o.status IN ('pending_payment','paid','dispatched') AND EXISTS(SELECT 1 FROM order_lines WHERE order_id=o.id) AND NOT EXISTS(SELECT 1 FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=o.id AND (j.state IS NULL OR j.state<>'submitted')))`, id).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return errors.New("all documents must be submitted before confirming printed output")
	}
	var invoiceHeld bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM separator_invoices WHERE order_id=? AND state NOT IN ('skipped','submitted'))`, id).Scan(&invoiceHeld); err != nil {
		return err
	}
	if invoiceHeld {
		return errors.New("the separator invoice must be submitted before confirming printed output")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE separator_invoices SET progress='completed',progress_detail='Printed output confirmed by merchant',updated_at=? WHERE order_id=? AND state='submitted'`, s.now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE print_submissions SET progress='completed',progress_detail='Printed output confirmed by merchant',updated_at=? WHERE line_id IN(SELECT id FROM order_lines WHERE order_id=?)`, s.now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE orders SET print_requested=0,updated_at=? WHERE id=?", s.now().Unix(), id); err != nil {
		return err
	}
	evidence, _ := json.Marshal(map[string]string{"orderId": id, "actor": actor})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,event_type,evidence,occurred_at) VALUES(lower(hex(randomblob(16))),'order.print_completed',?,?)`, string(evidence), s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
