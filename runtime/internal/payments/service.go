package payments

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
)

// Service owns the merchant's payment surface: provider configuration,
// local payment intents, signed authorisations, webhook delivery,
// manual approvals and the audit ledger.
type Service struct {
	database       *sql.DB
	broker         Broker
	now            func() time.Time
	newID          func() string
	encryptionSeed func() []byte
	mu             sync.Mutex
	adapters       map[Kind]MerchantProvider
	// orders transitions an order from pending_payment → paid when a
	// payment intent is captured (webhook or manual approval). This
	// unblocks the print dispatcher so the order is picked up within
	// the PollInterval of being paid.
	orders   *orders.Service
	ordersMu sync.Mutex
}

// Option mutates a Service during construction.
type Option func(*Service)

// WithBroker replaces the local stub broker with the supplied
// implementation. The local stub is the default so the service works
// in a fresh checkout without any external dependency.
func WithBroker(broker Broker) Option {
	return func(s *Service) { s.broker = broker }
}

// WithClock overrides the clock. Tests pass a fixed value so they
// can assert on timestamps deterministically.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithIDGenerator overrides the audit ID generator.
func WithIDGenerator(generator func() string) Option {
	return func(s *Service) { s.newID = generator }
}

// WithEncryptionSeed overrides the encryption seed. Tests pass a
// deterministic value so encrypted secrets can be inspected.
func WithEncryptionSeed(seed func() []byte) Option {
	return func(s *Service) { s.encryptionSeed = seed }
}

// WithProviderAdapter registers a merchant-owned gateway adapter.
// Production code wires adapters in main.go; tests can inject
// recording adapters that simulate the gateway behaviour.
func WithProviderAdapter(kind Kind, adapter MerchantProvider) Option {
	return func(s *Service) {
		if s.adapters == nil {
			s.adapters = make(map[Kind]MerchantProvider)
		}
		s.adapters[kind] = adapter
	}
}

// WithOrders wires the orders service so the payments service can
// automatically transition an order from pending_payment → paid when
// a payment intent is captured. This is the hot path for instant
// print release: the dispatcher picks up the order within the
// PollInterval (default 20 ms) of the payment being confirmed.
func WithOrders(ordersSvc *orders.Service) Option {
	return func(s *Service) { s.orders = ordersSvc }
}

// New constructs a Service bound to the supplied database. The
// encryption seed defaults to a mix of the data directory path, the
// host name and the machine identifier.
func New(database *sql.DB, dataDirectory string, options ...Option) (*Service, error) {
	if database == nil {
		return nil, fmt.Errorf("payments service requires a database")
	}
	if strings.TrimSpace(dataDirectory) == "" {
		return nil, fmt.Errorf("payments service requires a data directory")
	}
	service := &Service{
		database: database,
		now:      time.Now,
		newID:    randomID,
		encryptionSeed: func() []byte {
			return encryptionSeedFor(dataDirectory)
		},
		adapters: make(map[Kind]MerchantProvider),
	}
	for _, option := range options {
		option(service)
	}
	if service.broker == nil {
		if licensing.ControlPlanePrivateKeyHex == "" {
			service.broker = disabledBroker{}
			return service, nil
		}
		broker, err := NewLocalBroker()
		if err != nil {
			return nil, fmt.Errorf("init local broker: %w", err)
		}
		service.broker = broker
	}
	return service, nil
}

// EnsurePlatformProvider guarantees the platform Razorpay row exists.
// It is created disabled by default; the merchant enables it from the
// dashboard. The platform row is a singleton so the dashboard can
// rely on its id when constructing intents.
func (s *Service) EnsurePlatformProvider(ctx context.Context) (ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.database.QueryRowContext(ctx, `
SELECT id FROM payment_providers WHERE kind = 'razorpay_platform' LIMIT 1`)
	var id string
	if err := row.Scan(&id); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return ProviderConfig{}, err
		}
		id = s.newID()
		timestamp := s.now().UTC().Format(time.RFC3339Nano)
		_, err := s.database.ExecContext(ctx, `
INSERT INTO payment_providers (id, kind, display_name, enabled, is_default, config_json, created_at, updated_at)
VALUES (?, 'razorpay_platform', 'Platform Razorpay (signed by Print Catalyst)', 0, 0, '{}', ?, ?)`,
			id, timestamp, timestamp)
		if err != nil {
			return ProviderConfig{}, fmt.Errorf("insert platform provider: %w", err)
		}
		if err := s.appendLedger(ctx, "", LedgerProviderCreated, "platform Razorpay provider seeded", "system", 0); err != nil {
			return ProviderConfig{}, err
		}
	}
	return s.loadProvider(ctx, id)
}

