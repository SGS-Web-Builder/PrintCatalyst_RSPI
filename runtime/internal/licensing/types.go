// Package licensing records the device key pair, the signed entitlement
// issued by the control plane and the verification rules that gate every
// commercial operation. The local service keeps the device key pair in
// the local database, requests a signed entitlement from the control
// plane at activation time, and re-verifies the signature with the
// public key shipped inside the binary on every load.
//
// The control plane is a separate, document-free service that holds the
// signing private key and the merchant's purchase record. This package
// does not depend on that service: the wire format and the verification
// rule are the contract, not the transport. A future slice will replace
// the local stub with the production HTTP client; the local service
// will keep the same verification code path so the production key is
// the only thing that has to change.
package licensing

import (
	"errors"
	"time"
)

// Sentinel errors map to HTTP status codes in the owner endpoint layer.
var (
	ErrUnconfigured = errors.New("licence is not configured")
	ErrInvalid      = errors.New("licence payload is invalid")
	ErrSignature    = errors.New("licence signature verification failed")
	ErrExpired      = errors.New("licence support window has expired")
	ErrRevoked      = errors.New("licence has been revoked")
	ErrMismatch     = errors.New("licence does not match this installation")
	ErrTransfer     = errors.New("device transfer requires a fresh activation")
	ErrClockRollback = errors.New("system clock is earlier than the last verified timestamp")
)

// Edition is the licence edition the control plane grants. The local
// service does not interpret the edition string; it persists it verbatim
// so a future edition can be added without a code change here.
type Edition string

const (
	EditionPerpetual Edition = "perpetual"
	EditionTrial     Edition = "trial"
	EditionTerm      Edition = "term"
)

// Entitlement is a single capability granted by the licence. The set of
// entitlements the control plane can grant is fixed; the local service
// persists the JSON array verbatim so the dashboard can show the exact
// list a merchant purchased.
type Entitlement string

const (
	EntitlementPrintOrders Entitlement = "print_orders"
	EntitlementIDCards     Entitlement = "id_cards"
	EntitlementPassport    Entitlement = "passport_photos"
	EntitlementTunnel      Entitlement = "tunnel"
	EntitlementManualPay   Entitlement = "manual_payment"
	EntitlementGateway     Entitlement = "merchant_gateway"
)

// License is the parsed, verified payload returned to the dashboard. It
// is the projection: the raw signed payload is the source of truth, the
// parsed shape is the convenience.
type License struct {
	ID                string       `json:"id"`
	InstallationID    string       `json:"installationId"`
	Product           string       `json:"product"`
	Edition           Edition      `json:"edition"`
	DeviceFingerprint string       `json:"deviceFingerprint"`
	IssuedAt          time.Time    `json:"issuedAt"`
	SupportUntil      time.Time    `json:"supportUntil"`
	Entitlements      []Entitlement `json:"entitlements"`
	Nonce             string       `json:"nonce"`
	IsCurrent         bool         `json:"isCurrent"`
	Revoked           bool         `json:"revoked"`
	RevokedReason     string       `json:"revokedReason,omitempty"`
	ReceivedAt        time.Time    `json:"receivedAt"`
}

// Status is the read model returned to the dashboard.
type Status struct {
	Unconfigured     bool         `json:"unconfigured"`
	InstallationID   string       `json:"installationId"`
	PublicKey        string       `json:"publicKey"`
	Fingerprint      string       `json:"fingerprint"`
	Current          *License     `json:"current,omitempty"`
	Entitlements     []Entitlement `json:"entitlements"`
	SupportUntil     *time.Time   `json:"supportUntil,omitempty"`
	OfflineGrace     bool         `json:"offlineGrace"`
	GraceRemaining   string       `json:"graceRemaining,omitempty"`
	VerifiedAt       *time.Time   `json:"verifiedAt,omitempty"`
	ClockRollback    bool         `json:"clockRollback"`
	Revoked          bool         `json:"revoked"`
	RevokedReason    string       `json:"revokedReason,omitempty"`
}

// Event is a single append-only audit row.
type Event struct {
	ID          string    `json:"id"`
	LicenseID   string    `json:"licenseId,omitempty"`
	EventType   string    `json:"eventType"`
	Detail      string    `json:"detail"`
	PayloadHash string    `json:"payloadHash,omitempty"`
	OccurredAt  time.Time `json:"occurredAt"`
}

// EventType enumerates the audit row kinds. The values are stable and
// shipped in the local logs and the dashboard's transition list.
const (
	EventKeyGenerated   = "key.generated"
	EventKeyRotated     = "key.rotated"
	EventActivationSent = "activation.sent"
	EventActivated      = "activation.received"
	EventActivationFail = "activation.failed"
	EventVerified       = "verification.succeeded"
	EventVerifyFail     = "verification.failed"
	EventRevoked        = "licence.revoked"
	EventTransferred    = "device.transferred"
	EventClockRollback  = "clock.rollback_detected"
)

// Product is the product identifier the control plane recognises. The
// local service refuses to install a licence signed for a different
// product so a stolen control-plane signature cannot be replayed against
// the On-Premise edition (or vice versa).
const Product = "print-catalyst-on-premise"

// FingerprintLength is the number of hex characters the dashboard shows
// for the device fingerprint. 16 hex characters = 64 bits, which is
// enough to identify a unique installation without leaking the full
// device key fingerprint to a casual observer.
const FingerprintLength = 16

// OfflineGrace is how long the local service continues to honour the
// previously verified entitlement after the last successful refresh
// fails. The brief requires a "limited offline grace for licence
// refresh without disabling already-paid print completion" so a
// transient network outage does not lock a merchant out of their own
// shop.
const OfflineGrace = 14 * 24 * time.Hour

// ClockSkewTolerance allows the system clock to drift backwards by up
// to this much without raising the rollback alarm. Anything larger is
// treated as a deliberate clock manipulation and audited.
const ClockSkewTolerance = 5 * time.Minute
