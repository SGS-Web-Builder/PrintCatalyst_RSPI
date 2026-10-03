package payments

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
)

func TestLocalBrokerAuthorizeAndVerify(t *testing.T) {
	broker, err := NewLocalBroker()
	if err != nil {
		t.Fatalf("NewLocalBroker: %v", err)
	}
	request := AuthorizeRequest{
		InstallationID:     "installation-test",
		OrderID:            "order-test",
		GatewayPaymentID:   "pay-test",
		AmountMinor:        12345,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	}
	envelope, err := broker.Authorize(request)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if len(envelope.Payload) == 0 {
		t.Fatal("Authorize returned empty payload")
	}
	if len(envelope.Signature) != ed25519.SignatureSize {
		t.Fatalf("signature length = %d, want %d", len(envelope.Signature), ed25519.SignatureSize)
	}
	intent := Intent{
		ID:                 "intent-test",
		InstallationID:     request.InstallationID,
		OrderID:            request.OrderID,
		AmountMinor:        request.AmountMinor,
		Currency:           request.Currency,
		CurrencyMinorUnits: request.CurrencyMinorUnits,
	}
	payload, err := VerifyAuthorizationSignature(envelope, intent)
	if err != nil {
		t.Fatalf("VerifyAuthorizationSignature: %v", err)
	}
	if payload.Product != Product {
		t.Fatalf("product = %q, want %q", payload.Product, Product)
	}
	if payload.GatewayPaymentID != request.GatewayPaymentID {
		t.Fatalf("gateway payment id mismatch")
	}
}

func TestVerifyAuthorizationSignatureRejectsWrongIntent(t *testing.T) {
	broker, err := NewLocalBroker()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "installation-a",
		OrderID:            "order-a",
		GatewayPaymentID:   "pay-a",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := Intent{
		InstallationID:     "installation-b",
		OrderID:            "order-b",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
	}
	if _, err := VerifyAuthorizationSignature(envelope, wrong); err == nil {
		t.Fatal("VerifyAuthorizationSignature accepted envelope for wrong intent")
	}
}

func TestVerifyAuthorizationSignatureRejectsAmountMismatch(t *testing.T) {
	broker, _ := NewLocalBroker()
	envelope, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "x",
		OrderID:            "y",
		GatewayPaymentID:   "z",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := Intent{InstallationID: "x", OrderID: "y", AmountMinor: 200, Currency: "INR", CurrencyMinorUnits: 2}
	if _, err := VerifyAuthorizationSignature(envelope, wrong); err != ErrAmount {
		t.Fatalf("err = %v, want ErrAmount", err)
	}
}

func TestVerifyAuthorizationSignatureRejectsCurrencyMismatch(t *testing.T) {
	broker, _ := NewLocalBroker()
	envelope, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "x",
		OrderID:            "y",
		GatewayPaymentID:   "z",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := Intent{InstallationID: "x", OrderID: "y", AmountMinor: 100, Currency: "USD", CurrencyMinorUnits: 2}
	if _, err := VerifyAuthorizationSignature(envelope, wrong); err != ErrCurrency {
		t.Fatalf("err = %v, want ErrCurrency", err)
	}
}

func TestVerifyAuthorizationSignatureRejectsTamperedPayload(t *testing.T) {
	broker, _ := NewLocalBroker()
	envelope, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "x",
		OrderID:            "y",
		GatewayPaymentID:   "z",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Payload[10] ^= 0xFF
	intent := Intent{InstallationID: "x", OrderID: "y", AmountMinor: 100, Currency: "INR", CurrencyMinorUnits: 2}
	if _, err := VerifyAuthorizationSignature(envelope, intent); err != ErrSignature {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
}

func TestLocalBrokerRejectsMissingFields(t *testing.T) {
	broker, _ := NewLocalBroker()
	if _, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "",
		OrderID:            "y",
		GatewayPaymentID:   "z",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	}); err == nil {
		t.Fatal("Authorize accepted missing installation id")
	}
	if _, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "x",
		OrderID:            "y",
		GatewayPaymentID:   "",
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	}); err == nil {
		t.Fatal("Authorize accepted missing gateway payment id")
	}
	if _, err := broker.Authorize(AuthorizeRequest{
		InstallationID:     "x",
		OrderID:            "y",
		GatewayPaymentID:   "z",
		AmountMinor:        100,
		Currency:           "",
		CurrencyMinorUnits: 2,
		AuthorizedAt:       time.Now().UTC(),
	}); err == nil {
		t.Fatal("Authorize accepted missing currency")
	}
}

func TestPayloadHashIsStable(t *testing.T) {
	a := PayloadHash([]byte("hello"))
	b := PayloadHash([]byte("hello"))
	if a != b {
		t.Fatal("PayloadHash is not stable")
	}
	if len(a) != 64 {
		t.Fatalf("PayloadHash length = %d, want 64", len(a))
	}
}

func TestEncryptDecryptSecretRoundTrip(t *testing.T) {
	seed := licensing.DeriveEncryptionKey([]byte("secret-roundtrip"))
	ciphertext, nonce, err := encryptSecret([]byte("secret-value"), seed)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	plaintext, err := decryptSecret(ciphertext, nonce, seed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != "secret-value" {
		t.Fatalf("plaintext = %q, want %q", plaintext, "secret-value")
	}
}

func TestDecryptSecretRejectsTamperedCiphertext(t *testing.T) {
	seed := licensing.DeriveEncryptionKey([]byte("secret-tamper"))
	ciphertext, nonce, err := encryptSecret([]byte("secret-value"), seed)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[0] ^= 0xFF
	if _, err := decryptSecret(ciphertext, nonce, seed); err == nil {
		t.Fatal("decrypt accepted tampered ciphertext")
	}
}

func TestEmbeddedControlPlaneKeyMatchesLicensing(t *testing.T) {
	rawPrivate, err := hex.DecodeString(licensing.ControlPlanePrivateKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if len(rawPrivate) != ed25519.PrivateKeySize {
		t.Fatalf("private key length = %d, want %d", len(rawPrivate), ed25519.PrivateKeySize)
	}
	privateKey := ed25519.PrivateKey(rawPrivate)
	derived := privateKey.Public().(ed25519.PublicKey)
	if !strings.HasPrefix(hex.EncodeToString(derived), "fe14f621") {
		t.Fatalf("public key prefix unexpected: %s", hex.EncodeToString(derived))
	}
}

func TestKnownKindValidation(t *testing.T) {
	if !knownKind(KindRazorpayPlatform) {
		t.Fatal("razorpay_platform should be recognised")
	}
	if !knownKind(KindRazorpayMerchant) {
		t.Fatal("razorpay_merchant should be recognised")
	}
	if !knownKind(KindManual) {
		t.Fatal("manual should be recognised")
	}
	if knownKind(Kind("unknown")) {
		t.Fatal("unknown kind should not be recognised")
	}
}

func TestValidWebhookTransition(t *testing.T) {
	cases := []struct {
		from Status
		to   Status
		want bool
	}{
		{StatusPending, StatusRedirected, true},
		{StatusPending, StatusAuthorized, true},
		{StatusRedirected, StatusAuthorized, true},
		{StatusAuthorized, StatusCaptured, true},
		{StatusPending, StatusCaptured, false},
		{StatusCaptured, StatusFailed, false},
		{StatusFailed, StatusCaptured, false},
	}
	for _, c := range cases {
		if got := validWebhookTransition(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s: got %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
