// Package devicekeys separates installation secrets by purpose and device.
// It provides software binding, not resistance to a privileged local attacker.
package devicekeys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const envelopeVersion byte = 1

type Vault struct {
	seed   [32]byte
	serial string
}

func New(seed []byte, serial string) (*Vault, error) {
	serial = strings.TrimRight(strings.TrimSpace(serial), "\x00")
	decoded, err := hex.DecodeString(serial)
	if len(seed) != 32 || err != nil || len(decoded) != 8 {
		return nil, errors.New("32-byte credential and 16-digit Pi serial required")
	}
	nonzero := false
	for _, b := range decoded {
		nonzero = nonzero || b != 0
	}
	if !nonzero {
		return nil, errors.New("Pi serial is unavailable")
	}
	v := &Vault{serial: strings.ToLower(serial)}
	copy(v.seed[:], seed)
	return v, nil
}

// Derive uses distinct caller-controlled purpose labels, never customer input.
func (v *Vault) Derive(purpose string) ([]byte, error) {
	if purpose == "" || len(purpose) > 80 || strings.ContainsRune(purpose, 0) {
		return nil, errors.New("invalid key purpose")
	}
	h := hmac.New(sha256.New, v.seed[:])
	h.Write([]byte("printcatalyst-rspi/v1\x00" + v.serial + "\x00" + purpose))
	return h.Sum(nil), nil
}
func (v *Vault) aead(purpose string) (cipher.AEAD, error) {
	key, err := v.Derive(purpose)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (v *Vault) Seal(purpose string, plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty secret")
	}
	a, err := v.aead(purpose)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+a.NonceSize())
	out[0] = envelopeVersion
	if _, err = rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return a.Seal(out, out[1:], plain, []byte(purpose)), nil
}
func (v *Vault) Open(purpose string, sealed []byte) ([]byte, error) {
	a, err := v.aead(purpose)
	if err != nil {
		return nil, err
	}
	if len(sealed) < 1+a.NonceSize()+a.Overhead() || sealed[0] != envelopeVersion {
		return nil, errors.New("invalid protected secret")
	}
	raw, err := a.Open(nil, sealed[1:1+a.NonceSize()], sealed[1+a.NonceSize():], []byte(purpose))
	if err != nil {
		return nil, errors.New("protected secret does not match this device or credential")
	}
	return raw, nil
}
