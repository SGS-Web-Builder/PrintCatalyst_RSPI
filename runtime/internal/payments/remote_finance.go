package payments

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type RefundResult struct {
	ID        string `json:"id"`
	PaymentID string `json:"payment_id"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
}
type Settlement struct {
	ID        string `json:"id"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"`
	Fees      int64  `json:"fees"`
	Tax       int64  `json:"tax"`
	UTR       string `json:"utr"`
	CreatedAt int64  `json:"created_at"`
}
type RemoteFinance interface {
	Refund(context.Context, string, string, int64, []byte) (RefundResult, error)
	RefundStatus(context.Context, string, []byte) (RefundResult, error)
	Settlements(context.Context, int, []byte) ([]Settlement, error)
}

// RefundUnreleased journals one immutable full-refund request per order before any
// external call. It excludes orders whose physical release has already begun.
func (s *Service) RefundUnreleased(ctx context.Context, order, reason string) (RefundResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 240 {
		return RefundResult{}, errors.New("a refund reason is required (maximum 240 characters)")
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return RefundResult{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE kiosk_control SET paused=paused WHERE singleton=1"); err != nil {
		return RefundResult{}, err
	}
	var intent, provider, payment, currency, key, gateway, state string
	var amount int64
	err = tx.QueryRowContext(ctx, "SELECT intent_id,provider_id,payment_id,amount,currency,request_id,gateway_id,state FROM remote_refunds WHERE order_id=?", order).Scan(&intent, &provider, &payment, &amount, &currency, &key, &gateway, &state)
	if errors.Is(err, sql.ErrNoRows) {
		var released bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kiosk_releases WHERE order_id=? UNION ALL SELECT 1 FROM print_submissions s JOIN order_lines l ON l.id=s.line_id WHERE l.order_id=?)`, order, order).Scan(&released); err != nil {
			return RefundResult{}, err
		}
		if released {
			return RefundResult{}, errors.New("printing has been released; inspect physical output and handle this refund in Razorpay")
		}
		err = tx.QueryRowContext(ctx, `SELECT i.id,i.provider_id,v.payment_reference,i.amount_minor,i.currency FROM kiosk_verified_payments v JOIN payment_intents i ON i.id=v.intent_id JOIN orders o ON o.id=v.order_id JOIN payment_providers p ON p.id=i.provider_id WHERE v.order_id=? AND o.status='paid' AND i.status='captured' AND i.gateway_payment_id=v.payment_reference AND i.amount_minor=o.total_minor AND i.currency=o.currency AND p.kind='razorpay_merchant'`, order).Scan(&intent, &provider, &payment, &amount, &currency)
		if err != nil {
			return RefundResult{}, errors.New("verified captured payment unavailable for refund")
		}
		key = "pc-refund-" + order
		_, err = tx.ExecContext(ctx, "INSERT INTO remote_refunds(order_id,intent_id,provider_id,payment_id,amount,currency,request_id,state,reason,updated_at) VALUES(?,?,?,?,?,?,?,'pending',?,?)", order, intent, provider, payment, amount, currency, key, reason, time.Now().Unix())
		if err != nil {
			return RefundResult{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO remote_audit(action,detail,occurred_at) VALUES('refund.requested',?,?)", order, time.Now().Unix())
		if err != nil {
			return RefundResult{}, err
		}
	} else if err != nil {
		return RefundResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return RefundResult{}, err
	}
	config, err := s.loadProvider(ctx, provider)
	if err != nil {
		return RefundResult{}, err
	}
	adapter, ok := s.adapters[config.Kind].(RemoteFinance)
	if !ok {
		return RefundResult{}, errors.New("gateway does not support remote finance")
	}
	secret, err := s.loadSecret(ctx, provider)
	if err != nil {
		return RefundResult{}, err
	}
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	var result RefundResult
	if gateway != "" {
		result, err = adapter.RefundStatus(ctx, gateway, secret)
	} else {
		result, err = adapter.Refund(ctx, payment, key, amount, secret)
	}
	if err != nil {
		s.database.ExecContext(ctx, "UPDATE remote_refunds SET state='review',updated_at=? WHERE order_id=?", time.Now().Unix(), order)
		return result, errors.New("refund outcome requires review; retry this same order to reconcile, never create a second refund")
	}
	if result.ID == "" || result.PaymentID != payment || result.Amount != amount || result.Currency != currency || (result.Status != "pending" && result.Status != "processed" && result.Status != "failed") {
		return RefundResult{}, errors.New("refund identity or amount mismatch; review required")
	}
	_, err = s.database.ExecContext(ctx, "UPDATE remote_refunds SET gateway_id=?,state=?,updated_at=? WHERE order_id=?", result.ID, result.Status, time.Now().Unix(), order)
	return result, err
}
func (s *Service) RemoteSettlements(ctx context.Context, provider string, skip int) ([]Settlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if skip < 0 || skip > 100000 {
		return nil, errors.New("invalid page")
	}
	config, err := s.loadProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	adapter, ok := s.adapters[config.Kind].(RemoteFinance)
	if !ok {
		return nil, errors.New("gateway does not support settlements")
	}
	secret, err := s.loadSecret(ctx, provider)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	return adapter.Settlements(ctx, skip, secret)
}
