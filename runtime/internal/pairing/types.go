// Package pairing implements the device-pairing protocol the iOS and
// Android companion applications use to authenticate against an
// On-Premise installation.
//
// The protocol is two-step:
//
//  1. The owner of the On-Premise installation calls Initiate from
//     the dashboard to mint a short-lived, single-use pairing code.
//     The code is delivered to the merchant's phone either as a
//     human-readable six-word phrase or as a deep-link URL that the
//     mobile companion application opens directly.
//
//  2. The companion application calls Exchange from inside the paired
//     network with the code, its claimed device fingerprint and a
//     human-readable label. The server validates the code, returns a
//     long-lived bearer token bound to the new PairedDevice row, and
//     records an append-only audit trail.
//
// The companion application then authenticates subsequent calls with
// the bearer token; the token is rotation-friendly so the owner can
// revoke a single device without affecting the rest of the fleet.
package pairing

import (
	"errors"
	"strings"
	"time"
)

// Sentinel errors mapped by localserver.pairingOwnerError.
var (
	// ErrInvalid reports a malformed or missing field in the request.
	ErrInvalid = errors.New("pairing: invalid request")
	// ErrUnconfigured is returned when an operation requires a
	// pairing code or paired device that does not exist.
	ErrUnconfigured = errors.New("pairing: not configured")
	// ErrSignature reports a bearer-token verification failure.
	ErrSignature = errors.New("pairing: signature mismatch")
	// ErrMismatch reports a pairing-code/claimed-fingerprint
	// disagreement or a revoked device attempt.
	ErrMismatch = errors.New("pairing: mismatch")
	// ErrExpired reports a pairing code that has aged past its TTL.
	ErrExpired = errors.New("pairing: code expired")
	// ErrRevoked reports a bearer token or paired device that has
	// been revoked by the owner.
	ErrRevoked = errors.New("pairing: device revoked")
	// ErrRateLimit reports a token-exchange attempt that exceeded the
	// per-fingerprint rate limit.
	ErrRateLimit = errors.New("pairing: rate limit exceeded")
)

// CodeTTL is how long a freshly-minted pairing code remains valid.
// The companion application must exchange the code within this window
// or the owner must mint a new one.
const CodeTTL = 5 * time.Minute

// CodeLength is the number of characters in a pairing code. Eight
// characters from the unambiguous alphabet give ~10^14 combinations,
// which combined with the TTL is enough to make brute-force exchanges
// computationally infeasible.
const CodeLength = 8

// TokenTTL is the lifetime of a bearer token issued after a successful
// code exchange. Mobile devices are expected to refresh before this
// elapses; the owner can revoke sooner if a device is lost.
const TokenTTL = 30 * 24 * time.Hour

// CodeAlphabet is the unambiguous set of characters the pairing code
// generator draws from. 0/O, 1/I and 2/Z are excluded to keep the code
// readable when typed by hand and scannable from a QR code.
const CodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// MaxExchangesPerFingerprint bounds the number of times a single
// claimed device fingerprint may attempt to exchange a code inside the
// rate-limit window. The window is enforced separately via the
// pair_exchange_attempts table the migration adds.
const MaxExchangesPerFingerprint = 5

// RateLimitWindow is the rolling window the pairing service uses to
// throttle code-exchange attempts by claimed fingerprint.
const RateLimitWindow = time.Minute

// DeviceFingerprintMaxLength bounds the size of a claimed fingerprint
// the exchange endpoint accepts. The fingerprint is opaque to the
// server; we just enforce a sane upper bound.
const DeviceFingerprintMaxLength = 128

// DeviceLabelMaxLength bounds the size of the human-readable label the
// companion application submits at exchange time (for example,
// "Alice's iPhone 15"). The owner sees the label on the dashboard.
const DeviceLabelMaxLength = 128

// NormalizeCode uppercases the supplied pairing code and trims the
// surrounding whitespace so a deep link that wraps the code in a URL
// parameter still matches the canonical form stored in the database.
func NormalizeCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// IsValidFingerprint rejects empty, oversize or control-character
// bearing fingerprints without inspecting the format. The mobile
// clients are free to choose any opaque string; the server only cares
// that the value is non-empty and printable.
func IsValidFingerprint(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > DeviceFingerprintMaxLength {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

// IsValidLabel rejects empty, oversize or control-character labels.
func IsValidLabel(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > DeviceLabelMaxLength {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}
