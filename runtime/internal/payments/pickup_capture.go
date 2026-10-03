package payments

import (
	"context"
	"database/sql"
	"time"
)

// persistVerifiedCapture atomically records payment and the recoverable pickup
// work item. Call only after authenticated gateway verification, never manual approval.
func (s *Service) persistVerifiedCapture(ctx context.Context, intent Intent, w Webhook, timestamp string) error {
	if intent.GatewayOrderID == "" { // Legacy non-link flows cannot authorize kiosk pickup.
		// Never learn an expected link from the callback itself: a replay would
		// otherwise turn that unbound value into apparent verification evidence.
		return s.updateIntentStatus(ctx, intent.ID, StatusCaptured, "", w.GatewayPaymentID, timestamp)
	}
	if w.GatewayOrderID != intent.GatewayOrderID || w.GatewayPaymentID == "" {
		return ErrWebhook
	}
	if intent.Status == StatusCaptured && intent.GatewayPaymentID != w.GatewayPaymentID {
		return ErrWebhook
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var amount int64
	var currency string
	var units int
	err = tx.QueryRowContext(ctx, "SELECT total_minor,currency,currency_minor_units FROM orders WHERE id=?", intent.OrderID).Scan(&amount, &currency, &units)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if amount != intent.AmountMinor {
			return ErrAmount
		}
		if currency != intent.Currency || units != intent.CurrencyMinorUnits {
			return ErrCurrency
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE payment_intents SET status='captured',gateway_payment_id=?,updated_at=? WHERE id=?`, w.GatewayPaymentID, timestamp, intent.ID)
	if err != nil {
		return err
	}
	// Snapshot must match the submitted server-priced order and merchant provider.
	_, err = tx.ExecContext(ctx, `INSERT INTO kiosk_verified_payments(order_id,intent_id,payment_reference,verified_at)
 SELECT o.id,i.id,?,? FROM orders o JOIN payment_intents i ON i.order_id=o.id
 JOIN payment_providers p ON p.id=i.provider_id
 WHERE i.id=? AND p.kind='razorpay_merchant' AND o.submitted_at>0
 AND o.total_minor=i.amount_minor AND o.currency=i.currency AND o.currency_minor_units=i.currency_minor_units
 AND o.status IN ('pending_payment','paid')
 ON CONFLICT(order_id) DO NOTHING`, w.GatewayPaymentID, time.Now().Unix(), intent.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
