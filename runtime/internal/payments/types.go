// Package payments owns the merchant's payment surface: the configured
// providers, the local payment intents that bridge the portal to the
// gateway, the signed authorisations the platform Razorpay broker
// returns, the webhook receiver with idempotency tracking and the
// manual cash / UPI approval flow. The package is intentionally
// narrow: every commercial operation in the dashboard goes through
// it, and every status transition lands in the audit log so a support
// call can replay the exact sequence.
package payments

import (
	"errors"
	"time"
)

// Sentinel errors map to HTTP status codes in the owner endpoint
// layer. The mapping mirrors licensing and notifications.
var (
	ErrInvalid        = errors.New("payment configuration is invalid")
	ErrUnconfigured   = errors.New("payment provider is not configured")
	ErrNotFound       = errors.New("payment intent not found")
	ErrSignature      = errors.New("payment authorisation signature verification failed")
	ErrMismatch       = errors.New("payment authorisation does not match this intent")
	ErrDuplicate      = errors.New("payment idempotency key already used")
	ErrCurrency       = errors.New("payment currency does not match the order currency")
	ErrAmount         = errors.New("payment amount does not match the order amount")
	ErrWebhook        = errors.New("payment webhook verification failed")
	ErrTransition     = errors.New("payment status transition is not allowed")
	ErrManualDisabled = errors.New("manual payment is not enabled for this provider")
)

// Kind enumerates the supported provider types. The local service
// validates the row against this set so a malicious POST cannot
// smuggle in a non-existent provider.
type Kind string

const (
	KindRazorpayPlatform Kind = "razorpay_platform"
	KindRazorpayMerchant Kind = "razorpay_merchant"
	KindManual           Kind = "manual"
)

// Status is the lifecycle state of a payment intent.
type Status string

const (
	StatusPending    Status = "pending"
	StatusRedirected Status = "redirected"
	StatusAuthorized Status = "authorized"
	StatusCaptured   Status = "captured"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusExpired    Status = "expired"
)

// Method is the manual payment kind. Cash and UPI are the explicit
// merchant options; bank_transfer and other are recorded for
// completeness.
type Method string

const (
	MethodCash         Method = "cash"
	MethodUPI          Method = "upi"
	MethodBankTransfer Method = "bank_transfer"
	MethodOther        Method = "other"
)