// CreateProvider persists a new merchant-owned provider. The secret
// is encrypted at rest with the same AES-256-GCM key the licensing
// device key uses.
func (s *Service) CreateProvider(ctx context.Context, input ProviderInput) (ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !knownKind(input.Kind) {
		return ProviderConfig{}, ErrInvalid
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		return ProviderConfig{}, ErrInvalid
	}
	id := s.newID()
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	configJSON, err := json.Marshal(input.Config)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("encode config: %w", err)
	}
	var ciphertext []byte
	var nonce []byte
	if input.Secret != "" {
		ciphertext, nonce, err = encryptSecret([]byte(input.Secret), s.encryptionSeed())
		if err != nil {
			return ProviderConfig{}, err
		}
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("begin provider transaction: %w", err)
	}
	defer tx.Rollback()
	if input.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE payment_providers SET is_default = 0`); err != nil {
			return ProviderConfig{}, fmt.Errorf("clear default provider: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO payment_providers (id, kind, display_name, enabled, is_default, config_json, encrypted_secret, secret_nonce, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, input.Kind, input.DisplayName, boolToInt(input.Enabled), boolToInt(input.IsDefault), string(configJSON), ciphertext, nonce, timestamp, timestamp); err != nil {
		return ProviderConfig{}, fmt.Errorf("insert provider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ProviderConfig{}, fmt.Errorf("commit provider: %w", err)
	}
	if err := s.appendLedger(ctx, "", LedgerProviderCreated, "provider "+input.DisplayName+" created", "system", 0); err != nil {
		return ProviderConfig{}, err
	}
	return s.loadProvider(ctx, id)
}

// UpdateProvider updates the merchant-owned configuration. The
// secret is only replaced when the caller supplies a non-empty value;
// existing secrets are preserved otherwise.
func (s *Service) UpdateProvider(ctx context.Context, id string, input ProviderInput) (ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !knownKind(input.Kind) {
		return ProviderConfig{}, ErrInvalid
	}
	if strings.TrimSpace(id) == "" {
		return ProviderConfig{}, ErrInvalid
	}
	existing, err := s.loadProvider(ctx, id)
	if err != nil {
		return ProviderConfig{}, err
	}
	var ciphertext []byte
	var nonce []byte
	if input.Secret != "" {
		ciphertext, nonce, err = encryptSecret([]byte(input.Secret), s.encryptionSeed())
		if err != nil {
			return ProviderConfig{}, err
		}
	} else {
		ciphertext, nonce, err = s.loadEncryptedSecret(ctx, id)
		if err != nil {
			return ProviderConfig{}, err
		}
	}
	configJSON, err := json.Marshal(input.Config)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("encode config: %w", err)
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("begin provider transaction: %w", err)
	}
	defer tx.Rollback()
	if input.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE payment_providers SET is_default = 0 WHERE id != ?`, id); err != nil {
			return ProviderConfig{}, fmt.Errorf("clear default provider: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE payment_providers SET kind = ?, display_name = ?, enabled = ?, is_default = ?, config_json = ?, encrypted_secret = ?, secret_nonce = ?, updated_at = ?
WHERE id = ?`,
		input.Kind, input.DisplayName, boolToInt(input.Enabled), boolToInt(input.IsDefault), string(configJSON), ciphertext, nonce, timestamp, id); err != nil {
		return ProviderConfig{}, fmt.Errorf("update provider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ProviderConfig{}, fmt.Errorf("commit provider update: %w", err)
	}
	if err := s.appendLedger(ctx, "", LedgerProviderUpdated, "provider "+existing.DisplayName+" updated", "system", 0); err != nil {
		return ProviderConfig{}, err
	}
	return s.loadProvider(ctx, id)
}

// DeleteProvider removes a merchant-owned provider. The platform row
// cannot be deleted.
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return ErrInvalid
	}
	existing, err := s.loadProvider(ctx, id)
	if err != nil {
		return err
	}
	if existing.Kind == KindRazorpayPlatform {
		return ErrInvalid
	}
	if _, err := s.database.ExecContext(ctx, `DELETE FROM payment_providers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return s.appendLedger(ctx, "", LedgerProviderDeleted, "provider "+existing.DisplayName+" deleted", "system", 0)
}

// ListProviders returns every configured provider with secrets
// redacted.
func (s *Service) ListProviders(ctx context.Context) ([]ProviderConfig, error) {
	rows, err := s.database.QueryContext(ctx, `
SELECT id FROM payment_providers ORDER BY is_default DESC, display_name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan provider id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	providers := make([]ProviderConfig, 0, len(ids))
	for _, id := range ids {
		provider, err := s.loadProvider(ctx, id)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// LoadSecret decrypts and returns a provider's stored secret blob. Used
// by the dashboard to test the webhook endpoint with the real secret,
// without the secret ever leaving the server. The caller is responsible
// for handling the decrypted bytes safely (never log them, zero them
// after use).
func (s *Service) LoadSecret(ctx context.Context, providerID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSecret(ctx, providerID)
}

// CreateIntent persists a new payment intent. The idempotency key
// guarantees that two concurrent portal submissions cannot create
// two intents for the same order. The platform Razorpay provider
// asks the local broker for a redirect bundle; merchant-owned
// providers ask the registered adapter.
func (s *Service) CreateIntent(ctx context.Context, input IntentInput) (Intent, CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(input.OrderID) == "" || strings.TrimSpace(input.ProviderID) == "" {
		return Intent{}, CreateResult{}, ErrInvalid
	}
	if input.AmountMinor <= 0 {
		return Intent{}, CreateResult{}, ErrInvalid
	}
	if strings.TrimSpace(input.Currency) == "" || input.CurrencyMinorUnits < 0 {
		return Intent{}, CreateResult{}, ErrInvalid
	}
	provider, err := s.loadProvider(ctx, input.ProviderID)
	if err != nil {
		return Intent{}, CreateResult{}, err
	}
	if !provider.Enabled {
		return Intent{}, CreateResult{}, ErrUnconfigured
	}
	installationID, err := s.readInstallationID(ctx)
	if err != nil {
		return Intent{}, CreateResult{}, err
	}
	id := s.newID()
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		key = id
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.database.ExecContext(ctx, `
INSERT INTO payment_intents (id, order_id, installation_id, provider_id, amount_minor, currency, currency_minor_units, status, gateway_order_id, gateway_payment_id, customer_name, customer_phone, customer_email, idempotency_key, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', '', '', ?, ?, ?, ?, ?, ?)`,
		id, input.OrderID, installationID, input.ProviderID, input.AmountMinor, input.Currency, input.CurrencyMinorUnits,
		input.CustomerName, input.CustomerPhone, input.CustomerEmail, key, timestamp, timestamp)
	if err != nil {
		if isUniqueViolation(err) {
			if provider.Kind != KindRazorpayMerchant {
				return Intent{}, CreateResult{}, ErrDuplicate
			}
			var existingID string
			if err := s.database.QueryRowContext(ctx, "SELECT id FROM payment_intents WHERE idempotency_key=?", key).Scan(&existingID); err != nil {
				return Intent{}, CreateResult{}, err
			}
			existing, err := s.loadIntent(ctx, existingID)
			if err != nil {
				return Intent{}, CreateResult{}, err
			}
			if existing.OrderID != input.OrderID || existing.ProviderID != input.ProviderID || existing.AmountMinor != input.AmountMinor || existing.Currency != input.Currency {
				return Intent{}, CreateResult{}, ErrDuplicate
			}
			existing.ReturnURL = input.ReturnURL
			result, err := s.merchantLink(ctx, existing, true)
			refreshed, _ := s.loadIntent(ctx, existingID)
			return refreshed, result, err
		}
		return Intent{}, CreateResult{}, fmt.Errorf("insert payment intent: %w", err)
	}
	intent, err := s.loadIntent(ctx, id)
	if err != nil {
		return Intent{}, CreateResult{}, err
	}
	if err := s.appendAttempt(ctx, id, AttemptCreate, "intent created", 0); err != nil {
		return Intent{}, CreateResult{}, err
	}
	if err := s.appendLedger(ctx, id, LedgerIntentCreated, "payment intent created", "system", intent.AmountMinor); err != nil {
		return Intent{}, CreateResult{}, err
	}
	var result CreateResult
	switch provider.Kind {
	case KindRazorpayPlatform:
		envelope, err := s.broker.Authorize(AuthorizeRequest{
			InstallationID:     installationID,
			OrderID:            input.OrderID,
			GatewayPaymentID:   id,
			AmountMinor:        input.AmountMinor,
			Currency:           input.Currency,
			CurrencyMinorUnits: input.CurrencyMinorUnits,
			AuthorizedAt:       s.now().UTC(),
		})
		if err != nil {
			return intent, CreateResult{}, fmt.Errorf("platform authorise: %w", err)
		}
		payload, err := VerifyAuthorizationSignature(envelope, intent)
		if err != nil {
			return intent, CreateResult{}, err
		}
		if err := s.persistAuthorization(ctx, intent, envelope, payload); err != nil {
			return intent, CreateResult{}, err
		}
		if err := s.updateIntentStatus(ctx, id, StatusAuthorized, "", payload.GatewayPaymentID, timestamp); err != nil {
			return intent, CreateResult{}, err
		}
		if err := s.appendAttempt(ctx, id, AttemptVerify, "platform signature verified", 0); err != nil {
			return intent, CreateResult{}, err
		}
		if err := s.appendLedger(ctx, id, LedgerIntentAuthorized, "signed by platform broker", "system", intent.AmountMinor); err != nil {
			return intent, CreateResult{}, err
		}
		result = CreateResult{GatewayOrderID: id, GatewayPaymentID: payload.GatewayPaymentID}
	case KindRazorpayMerchant:
		intent.ReturnURL = input.ReturnURL
		result, err = s.merchantLink(ctx, intent, false)
		if err != nil {
			return intent, CreateResult{}, err
		}

	default:
		return Intent{}, CreateResult{}, ErrUnconfigured
	}
	refreshed, err := s.loadIntent(ctx, id)
	if err != nil {
		return Intent{}, CreateResult{}, err
	}
	return refreshed, result, nil
}

// RecordWebhook persists a webhook delivery and applies its effect
// to the local intent. The idempotency key deduplicates retried
// deliveries; the gateway event id is the merchant-visible label.
//
// The merchant-owned provider kind's adapter is responsible for
// verifying the signature against the provider secret.
func (s *Service) RecordWebhook(ctx context.Context, providerID string, rawBody []byte, signature string) (WebhookEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(providerID) == "" {
		return WebhookEvent{}, ErrInvalid
	}
	provider, err := s.loadProvider(ctx, providerID)
	if err != nil {
		return WebhookEvent{}, err
	}
	adapter, ok := s.adapters[provider.Kind]
	if !ok {
		return WebhookEvent{}, fmt.Errorf("%w: no adapter for %s", ErrUnconfigured, provider.Kind)
	}
	secret, err := s.loadSecret(ctx, providerID)
	if err != nil {
		return WebhookEvent{}, err
	}
	if err := adapter.VerifyWebhookSignature(rawBody, signature, secret); err != nil {
		_ = s.appendLedger(ctx, "", LedgerWebhookRejected, fmt.Sprintf("webhook signature rejected: %v", err), "system", 0)
		return WebhookEvent{}, fmt.Errorf("%w: %v", ErrWebhook, err)
	}
	webhook, err := adapter.ParseWebhook(rawBody)
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: parse: %v", ErrWebhook, err)
	}
	intent, err := s.loadIntent(ctx, webhook.IntentID)
	if err != nil {
		return WebhookEvent{}, err
	}
	if intent.ProviderID != providerID {
		return WebhookEvent{}, ErrWebhook
	}
	id := s.newID()
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	key := strings.TrimSpace(webhook.IdempotencyKey)
	if key == "" {
		key = id
	}
	_, err = s.database.ExecContext(ctx, `
INSERT INTO payment_webhook_events (id, provider_id, gateway_event_id, intent_id, idempotency_key, raw_payload, signature, verified, received_at, processed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, providerID, webhook.GatewayEventID, webhook.IntentID, key, rawBody, signature, timestamp, timestamp)
	if err != nil {
		if isUniqueViolation(err) {
			_ = s.appendLedger(ctx, webhook.IntentID, LedgerWebhookDuplicate, "duplicate webhook ignored", "system", 0)
			if err := s.applyWebhookTransition(ctx, webhook); err != nil {
				return WebhookEvent{}, err
			}
			return WebhookEvent{}, ErrDuplicate
		}
		return WebhookEvent{}, fmt.Errorf("insert webhook event: %w", err)
	}
	if err := s.appendAttempt(ctx, webhook.IntentID, AttemptWebhook, fmt.Sprintf("webhook received: %s", webhook.Status), 0); err != nil {
		return WebhookEvent{}, err
	}
	if err := s.applyWebhookTransition(ctx, webhook); err != nil {
		return WebhookEvent{}, err
	}
	if err := s.appendLedger(ctx, webhook.IntentID, LedgerWebhookProcessed, fmt.Sprintf("webhook applied: %s", webhook.Status), "system", webhook.AmountMinor); err != nil {
		return WebhookEvent{}, err
	}
	return s.loadWebhook(ctx, id)
}

// ManualApprove records an explicit cash / UPI / bank-transfer
// approval. The intent is moved to StatusCaptured so the order can
// proceed to print. The brief requires manual approval to never be
// presented as gateway-verified payment: the Manual field is
// surfaced separately in the dashboard.
func (s *Service) ManualApprove(ctx context.Context, intentID string, method Method, reference string, amountMinor int64, currency string, approvedBy string, note string) (ManualApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(intentID) == "" {
		return ManualApproval{}, ErrInvalid
	}
	if amountMinor <= 0 || strings.TrimSpace(currency) == "" {
		return ManualApproval{}, ErrInvalid
	}
	if strings.TrimSpace(approvedBy) == "" {
		return ManualApproval{}, ErrInvalid
	}
	intent, err := s.loadIntent(ctx, intentID)
	if err != nil {
		return ManualApproval{}, err
	}
	if intent.AmountMinor != amountMinor {
		return ManualApproval{}, ErrAmount
	}
	if intent.Currency != currency {
		return ManualApproval{}, ErrCurrency
	}
	if intent.Status == StatusCaptured {
		return ManualApproval{}, ErrTransition
	}
	approvalID := s.newID()
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ManualApproval{}, fmt.Errorf("begin manual approval transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO manual_payments (id, intent_id, method, reference, amount_minor, currency, approved_by, note, approved_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		approvalID, intentID, method, reference, amountMinor, currency, approvedBy, note, timestamp); err != nil {
		return ManualApproval{}, fmt.Errorf("insert manual approval: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_intents SET status = 'captured', updated_at = ? WHERE id = ?`, timestamp, intentID); err != nil {
		return ManualApproval{}, fmt.Errorf("update intent to captured: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ManualApproval{}, fmt.Errorf("commit manual approval: %w", err)
	}
	if err := s.appendAttempt(ctx, intentID, AttemptManualApprove, fmt.Sprintf("manual %s approval by %s", method, approvedBy), amountMinor); err != nil {
		return ManualApproval{}, err
	}
	if err := s.appendLedger(ctx, intentID, LedgerManualApproved, fmt.Sprintf("manual %s approval by %s", method, approvedBy), approvedBy, amountMinor); err != nil {
		return ManualApproval{}, err
	}
	if err := s.appendLedger(ctx, intentID, LedgerIntentCaptured, "manual approval captured", approvedBy, amountMinor); err != nil {
		return ManualApproval{}, err
	}
	// Instantly promote the order to paid so the print dispatcher fires
	// within the next PollInterval. Cash-on-counter orders are approved
	// by the merchant pressing a button in the dashboard; the document
	// should be in the printer within milliseconds of that tap.
	if intent.OrderID != "" {
		s.advanceOrderToPaid(context.Background(), intent.OrderID)
	}
	return ManualApproval{
		ID:          approvalID,
		IntentID:    intentID,
		Method:      method,
		Reference:   reference,
		AmountMinor: amountMinor,
		Currency:    currency,
		ApprovedBy:  approvedBy,
		Note:        note,
		ApprovedAt:  s.now().UTC(),
	}, nil
}

// ManualReject records an explicit rejection (the merchant
// decided not to honour a cash / UPI attempt). The intent is moved
// to StatusFailed so the dashboard can show the merchant's
// decision.
func (s *Service) ManualReject(ctx context.Context, intentID string, approvedBy string, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(intentID) == "" {
		return ErrInvalid
	}
	if strings.TrimSpace(approvedBy) == "" {
		return ErrInvalid
	}
	intent, err := s.loadIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == StatusCaptured {
		return ErrTransition
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.database.ExecContext(ctx, `UPDATE payment_intents SET status = 'failed', updated_at = ? WHERE id = ?`, timestamp, intentID); err != nil {
		return fmt.Errorf("update intent to failed: %w", err)
	}
	if err := s.appendAttempt(ctx, intentID, AttemptManualReject, note, 0); err != nil {
		return err
	}
	return s.appendLedger(ctx, intentID, LedgerManualRejected, note, approvedBy, 0)
}

// ListIntents returns every intent newest-first. The dashboard uses
// this to render the reconciliation list.
func (s *Service) ListIntents(ctx context.Context, limit int) ([]Intent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.database.QueryContext(ctx, `
SELECT id FROM payment_intents ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list intents: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan intent id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	intents := make([]Intent, 0, len(ids))
	for _, id := range ids {
		intent, err := s.loadIntent(ctx, id)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, nil
}

// Ledger returns the most recent ledger entries newest-first.
func (s *Service) Ledger(ctx context.Context, limit int) ([]LedgerEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.database.QueryContext(ctx, `
SELECT id, intent_id, event_type, detail, actor, COALESCE(amount_minor, 0), occurred_at
FROM payment_ledger ORDER BY occurred_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer rows.Close()
	var entries []LedgerEntry
	for rows.Next() {
		var entry LedgerEntry
		var occurredAt string
		if err := rows.Scan(&entry.ID, &entry.IntentID, &entry.EventType, &entry.Detail, &entry.Actor, &entry.AmountMinor, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan ledger entry: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, fmt.Errorf("parse ledger timestamp: %w", err)
		}
		entry.OccurredAt = parsed
		entries = append(entries, entry)
	}
	if entries == nil {
		entries = []LedgerEntry{}
	}
	return entries, rows.Err()
}

// readInstallationID reads the current installation id from the
// licensing singleton row. The payments service depends on the
// licensing table being seeded (the licensing service Ensure
// guarantees this on the first read).
func (s *Service) readInstallationID(ctx context.Context) (string, error) {
	var id string
	row := s.database.QueryRowContext(ctx, `SELECT installation_id FROM installation_keys WHERE singleton = 1`)
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrUnconfigured
		}
		return "", fmt.Errorf("read installation id: %w", err)
	}
	return id, nil
}

// loadProvider reads a provider row by id and returns the
// redacted config view.
func (s *Service) loadProvider(ctx context.Context, id string) (ProviderConfig, error) {
	var provider ProviderConfig
	var kind string
	var displayName string
	var enabled int
	var isDefault int
	var configJSON string
	var createdAt, updatedAt string
	var secret []byte
	row := s.database.QueryRowContext(ctx, `
SELECT id, kind, display_name, enabled, is_default, config_json, COALESCE(encrypted_secret, X''), created_at, updated_at
FROM payment_providers WHERE id = ?`, id)
	if err := row.Scan(&provider.ID, &kind, &displayName, &enabled, &isDefault, &configJSON, &secret, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return provider, ErrNotFound
		}
		return provider, fmt.Errorf("read provider: %w", err)
	}
	provider.Kind = Kind(kind)
	provider.DisplayName = displayName
	provider.Enabled = enabled == 1
	provider.IsDefault = isDefault == 1
	provider.HasSecret = len(secret) > 0
	provider.Config = map[string]any{}
	if strings.TrimSpace(configJSON) != "" {
		if err := json.Unmarshal([]byte(configJSON), &provider.Config); err != nil {
			return provider, fmt.Errorf("decode provider config: %w", err)
		}
	}
	parsedCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return provider, fmt.Errorf("parse created_at: %w", err)
	}
	parsedUpdated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return provider, fmt.Errorf("parse updated_at: %w", err)
	}
	provider.CreatedAt = parsedCreated
	provider.UpdatedAt = parsedUpdated
	return provider, nil
}

