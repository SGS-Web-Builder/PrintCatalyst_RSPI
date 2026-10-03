package licensing

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateKeyPairIsUnique(t *testing.T) {
	first, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair first: %v", err)
	}
	second, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair second: %v", err)
	}
	if hex.EncodeToString(first.PublicKey) == hex.EncodeToString(second.PublicKey) {
		t.Fatal("two consecutive generations produced the same public key")
	}
}

func TestFingerprintIsStable(t *testing.T) {
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	a := Fingerprint(pair.PublicKey)
	b := Fingerprint(pair.PublicKey)
	if a != b {
		t.Fatalf("fingerprint is not stable: %s vs %s", a, b)
	}
	if len(a) != FingerprintLength {
		t.Fatalf("fingerprint length = %d, want %d", len(a), FingerprintLength)
	}
}

func TestFingerprintDistinguishesDifferentKeys(t *testing.T) {
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	if Fingerprint(a.PublicKey) == Fingerprint(b.PublicKey) {
		t.Fatal("different keys produced the same fingerprint")
	}
}

func TestInstallationIDIs64Hex(t *testing.T) {
	pair, _ := GenerateKeyPair()
	id := InstallationID(pair.PublicKey)
	if len(id) != 64 {
		t.Fatalf("installation id length = %d, want 64", len(id))
	}
	if strings.ContainsAny(id, "ghijklmnopqrstuvwxyz") {
		t.Fatalf("installation id is not hex: %s", id)
	}
}

func TestDeriveEncryptionKeyStable(t *testing.T) {
	seed := []byte("unit-test-seed")
	first := DeriveEncryptionKey(seed)
	second := DeriveEncryptionKey(seed)
	if hex.EncodeToString(first) != hex.EncodeToString(second) {
		t.Fatalf("derived key is not stable")
	}
	if len(first) != 32 {
		t.Fatalf("derived key length = %d, want 32", len(first))
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	pair, _ := GenerateKeyPair()
	key := DeriveEncryptionKey([]byte("round-trip"))
	ciphertext, nonce, err := EncryptPrivateKey(pair.PrivateKey, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ciphertext) == 0 || len(nonce) == 0 {
		t.Fatal("encryption returned empty artefacts")
	}
	plaintext, err := DecryptPrivateKey(ciphertext, nonce, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !ed25519.PrivateKey(plaintext).Equal(pair.PrivateKey) {
		t.Fatal("decrypted private key does not match")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	pair, _ := GenerateKeyPair()
	key := DeriveEncryptionKey([]byte("tamper-test"))
	ciphertext, nonce, err := EncryptPrivateKey(pair.PrivateKey, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ciphertext[0] ^= 0xFF
	if _, err := DecryptPrivateKey(ciphertext, nonce, key); err == nil {
		t.Fatal("decrypt accepted tampered ciphertext")
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	pair, _ := GenerateKeyPair()
	ciphertext, nonce, err := EncryptPrivateKey(pair.PrivateKey, DeriveEncryptionKey([]byte("a")))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := DecryptPrivateKey(ciphertext, nonce, DeriveEncryptionKey([]byte("b"))); err == nil {
		t.Fatal("decrypt accepted wrong key")
	}
}

func TestDecryptRejectsWrongNonceLength(t *testing.T) {
	pair, _ := GenerateKeyPair()
	key := DeriveEncryptionKey([]byte("nonce-test"))
	ciphertext, _, err := EncryptPrivateKey(pair.PrivateKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptPrivateKey(ciphertext, []byte{}, key); err == nil {
		t.Fatal("decrypt accepted empty nonce")
	}
	if _, err := DecryptPrivateKey(ciphertext, []byte("short"), key); err == nil {
		t.Fatal("decrypt accepted short nonce")
	}
}
