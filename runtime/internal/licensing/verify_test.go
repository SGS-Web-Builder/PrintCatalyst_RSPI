package licensing

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestLocalControlPlaneIssueLicenseAndVerify(t *testing.T) {
	stub, err := NewLocalControlPlane()
	if err != nil {
		t.Fatalf("NewLocalControlPlane: %v", err)
	}
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	request := IssueRequest{
		InstallationID:    InstallationID(pair.PublicKey),
		DeviceFingerprint: Fingerprint(pair.PublicKey),
		PublicKey:         pair.PublicKey,
		RequestedAt:       time.Now().UTC(),
		Edition:           EditionPerpetual,
		Entitlements:      []Entitlement{EntitlementPrintOrders, EntitlementIDCards},
		SupportUntil:      time.Now().UTC().Add(365 * 24 * time.Hour),
	}
	envelope, err := stub.IssueLicense(request)
	if err != nil {
		t.Fatalf("IssueLicense: %v", err)
	}
	if len(envelope.Payload) == 0 {
		t.Fatal("IssueLicense returned empty payload")
	}
	if len(envelope.Signature) != ed25519.SignatureSize {
		t.Fatalf("signature length = %d, want %d", len(envelope.Signature), ed25519.SignatureSize)
	}
	payload, err := VerifySignature(envelope, pair.PublicKey)
	if err != nil {
		t.Fatalf("VerifySignature: %v", err)
	}
	if payload.Product != Product {
		t.Fatalf("product = %q, want %q", payload.Product, Product)
	}
	if payload.InstallationID != request.InstallationID {
		t.Fatalf("installation id mismatch")
	}
	if len(payload.Entitlements) != 2 {
		t.Fatalf("entitlements = %d, want 2", len(payload.Entitlements))
	}
}

func TestVerifySignatureRejectsTamperedPayload(t *testing.T) {
	stub, err := NewLocalControlPlane()
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := GenerateKeyPair()
	envelope, err := stub.IssueLicense(IssueRequest{
		InstallationID:    InstallationID(pair.PublicKey),
		DeviceFingerprint: Fingerprint(pair.PublicKey),
		PublicKey:         pair.PublicKey,
		RequestedAt:       time.Now().UTC(),
		Edition:           EditionPerpetual,
		Entitlements:      []Entitlement{EntitlementPrintOrders},
		SupportUntil:      time.Now().UTC().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Payload[10] ^= 0xFF
	if _, err := VerifySignature(envelope, pair.PublicKey); err == nil {
		t.Fatal("VerifySignature accepted tampered payload")
	}
}

func TestVerifySignatureRejectsWrongPublicKey(t *testing.T) {
	stub, err := NewLocalControlPlane()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	envelope, err := stub.IssueLicense(IssueRequest{
		InstallationID:    InstallationID(a.PublicKey),
		DeviceFingerprint: Fingerprint(a.PublicKey),
		PublicKey:         a.PublicKey,
		RequestedAt:       time.Now().UTC(),
		Edition:           EditionPerpetual,
		Entitlements:      []Entitlement{EntitlementPrintOrders},
		SupportUntil:      time.Now().UTC().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySignature(envelope, b.PublicKey); err == nil {
		t.Fatal("VerifySignature accepted envelope under wrong public key")
	}
}

func TestIssueLicenseRejectsEmptyEntitlements(t *testing.T) {
	stub, _ := NewLocalControlPlane()
	pair, _ := GenerateKeyPair()
	_, err := stub.IssueLicense(IssueRequest{
		InstallationID:    InstallationID(pair.PublicKey),
		DeviceFingerprint: Fingerprint(pair.PublicKey),
		PublicKey:         pair.PublicKey,
		RequestedAt:       time.Now().UTC(),
		Edition:           EditionPerpetual,
		Entitlements:      nil,
		SupportUntil:      time.Now().UTC().Add(24 * time.Hour),
	})
	if err == nil {
		t.Fatal("IssueLicense accepted empty entitlements")
	}
}

func TestVerifySignatureRejectsBadProduct(t *testing.T) {
	stub, _ := NewLocalControlPlane()
	pair, _ := GenerateKeyPair()
	// The local stub refuses to sign a non-matching product, so we
	// hand-craft a payload that pretends to be a different product.
	envelope, err := stub.IssueLicense(IssueRequest{
		InstallationID:    InstallationID(pair.PublicKey),
		DeviceFingerprint: Fingerprint(pair.PublicKey),
		PublicKey:         pair.PublicKey,
		RequestedAt:       time.Now().UTC(),
		Edition:           EditionPerpetual,
		Entitlements:      []Entitlement{EntitlementPrintOrders},
		SupportUntil:      time.Now().UTC().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Replace the product with a different value while keeping the
	// original signature intact. The verification must reject it.
	corrupted := []byte(strings.Replace(string(envelope.Payload), `"product":"`+Product+`"`, `"product":"saas"`, 1))
	envelope.Payload = corrupted
	if _, err := VerifySignature(envelope, pair.PublicKey); err == nil {
		t.Fatal("VerifySignature accepted a payload from a different product")
	}
}

func TestPayloadHashIsStable(t *testing.T) {
	first := PayloadHash([]byte("hello"))
	second := PayloadHash([]byte("hello"))
	if first != second {
		t.Fatal("PayloadHash is not stable")
	}
	if len(first) != 64 {
		t.Fatalf("PayloadHash length = %d, want 64", len(first))
	}
	if first == PayloadHash([]byte("world")) {
		t.Fatal("PayloadHash collision for distinct inputs")
	}
}

func TestEmbeddedPublicKeyHexIsValid(t *testing.T) {
	raw, err := hex.DecodeString(controlPlanePublicKeyHex)
	if err != nil {
		t.Fatalf("control plane public key hex is invalid: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("control plane public key length = %d, want %d", len(raw), ed25519.PublicKeySize)
	}
}

func TestEmbeddedPublicKeyAndPrivateKeyMatch(t *testing.T) {
	rawPrivate, err := hex.DecodeString(ControlPlanePrivateKeyHex)
	if err != nil {
		t.Fatalf("private key hex invalid: %v", err)
	}
	if len(rawPrivate) != ed25519.PrivateKeySize {
		t.Fatalf("private key length = %d, want %d", len(rawPrivate), ed25519.PrivateKeySize)
	}
	privateKey := ed25519.PrivateKey(rawPrivate)
	derived := privateKey.Public().(ed25519.PublicKey)
	if hex.EncodeToString(derived) != controlPlanePublicKeyHex {
		t.Fatal("embedded control plane private/public key pair does not match")
	}
}