// loadEncryptedSecret reads the encrypted_secret + nonce columns
// without decrypting. Used by UpdateProvider when the caller does
// not supply a new secret.
func (s *Service) loadEncryptedSecret(ctx context.Context, id string) ([]byte, []byte, error) {
	var secret []byte
	var nonce []byte
	row := s.database.QueryRowContext(ctx, `
SELECT COALESCE(encrypted_secret, X''), COALESCE(secret_nonce, X'')
FROM payment_providers WHERE id = ?`, id)
	if err := row.Scan(&secret, &nonce); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("read encrypted secret: %w", err)
	}
	return secret, nonce, nil
}

// loadSecret decrypts the provider's stored secret. Used by the
// merchant gateway adapters on Create / VerifyWebhookSignature.
func (s *Service) loadSecret(ctx context.Context, id string) ([]byte, error) {
	secret, nonce, err := s.loadEncryptedSecret(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(secret) == 0 {
		return nil, nil
	}
	plaintext, err := decryptSecret(secret, nonce, s.encryptionSeed())
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

// loadIntent reads an intent row by id.
func (s *Service) loadIntent(ctx context.Context, id string) (Intent, error) {
	var intent Intent
	var status string
	var createdAt, updatedAt string
	row := s.database.QueryRowContext(ctx, `
SELECT id, order_id, installation_id, provider_id, amount_minor, currency, currency_minor_units, status,
       COALESCE(gateway_order_id, ''), COALESCE(gateway_payment_id, ''), customer_name, customer_phone, customer_email,
       idempotency_key, created_at, updated_at
FROM payment_intents WHERE id = ?`, id)
	if err := row.Scan(&intent.ID, &intent.OrderID, &intent.InstallationID, &intent.ProviderID,
		&intent.AmountMinor, &intent.Currency, &intent.CurrencyMinorUnits, &status,
		&intent.GatewayOrderID, &intent.GatewayPaymentID, &intent.CustomerName, &intent.CustomerPhone, &intent.CustomerEmail,
		&intent.IdempotencyKey, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return intent, ErrNotFound
		}
		return intent, fmt.Errorf("read intent: %w", err)
	}
	intent.Status = Status(status)
	parsedCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return intent, fmt.Errorf("parse created_at: %w", err)
	}
	parsedUpdated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return intent, fmt.Errorf("parse updated_at: %w", err)
	}
	intent.CreatedAt = parsedCreated
	intent.UpdatedAt = parsedUpdated
	return intent, nil
}

