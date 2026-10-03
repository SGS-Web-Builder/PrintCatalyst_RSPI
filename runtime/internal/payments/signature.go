package payments

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
)

// signedAuthorizationPayload is the canonical byte sequence the
// control plane signs. Adding, removing or reordering a field
// changes the signature, so any drift between the local service and
// the control plane shows up as a failed verification on first use.
type signedAuthorizationPayload struct {
	Product            string    `json:"product"`
	InstallationID     string    `json:"installationId"`
	OrderID            string    `json:"orderId"`
	GatewayPaymentID   string    `json:"gatewayPaymentId"`
	AmountMinor        int64     `json:"amountMinor"`
	Currency           string    `json:"currency"`
	CurrencyMinorUnits int       `json:"currencyMinorUnits"`
	AuthorizedAt       time.Time `json:"authorizedAt"`
	Nonce              string    `json:"nonce"`
}

// MarshalAuthorization returns the byte sequence the control plane
// must sign. The local stub uses this to produce test envelopes; the
// production control plane signs the same byte sequence.
func MarshalAuthorization(payload signedAuthorizationPayload) ([]byte, error) {
	if payload.Product != Product {
		return nil, fmt.Errorf("product must be %q", Product)
	}
	return json.Marshal(payload)
}

// VerifyAuthorizationSignature checks the signature against the
// embedded control-plane public key (the same key the licensing
// package uses) and returns the parsed payload on success.
//
// The caller passes the local intent so the verifier can confirm
// the envelope actually matches what the local service is waiting
// to settle; a stolen signature cannot be replayed against a
// different intent.
func VerifyAuthorizationSignature(envelope SignedAuthorization, intent Intent) (signedAuthorizationPayload, error) {
	if len(envelope.Signature) != ed25519.SignatureSize {
		return signedAuthorizationPayload{}, ErrSignature
	}
	if !ed25519.Verify(licensing.PublicKey(), envelope.Payload, envelope.Signature) {
		return signedAuthorizationPayload{}, ErrSignature
	}
	var payload signedAuthorizationPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return signedAuthorizationPayload{}, ErrInvalid
	}
	if payload.Product != Product {
		return payload, fmt.Errorf("%w: product %q", ErrMismatch, payload.Product)
	}
	if payload.InstallationID != intent.InstallationID {
		return payload, ErrMismatch
	}
	if payload.OrderID != intent.OrderID {
		return payload, ErrMismatch
	}
	if payload.AmountMinor != intent.AmountMinor {
		return payload, ErrAmount
	}
	if payload.Currency != intent.Currency {
		return payload, ErrCurrency
	}
	if payload.CurrencyMinorUnits != intent.CurrencyMinorUnits {
		return payload, ErrCurrency
	}
	if payload.AuthorizedAt.After(time.Now().Add(ClockSkewTolerance)) {
		return payload, ErrInvalid
	}
	if len(payload.Nonce) < 8 {
		return payload, ErrInvalid
	}
	if payload.GatewayPaymentID == "" {
		return payload, ErrInvalid
	}
	return payload, nil
}

// PayloadHash returns the hex digest the audit log records so an
// operator can compare it against the control plane's record without
// exposing the payload contents.
func PayloadHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
