package payments

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
)

// Broker is the interface the local service uses to obtain a signed
// payment authorisation from the central control plane. Production
// wires this to the hosted control plane's HTTP API; tests and the
// local fixture wire it to the local stub that signs with the same
// private key the licensing package verifies against.
type Broker interface {
	Authorize(request AuthorizeRequest) (SignedAuthorization, error)
}

// AuthorizeRequest is the input the local service sends to the
// control plane. The local intent is the source of truth for the
// installation id, order id, amount, currency and gateway payment
// id; the control plane only signs envelopes that match exactly.
type AuthorizeRequest struct {
	InstallationID    string
	OrderID           string
	GatewayPaymentID  string
	AmountMinor       int64
	Currency          string
	CurrencyMinorUnits int
	AuthorizedAt      time.Time
}

// LocalBroker is the in-process implementation of Broker. It signs
// envelopes with the same private key the licensing package ships,
// so the local service exercises the production verification path
// without a network round trip. A test that needs to simulate a
// hostile broker can construct its own implementation.
type LocalBroker struct {
	PrivateKey ed25519.PrivateKey
}

// NewLocalBroker returns a LocalBroker signed by the embedded
// control-plane private key. Tests that need to simulate a hostile
// broker should construct LocalBroker with a freshly generated key
// instead.
func NewLocalBroker() (LocalBroker, error) {
	raw, err := hex.DecodeString(licensing.ControlPlanePrivateKeyHex)
	if err != nil {
		return LocalBroker{}, fmt.Errorf("decode control-plane private key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return LocalBroker{}, fmt.Errorf("control-plane private key has wrong size")
	}
	return LocalBroker{PrivateKey: ed25519.PrivateKey(raw)}, nil
}

// Authorize signs a fresh payment authorisation for the supplied
// request. A 16-byte nonce is generated from the operating system's
// entropy source so two authorisations for the same intent never
// collide.
func (b LocalBroker) Authorize(request AuthorizeRequest) (SignedAuthorization, error) {
	if request.InstallationID == "" || request.OrderID == "" {
		return SignedAuthorization{}, fmt.Errorf("authorise request missing identifiers")
	}
	if request.GatewayPaymentID == "" {
		return SignedAuthorization{}, fmt.Errorf("authorise request missing gateway payment id")
	}
	if request.AmountMinor < 0 || request.CurrencyMinorUnits < 0 {
		return SignedAuthorization{}, fmt.Errorf("authorise request amounts must be non-negative")
	}
	if request.AuthorizedAt.IsZero() {
		return SignedAuthorization{}, fmt.Errorf("authorise request missing timestamp")
	}
	if request.Currency == "" {
		return SignedAuthorization{}, fmt.Errorf("authorise request missing currency")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return SignedAuthorization{}, fmt.Errorf("generate nonce: %w", err)
	}
	payload := signedAuthorizationPayload{
		Product:            Product,
		InstallationID:     request.InstallationID,
		OrderID:            request.OrderID,
		GatewayPaymentID:   request.GatewayPaymentID,
		AmountMinor:        request.AmountMinor,
		Currency:           request.Currency,
		CurrencyMinorUnits: request.CurrencyMinorUnits,
		AuthorizedAt:       request.AuthorizedAt.UTC().Truncate(time.Second),
		Nonce:              hex.EncodeToString(nonce),
	}
	canonical, err := MarshalAuthorization(payload)
	if err != nil {
		return SignedAuthorization{}, err
	}
	signature := ed25519.Sign(b.PrivateKey, canonical)
	return SignedAuthorization{Payload: canonical, Signature: signature}, nil
}

// MerchantProvider is the interface a merchant-owned gateway adapter
// implements. The local service calls Create to obtain a redirect
// URL the customer uses to complete payment, and the gateway then
// POSTs a webhook back. VerifyWebhookSignature is the hook the
// adapter uses to authenticate the webhook delivery.
type MerchantProvider interface {
	Create(intent Intent, secret []byte) (CreateResult, error)
	VerifyWebhookSignature(rawBody []byte, signature string, secret []byte) error
	ParseWebhook(rawBody []byte) (Webhook, error)
}

// CreateResult is the redirect bundle the gateway returns after the
// local service asks it to create a payment order.
type CreateResult struct {
	GatewayOrderID   string
	GatewayPaymentID string
	RedirectURL      string
	ExpiresAt        time.Time
}

// Webhook is the parsed body of a webhook delivery. The intent id is
// the local id; the gateway payment id is whatever the gateway uses
// to identify the payment. Status is the lifecycle transition the
// gateway is asserting.
type Webhook struct {
	GatewayEventID   string
	IdempotencyKey   string
	IntentID         string
	GatewayOrderID   string
	GatewayPaymentID string
	Status           Status
	AmountMinor      int64
	Currency         string
	ReceivedAt       time.Time
}

// ProviderAdapter is the registration the local service keeps for
// each merchant-owned gateway. The Kind field is the stable
// identifier the merchant uses in the dashboard; the constructor
// returns the concrete MerchantProvider implementation.
type ProviderAdapter struct {
	Kind       Kind
	Adapter    MerchantProvider
	ConfigKeys []string
}

// Connector is the interface a merchant-owned gateway adapter
// implements when it can probe the remote API with the supplied
// credentials without persisting anything. The dashboard uses it
// for the "Test connection" button on the payment setup wizard —
// merchants paste their Razorpay keys and click the button to
// verify the secret is live before saving. The adapter must not
// store or log the supplied credentials.
type Connector interface {
	TestConnection(ctx context.Context, secret []byte) error
}

// TestProviderConnection verifies that the supplied Razorpay
// credentials (key id, key secret, webhook secret) authenticate
// against the live Razorpay API without persisting them. The
// dashboard calls this from the "Test connection" button on the
// payment setup wizard so the merchant can confirm their keys work
// before they save. The test issues an authenticated GET against
// /v1/payment_links?count=1, which is the cheapest read-only call
// that exercises the Basic-auth credentials. A 200 response means
// the keys are valid; a 401 means one of them is wrong; any other
// status is surfaced verbatim.
func (s *Service) TestProviderConnection(ctx context.Context, secret []byte) error {
	adapter, ok := s.adapters[KindRazorpayMerchant]
	if !ok {
		return fmt.Errorf("%w: merchant gateway adapter not registered", ErrUnconfigured)
	}
	connector, ok := adapter.(Connector)
	if !ok {
		return fmt.Errorf("merchant gateway adapter does not implement TestConnection")
	}
	return connector.TestConnection(ctx, secret)
}