// updateIntentStatus moves an intent to a new status and records the
// gateway identifiers the gateway returned.
func (s *Service) updateIntentStatus(ctx context.Context, id string, status Status, gatewayOrderID, gatewayPaymentID, timestamp string) error {
	if _, err := s.database.ExecContext(ctx, `
UPDATE payment_intents SET status = ?, gateway_order_id = COALESCE(NULLIF(?, ''), gateway_order_id), gateway_payment_id = COALESCE(NULLIF(?, ''), gateway_payment_id), updated_at = ?
WHERE id = ?`, status, gatewayOrderID, gatewayPaymentID, timestamp, id); err != nil {
		return fmt.Errorf("update intent status: %w", err)
	}
	return nil
}

// persistAuthorization writes the signed envelope as the current
// authorisation for the intent.
func (s *Service) persistAuthorization(ctx context.Context, intent Intent, envelope SignedAuthorization, payload signedAuthorizationPayload) error {
	authID := s.newID()
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.database.ExecContext(ctx, `
INSERT INTO payment_authorizations (id, intent_id, installation_id, order_id, gateway_payment_id, amount_minor, currency, currency_minor_units, authorized_at, nonce, payload, signature, verified_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(intent_id) DO UPDATE SET
    gateway_payment_id = excluded.gateway_payment_id,
    amount_minor = excluded.amount_minor,
    currency = excluded.currency,
    currency_minor_units = excluded.currency_minor_units,
    authorized_at = excluded.authorized_at,
    nonce = excluded.nonce,
    payload = excluded.payload,
    signature = excluded.signature,
    verified_at = excluded.verified_at`,
		authID, intent.ID, payload.InstallationID, payload.OrderID, payload.GatewayPaymentID, payload.AmountMinor, payload.Currency, payload.CurrencyMinorUnits, payload.AuthorizedAt.UTC().Format(time.RFC3339Nano), payload.Nonce, envelope.Payload, envelope.Signature, timestamp)
	if err != nil {
		return fmt.Errorf("insert payment authorisation: %w", err)
	}
	return nil
}

