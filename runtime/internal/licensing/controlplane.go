package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// ControlPlane is the interface the local service uses to obtain a
// signed entitlement. Production wires this to the hosted control
// plane's HTTP API; tests and the local fixture wire it to a local
// stub that signs with the same private key the local service
// verifies against.
//
// IssueLicense receives the device public key and the requested
// entitlements, then returns the signed envelope the local service
// should persist. A production implementation would also record the
// purchase identifier and any merchant billing metadata; the local
// stub returns a deterministic envelope so tests are reproducible.
type ControlPlane interface {
	IssueLicense(request IssueRequest) (SignedLicense, error)
}

// IssueRequest is the input the local service sends to the control
// plane. The public key is the only identifier the control plane needs
// at activation time; the device fingerprint is included so the
// control plane can refuse a request that does not match its own
// derivation.
type IssueRequest struct {
	InstallationID    string
	DeviceFingerprint string
	PublicKey         ed25519.PublicKey
	RequestedAt       time.Time
	Edition           Edition
	Entitlements      []Entitlement
	SupportUntil      time.Time
}

// LocalControlPlane is the in-process implementation of ControlPlane.
// It signs envelopes with ControlPlanePrivateKeyHex so the local
// service can exercise the production verification code path without
// a network round trip. A test that requires a malicious or faulty
// control plane can construct its own implementation.
type LocalControlPlane struct {
	PrivateKey ed25519.PrivateKey
}

// NewLocalControlPlane returns a LocalControlPlane signed by the
// embedded control-plane private key. Tests that need to simulate a
// hostile control plane should construct LocalControlPlane with a
// freshly generated key instead.
func NewLocalControlPlane() (LocalControlPlane, error) {
	raw, err := hex.DecodeString(ControlPlanePrivateKeyHex)
	if err != nil {
		return LocalControlPlane{}, fmt.Errorf("decode control-plane private key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return LocalControlPlane{}, fmt.Errorf("control-plane private key has wrong size")
	}
	return LocalControlPlane{PrivateKey: ed25519.PrivateKey(raw)}, nil
}

// IssueLicense signs a fresh entitlement for the supplied device. A
// 16-byte nonce is generated from the operating system's entropy
// source so two envelopes for the same device never collide.
func (l LocalControlPlane) IssueLicense(request IssueRequest) (SignedLicense, error) {
	if request.InstallationID == "" || request.DeviceFingerprint == "" {
		return SignedLicense{}, fmt.Errorf("issue request missing identifiers")
	}
	if len(request.PublicKey) != ed25519.PublicKeySize {
		return SignedLicense{}, fmt.Errorf("issue request public key has wrong size")
	}
	if len(request.Entitlements) == 0 {
		return SignedLicense{}, fmt.Errorf("issue request entitlements must not be empty")
	}
	if request.SupportUntil.Before(request.RequestedAt) {
		return SignedLicense{}, fmt.Errorf("issue request support window is invalid")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return SignedLicense{}, fmt.Errorf("generate nonce: %w", err)
	}
	payload := signedPayload{
		Product:           Product,
		Edition:           request.Edition,
		InstallationID:    request.InstallationID,
		DeviceFingerprint: request.DeviceFingerprint,
		IssuedAt:          request.RequestedAt.UTC().Truncate(time.Second),
		SupportUntil:      request.SupportUntil.UTC().Truncate(time.Second),
		Entitlements:      append([]Entitlement(nil), request.Entitlements...),
		Nonce:             hex.EncodeToString(nonce),
	}
	canonical, err := MarshalSignedPayload(payload)
	if err != nil {
		return SignedLicense{}, err
	}
	signature := ed25519.Sign(l.PrivateKey, canonical)
	return SignedLicense{Payload: canonical, Signature: signature}, nil
}

// MarshalSignedRequest returns a JSON byte slice describing the issue
// request. Production deployments will send this to the hosted control
// plane over the authenticated device channel; the local stub does
// not use it because it accepts a Go struct.
func MarshalSignedRequest(request IssueRequest) ([]byte, error) {
	type wire struct {
		InstallationID    string       `json:"installationId"`
		DeviceFingerprint string       `json:"deviceFingerprint"`
		PublicKey         string       `json:"publicKey"`
		RequestedAt       time.Time    `json:"requestedAt"`
		Edition           Edition      `json:"edition"`
		Entitlements      []Entitlement `json:"entitlements"`
		SupportUntil      time.Time    `json:"supportUntil"`
	}
	return json.Marshal(wire{
		InstallationID:    request.InstallationID,
		DeviceFingerprint: request.DeviceFingerprint,
		PublicKey:         hex.EncodeToString(request.PublicKey),
		RequestedAt:       request.RequestedAt,
		Edition:           request.Edition,
		Entitlements:      request.Entitlements,
		SupportUntil:      request.SupportUntil,
	})
}
