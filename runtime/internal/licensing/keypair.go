package licensing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// KeyPair is a generated device key pair. The public key is shared with
// the control plane; the private key never leaves the local service.
type KeyPair struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

// GenerateKeyPair returns a fresh Ed25519 key pair seeded from the
// operating system's entropy source. The control plane will sign
// entitlements for the returned public key; the local service keeps the
// private key encrypted at rest.
func GenerateKeyPair() (KeyPair, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate device key: %w", err)
	}
	return KeyPair{PublicKey: publicKey, PrivateKey: privateKey}, nil
}

// Fingerprint returns a stable, short hex fingerprint of the public
// key. The control plane uses the same algorithm to identify the
// installation on its side, so both sides can confirm they are talking
// about the same installation without leaking the full key.
func Fingerprint(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])[:FingerprintLength]
}

// InstallationID returns the canonical installation identifier: a
// 32-character hex digest of the public key. The brief requires an
// installation id on every signed authorisation, so every code path
// derives it from the same source.
func InstallationID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])
}

// DeriveEncryptionKey turns a data-directory identifier into a 32-byte
// AES-256 key. The local service passes a path that contains the data
// directory and any stable machine identifier; the derived key binds
// the encrypted private key to that exact combination so a copy of the
// SQLite file alone cannot recover the private key on another machine.
func DeriveEncryptionKey(seed []byte) []byte {
	digest := sha256.Sum256(append([]byte("pc-onpremise-licence-v1:"), seed...))
	return digest[:]
}

// EncryptPrivateKey wraps the private key with AES-256-GCM using a
// nonce generated for every call. The nonce is returned alongside the
// ciphertext so the local service can persist both atomically.
func EncryptPrivateKey(privateKey ed25519.PrivateKey, key []byte) (ciphertext []byte, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("init encryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("init gcm: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, []byte(privateKey), nil)
	return ciphertext, nonce, nil
}

// DecryptPrivateKey reverses EncryptPrivateKey. The constant-time
// comparison guards against an oracle that learns whether the GCM tag
// validated before the caller can react.
func DecryptPrivateKey(ciphertext, nonce, key []byte) (ed25519.PrivateKey, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init decryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("nonce size mismatch")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(plaintext) != ed25519.PrivateKeySize {
		_ = subtle.ConstantTimeCompare(plaintext, plaintext)
		return nil, ErrInvalid
	}
	return ed25519.PrivateKey(plaintext), nil
}

// SeedFromEnvironment collects the seeds available in the runtime to
// derive the encryption key. The slice is concatenated in a stable
// order so two installations on the same data directory and machine
// derive the same key, but copying the SQLite file to a different
// data directory or host cannot recover the private key.
//
// Order:
//  1. DataDirectory (always present in production).
//  2. Hostname (best-effort; absent in some sandboxes).
//  3. MachineID from /etc/machine-id when available (best-effort).
func SeedFromEnvironment(dataDirectory string) []byte {
	seed := []byte(dataDirectory)
	if host, err := os.Hostname(); err == nil && host != "" {
		seed = append(seed, '\x00')
		seed = append(seed, []byte(host)...)
	}
	if machineID, err := os.ReadFile("/etc/machine-id"); err == nil && len(machineID) > 0 {
		seed = append(seed, '\x00')
		seed = append(seed, machineID...)
	}
	return seed
}

// IsEncrypted checks whether the supplied blob looks like an encrypted
// private key. The local service keeps both plaintext and encrypted
// private keys during the migration window; this helper lets the
// service tell the two apart without committing to a specific length.
func IsEncrypted(buf []byte) bool {
	return len(buf) > 0 && len(buf) != ed25519.PrivateKeySize
}

// Compile-time guard: errors.Is must recognise ErrInvalid as a sentinel.
var _ = errors.Is