// applyWebhookTransition moves the intent to the status the gateway
// asserted.
func (s *Service) applyWebhookTransition(ctx context.Context, webhook Webhook) error {
	if strings.TrimSpace(webhook.IntentID) == "" {
		return nil
	}
	intent, err := s.loadIntent(ctx, webhook.IntentID)
	if err != nil {
		return err
	}
	if intent.AmountMinor != webhook.AmountMinor {
		return ErrAmount
	}
	if intent.Currency != webhook.Currency {
		return ErrCurrency
	}
	target := webhook.Status
	if !validWebhookTransition(intent.Status, target) && !(intent.Status == StatusCaptured && target == StatusCaptured) {
		return ErrTransition
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	if target == StatusCaptured {
		err = s.persistVerifiedCapture(ctx, intent, webhook, timestamp)
	} else {
		err = s.updateIntentStatus(ctx, intent.ID, target, webhook.GatewayOrderID, webhook.GatewayPaymentID, timestamp)
	}
	if err != nil {
		return err
	}
	// Payment and print release are separate. A verified capture queues pickup
	// issuance; the dispatcher still requires a physical kiosk claim.
	if target == StatusCaptured && intent.OrderID != "" {
		s.advanceOrderToPaid(context.Background(), intent.OrderID)
	}
	return nil
}

// loadWebhook reads a webhook event row by id.
func (s *Service) loadWebhook(ctx context.Context, id string) (WebhookEvent, error) {
	var event WebhookEvent
	var intentID string
	var receivedAt string
	var processedAt sql.NullString
	row := s.database.QueryRowContext(ctx, `
SELECT id, provider_id, gateway_event_id, COALESCE(intent_id, ''), idempotency_key, verified, received_at, processed_at
FROM payment_webhook_events WHERE id = ?`, id)
	if err := row.Scan(&event.ID, &event.ProviderID, &event.GatewayEventID, &intentID, &event.IdempotencyKey, &event.Verified, &receivedAt, &processedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return event, ErrNotFound
		}
		return event, fmt.Errorf("read webhook event: %w", err)
	}
	event.IntentID = intentID
	parsedReceived, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		return event, fmt.Errorf("parse webhook received_at: %w", err)
	}
	event.ReceivedAt = parsedReceived
	if processedAt.Valid {
		parsed, perr := time.Parse(time.RFC3339Nano, processedAt.String)
		if perr == nil {
			event.ProcessedAt = parsed
		}
	}
	return event, nil
}

