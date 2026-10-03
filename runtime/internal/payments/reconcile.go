package payments

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// CaptureReader verifies payment through the merchant's authenticated outbound
// API connection. LAN installations do not need an inbound public webhook.
type CaptureReader interface {
	FetchCapture(context.Context, Intent, []byte) (Webhook, bool, error)
}

func (s *Service) RunReconciliation(ctx context.Context) error {
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	for {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			log.Printf("payment reconciliation: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *Service) Reconcile(ctx context.Context) error {
	rows, err := s.database.QueryContext(ctx, `SELECT i.id FROM payment_intents i JOIN orders o ON o.id=i.order_id
WHERE (i.status IN ('redirected','pending') AND i.gateway_order_id<>'') OR (i.status='captured' AND o.status='pending_payment')
ORDER BY i.polled_at,i.created_at LIMIT 20`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.reconcileIntent(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) reconcileIntent(ctx context.Context, id string) error {
	s.mu.Lock()
	intent, err := s.loadIntent(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	_, err = s.database.ExecContext(ctx, "UPDATE payment_intents SET polled_at=? WHERE id=?", time.Now().UnixNano(), id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if intent.Status == StatusCaptured {
		s.mu.Unlock()
		s.advanceOrderToPaid(ctx, intent.OrderID)
		return nil
	}
	provider, err := s.loadProvider(ctx, intent.ProviderID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	reader, ok := s.adapters[provider.Kind].(CaptureReader)
	if !ok || !provider.Enabled {
		s.mu.Unlock()
		return nil
	}
	secret, err := s.loadSecret(ctx, provider.ID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, paid, err := reader.FetchCapture(probe, intent, secret)
	if err != nil || !paid {
		return err
	}
	if result.IntentID != intent.ID || result.GatewayOrderID != intent.GatewayOrderID || result.Status != StatusCaptured || result.AmountMinor != intent.AmountMinor || result.Currency != intent.Currency {
		return fmt.Errorf("gateway payment does not match the local intent")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadIntent(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == StatusCaptured {
		return nil
	}
	if err = s.applyWebhookTransition(ctx, result); err != nil {
		return err
	}
	return s.appendLedger(ctx, id, LedgerIntentCaptured, "capture verified using merchant API polling", "system", result.AmountMinor)
}