// ProviderConfig is the merchant-owned configuration for a payment
// provider. Secrets are encrypted at rest; the Service loads and
// decrypts them on demand and never returns the plaintext over the
// HTTP layer.
type ProviderConfig struct {
	ID          string         `json:"id"`
	Kind        Kind           `json:"kind"`
	DisplayName string         `json:"displayName"`
	Enabled     bool           `json:"enabled"`
	IsDefault   bool           `json:"isDefault"`
	Config      map[string]any `json:"config"`
	HasSecret   bool           `json:"hasSecret"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// ProviderInput is the create / update body the HTTP layer accepts.
// Secret is optional; when omitted the existing secret is preserved.
type ProviderInput struct {
	Kind        Kind           `json:"kind"`
	DisplayName string         `json:"displayName"`
	Enabled     bool           `json:"enabled"`
	IsDefault   bool           `json:"isDefault"`
	Config      map[string]any `json:"config"`
	Secret      string         `json:"secret,omitempty"`
}

// Intent is the local record of a payment attempt. The portal creates
// one before contacting the gateway; the gateway's identifiers are
// filled in once known; the signed authorisation closes the loop.
type Intent struct {
	// ReturnURL is supplied by the trusted portal handler, never by customer JSON.
	ReturnURL          string    `json:"-"`
	ID                 string    `json:"id"`
	OrderID            string    `json:"orderId"`
	InstallationID     string    `json:"installationId"`
	ProviderID         string    `json:"providerId"`
	AmountMinor        int64     `json:"amountMinor"`
	Currency           string    `json:"currency"`
	CurrencyMinorUnits int       `json:"currencyMinorUnits"`
	Status             Status    `json:"status"`
	GatewayOrderID     string    `json:"gatewayOrderId,omitempty"`
	GatewayPaymentID   string    `json:"gatewayPaymentId,omitempty"`
	CustomerName       string    `json:"customerName"`
	CustomerPhone      string    `json:"customerPhone"`
	CustomerEmail      string    `json:"customerEmail"`
	IdempotencyKey     string    `json:"idempotencyKey"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// IntentInput is the body the portal POSTs to create a payment
// intent. The local service generates the idempotency key when the
// caller does not supply one.
type IntentInput struct {
	ReturnURL          string `json:"-"`
	OrderID            string `json:"orderId"`
	ProviderID         string `json:"providerId"`
	AmountMinor        int64  `json:"amountMinor"`
	Currency           string `json:"currency"`
	CurrencyMinorUnits int    `json:"currencyMinorUnits"`
	CustomerName       string `json:"customerName"`
	CustomerPhone      string `json:"customerPhone"`
	CustomerEmail      string `json:"customerEmail"`
	IdempotencyKey     string `json:"idempotencyKey,omitempty"`
}

// Authorization is the parsed, verified payment authorisation the
// control plane returns. The raw envelope is the source of truth; the
// parsed shape is the convenience.
type Authorization struct {
	ID                 string    `json:"id"`
	IntentID           string    `json:"intentId"`
	InstallationID     string    `json:"installationId"`
	OrderID            string    `json:"orderId"`
	GatewayPaymentID   string    `json:"gatewayPaymentId"`
	AmountMinor        int64     `json:"amountMinor"`
	Currency           string    `json:"currency"`
	CurrencyMinorUnits int       `json:"currencyMinorUnits"`
	AuthorizedAt       time.Time `json:"authorizedAt"`
	Nonce              string    `json:"nonce"`
	VerifiedAt         time.Time `json:"verifiedAt"`
}

// SignedAuthorization is the wire envelope the control plane returns
// to the local service. The same key the licensing package uses to
// verify the licence signs this envelope.
type SignedAuthorization struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// ManualApproval records an explicit cash / UPI / bank-transfer
// approval. Offline approval is never presented as gateway-verified
// payment; the dashboard surfaces the manual flag separately.
type ManualApproval struct {
	ID          string    `json:"id"`
	IntentID    string    `json:"intentId"`
	Method      Method    `json:"method"`
	Reference   string    `json:"reference"`
	AmountMinor int64     `json:"amountMinor"`
	Currency    string    `json:"currency"`
	ApprovedBy  string    `json:"approvedBy"`
	Note        string    `json:"note"`
	ApprovedAt  time.Time `json:"approvedAt"`
}

// WebhookEvent is one webhook delivery the merchant's gateway sent.
// The IdempotencyKey field is the dedupe key; a retried webhook with
// the same key is recorded but not processed twice.
type WebhookEvent struct {
	ID             string    `json:"id"`
	ProviderID     string    `json:"providerId"`
	GatewayEventID string    `json:"gatewayEventId"`
	IntentID       string    `json:"intentId,omitempty"`
	IdempotencyKey string    `json:"idempotencyKey"`
	Verified       bool      `json:"verified"`
	ReceivedAt     time.Time `json:"receivedAt"`
	ProcessedAt    time.Time `json:"processedAt,omitempty"`
}

// LedgerEntry is one row in the payment audit log.
type LedgerEntry struct {
	ID          string    `json:"id"`
	IntentID    string    `json:"intentId"`
	EventType   string    `json:"eventType"`
	Detail      string    `json:"detail"`
	Actor       string    `json:"actor"`
	AmountMinor int64     `json:"amountMinor,omitempty"`
	OccurredAt  time.Time `json:"occurredAt"`
}

// AttemptKind is one row in the attempts log.
type AttemptKind string

const (
	AttemptCreate        AttemptKind = "create"
	AttemptRedirect      AttemptKind = "redirect"
	AttemptWebhook       AttemptKind = "webhook"
	AttemptCapture       AttemptKind = "capture"
	AttemptVerify        AttemptKind = "verify"
	AttemptManualApprove AttemptKind = "manual_approve"
	AttemptManualReject  AttemptKind = "manual_reject"
)

// LedgerEventType enumerates the audit event types. The values are
// stable and shipped in the local logs and the dashboard's transition
// list.
const (
	LedgerProviderCreated  = "provider.created"
	LedgerProviderUpdated  = "provider.updated"
	LedgerProviderDeleted  = "provider.deleted"
	LedgerIntentCreated    = "intent.created"
	LedgerIntentRedirected = "intent.redirected"
	LedgerIntentAuthorized = "intent.authorized"
	LedgerIntentCaptured   = "intent.captured"
	LedgerIntentFailed     = "intent.failed"
	LedgerIntentCancelled  = "intent.cancelled"
	LedgerIntentExpired    = "intent.expired"
	LedgerWebhookReceived  = "webhook.received"
	LedgerWebhookDuplicate = "webhook.duplicate"
	LedgerWebhookRejected  = "webhook.rejected"
	LedgerWebhookProcessed = "webhook.processed"
	LedgerManualApproved   = "manual.approved"
	LedgerManualRejected   = "manual.rejected"
)

// Product is the product identifier the control plane recognises.
// Payment authorisations signed for a different product are rejected
// before any state change so a stolen control-plane signature cannot
// be replayed against the On-Premise edition.
const Product = "print-catalyst-on-premise"

// ClockSkewTolerance allows the system clock to drift backwards by
// up to this much without raising a signature rejection. Anything
// larger is treated as a deliberate clock manipulation.
const ClockSkewTolerance = 5 * time.Minute
