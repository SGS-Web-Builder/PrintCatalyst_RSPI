package licensing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// signedPayload is the canonical byte sequence the control plane signs.
// Adding, removing or reordering a field changes the signature, so any
// drift between the local service and the control plane shows up as a
// failed verification on first refresh.
type signedPayload struct {
	Product           string        `json:"product"`
	Edition           Edition       `json:"edition"`
	InstallationID    string        `json:"installationId"`
	DeviceFingerprint string        `json:"deviceFingerprint"`
	IssuedAt          time.Time     `json:"issuedAt"`
	SupportUntil      time.Time     `json:"supportUntil"`
	Entitlements      []Entitlement `json:"entitlements"`
	Nonce             string        `json:"nonce"`
}

// SignedLicense is the on-the-wire envelope returned by the control
// plane. The wire format is intentionally separate from the local
// projection so the local schema can evolve without breaking the wire
// contract.
type SignedLicense struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// PublicKey returns the Ed25519 verification key the local service
// uses to check every signature the control plane emits.
//
// This is the test/fixture key the local stub uses today. Replacing it
// with the production key is the only change the production deployment
// needs: the rest of the verification code path is unchanged.
func PublicKey() ed25519.PublicKey {
	publicKey, _ := hex.DecodeString(controlPlanePublicKeyHex)
	return ed25519.PublicKey(publicKey)
}

// VerifySignature checks the signature against the embedded public key
// and returns the parsed payload on success. The caller passes the
// public key of the device the payload is for so a stolen signature
// cannot be replayed against a different installation.
func VerifySignature(envelope SignedLicense, installationPublicKey ed25519.PublicKey) (signedPayload, error) {
	if len(envelope.Signature) != ed25519.SignatureSize {
		return signedPayload{}, ErrSignature
	}
	if !ed25519.Verify(PublicKey(), envelope.Payload, envelope.Signature) {
		return signedPayload{}, ErrSignature
	}
	var payload signedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return signedPayload{}, ErrInvalid
	}
	if payload.Product != Product {
		return signedPayload{}, fmt.Errorf("%w: product %q", ErrMismatch, payload.Product)
	}
	if payload.InstallationID != InstallationID(installationPublicKey) {
		return signedPayload{}, ErrMismatch
	}
	if payload.DeviceFingerprint != Fingerprint(installationPublicKey) {
		return payload, ErrMismatch
	}
	if payload.IssuedAt.After(time.Now().Add(ClockSkewTolerance)) {
		return payload, ErrInvalid
	}
	if payload.SupportUntil.Before(payload.IssuedAt) {
		return payload, ErrInvalid
	}
	if len(payload.Nonce) < 8 {
		return payload, ErrInvalid
	}
	if len(payload.Entitlements) == 0 {
		return payload, ErrInvalid
	}
	for _, entitlement := range payload.Entitlements {
		if entitlement == "" {
			return payload, ErrInvalid
		}
	}
	return payload, nil
}

// MarshalSignedPayload returns the byte sequence the control plane
// must sign. The local stub uses this to produce test envelopes; the
// production control plane will sign the same byte sequence.
func MarshalSignedPayload(payload signedPayload) ([]byte, error) {
	if payload.Product != Product {
		return nil, fmt.Errorf("product must be %q", Product)
	}
	return json.Marshal(payload)
}

// PayloadHash returns the hex digest the audit log records so an
// operator can compare it against the control plane's record without
// exposing the payload contents.
func PayloadHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// controlPlanePublicKeyHex is the Ed25519 public key the local stub
// uses today. It is the only thing that changes between the local
// stub and the production deployment: the verification code path is
// the same.
//
// Generating a fresh key pair is the first step the production
// onboarding runbook describes; replacing this constant with the
// production key is the only change a release needs.
const controlPlanePublicKeyHex = "fe14f6211bb162b0d55afdf9f9dfbe223a88114c3b1823386355ec2fe1eccfa3"