// appendAttempt records a row in the payment_attempts log.
func (s *Service) appendAttempt(ctx context.Context, intentID string, kind AttemptKind, detail string, amountMinor int64) error {
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.database.ExecContext(ctx, `
INSERT INTO payment_attempts (id, intent_id, attempt_kind, status, detail, occurred_at)
VALUES (?, ?, ?, 'ok', ?, ?)`,
		s.newID(), intentID, kind, detail, timestamp)
	if err != nil {
		return fmt.Errorf("record payment attempt: %w", err)
	}
	return nil
}

// appendLedger records a row in the payment_ledger audit log.
func (s *Service) appendLedger(ctx context.Context, intentID string, eventType string, detail string, actor string, amountMinor int64) error {
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.database.ExecContext(ctx, `
INSERT INTO payment_ledger (id, intent_id, event_type, detail, actor, amount_minor, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.newID(), intentID, eventType, detail, actor, amountMinor, timestamp)
	if err != nil {
		return fmt.Errorf("record payment ledger: %w", err)
	}
	return nil
}

// encryptionSeedFor builds a deterministic seed from the data
// directory path so the secret encryption key is bound to the
// installation. Same approach as licensing.
func encryptionSeedFor(dataDirectory string) []byte {
	return licensing.SeedFromEnvironment(dataDirectory)
}

// encryptSecret / decryptSecret wrap a secret with AES-256-GCM using
// the same derivation the licensing package uses for the device
// private key. The functions live here so the payments package can
// remain self-contained.
func encryptSecret(plaintext []byte, seed []byte) ([]byte, []byte, error) {
	if len(seed) == 0 {
		return nil, nil, fmt.Errorf("empty encryption seed")
	}
	key := licensing.DeriveEncryptionKey(seed)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	prefix := []byte("pc-onpremise-payment-secret-v1:")
	payload := append(prefix, plaintext...)
	ciphertext := gcm.Seal(nil, nonce, payload, nil)
	return ciphertext, nonce, nil
}

// decryptSecret reverses encryptSecret. The leading prefix is
// stripped from the recovered plaintext so the caller sees only the
// raw secret.
func decryptSecret(ciphertext []byte, nonce []byte, seed []byte) ([]byte, error) {
	if len(seed) == 0 {
		return nil, fmt.Errorf("empty encryption seed")
	}
	key := licensing.DeriveEncryptionKey(seed)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, ErrInvalid
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrInvalid
	}
	prefix := []byte("pc-onpremise-payment-secret-v1:")
	if !bytes.HasPrefix(plaintext, prefix) {
		return nil, ErrInvalid
	}
	return plaintext[len(prefix):], nil
}

// knownKind reports whether the supplied kind is recognised by the
// service.
func knownKind(kind Kind) bool {
	switch kind {
	case KindRazorpayPlatform, KindRazorpayMerchant, KindManual:
		return true
	}
	return false
}

// validWebhookTransition enforces the state machine the gateway
// drives the intent through. The intent can only move forward
// through the recognised lifecycle states. Razorpay's Payment Link
// flow moves directly from redirected to captured on
// payment_link.paid (it never sends a separate authorised event),
// so that transition must be accepted alongside the platform
// broker's pending → authorised → captured path. pending → captured
// is deliberately rejected so a webhook can never skip the
// redirect/authorise step and promote an unverified intent.
func validWebhookTransition(from, to Status) bool {
	switch from {
	case StatusPending:
		return to == StatusRedirected || to == StatusAuthorized || to == StatusFailed || to == StatusCancelled || to == StatusExpired
	case StatusRedirected:
		// The merchant-owned Razorpay adapter lands here after
		// Create. payment_link.paid carries a captured event; we
		// accept it directly. Authorised is allowed so the same
		// adapter can support a future Standard Checkout flow.
		return to == StatusAuthorized || to == StatusCaptured || to == StatusFailed || to == StatusCancelled || to == StatusExpired
	case StatusAuthorized:
		return to == StatusCaptured || to == StatusFailed || to == StatusExpired
	case StatusCaptured, StatusFailed, StatusCancelled, StatusExpired:
		return false
	}
	return false
}

// isUniqueViolation reports whether the supplied error is a SQLite
// UNIQUE constraint failure.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

// boolToInt converts a boolean to the 0/1 value the SQLite CHECK
// constraints expect.
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// randomID returns a 32-character hex identifier.
func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("secure random ID generation failed: %v", err))
	}
	return hex.EncodeToString(buffer)
}

// advanceOrderToPaid promotes an order from pending_payment → paid when
// a payment intent has been captured. This is the instant-print trigger:
// the dispatcher polls for orders with status = 'paid' every PollInterval
// (default 20 ms), so the document is submitted to the printer within
// 20 ms of the payment being confirmed.
//
// The call is asynchronous (goroutine) so a slow or failing orders service
// does not block the payment webhook response. A missed transition is
// harmless — the merchant can manually promote the order from the
// dashboard, or the retry would succeed on a subsequent payment event.
func (s *Service) advanceOrderToPaid(ctx context.Context, orderID string) {
	s.ordersMu.Lock()
	ordersSvc := s.orders
	s.ordersMu.Unlock()
	if ordersSvc == nil {
		return
	}
	go func() {
		_, err := ordersSvc.SetStatus(context.Background(), orderID, orders.StatusPaid, "system")
		if err != nil && !errors.Is(err, orders.ErrNotFound) && !errors.Is(err, orders.ErrInvalidTransition) {
			// Log but do not surface: the payment has already been captured.
			// The worst case is a delayed print that the merchant can
			// manually trigger from the dashboard.
			log.Printf("advanceOrderToPaid(%s): SetStatus error: %v", orderID, err)
		}
	}()
}

// merchantLink is called with s.mu held, serialising retries for one intent.
func (s *Service) merchantLink(ctx context.Context, intent Intent, retry bool) (CreateResult, error) {
	if intent.Status != StatusPending && intent.Status != StatusRedirected {
		return CreateResult{}, fmt.Errorf("payment is %s; refresh the order status", intent.Status)
	}
	var savedURL string
	var expires int64
	if err := s.database.QueryRowContext(ctx, "SELECT redirect_url,link_expires_at FROM payment_intents WHERE id=?", intent.ID).Scan(&savedURL, &expires); err != nil {
		return CreateResult{}, err
	}
	if savedURL != "" {
		if expires > 0 && expires <= s.now().Unix() {
			return CreateResult{}, fmt.Errorf("payment link expired; ask the counter to resolve this order")
		}
		return CreateResult{GatewayOrderID: intent.GatewayOrderID, RedirectURL: savedURL, ExpiresAt: time.Unix(expires, 0)}, nil
	}
	adapter, ok := s.adapters[KindRazorpayMerchant]
	if !ok {
		return CreateResult{}, ErrUnconfigured
	}
	secret, err := s.loadSecret(ctx, intent.ProviderID)
	if err != nil {
		return CreateResult{}, err
	}
	var result CreateResult
	if retry {
		recoverer, ok := adapter.(interface {
			RecoverCreate(context.Context, Intent, []byte) (CreateResult, error)
		})
		if !ok {
			return CreateResult{}, fmt.Errorf("gateway cannot safely recover this payment; ask the counter for help")
		}
		result, err = recoverer.RecoverCreate(ctx, intent, secret)
	} else {
		result, err = adapter.Create(intent, secret)
	}
	if err != nil {
		return CreateResult{}, err
	}
	if result.RedirectURL == "" || result.GatewayOrderID == "" {
		return CreateResult{}, fmt.Errorf("gateway did not return a payment link")
	}
	_, err = s.database.ExecContext(ctx, `UPDATE payment_intents SET status='redirected',gateway_order_id=?,redirect_url=?,link_expires_at=?,updated_at=? WHERE id=?`, result.GatewayOrderID, result.RedirectURL, result.ExpiresAt.Unix(), s.now().UTC().Format(time.RFC3339Nano), intent.ID)
	if err != nil {
		return CreateResult{}, err
	}
	if err = s.appendLedger(ctx, intent.ID, LedgerIntentRedirected, "payment link saved", "system", intent.AmountMinor); err != nil {
		return CreateResult{}, err
	}
	return result, nil
}
