package devicekeys

import (
	"bytes"
	"testing"
)

func TestDeviceAndPurposeBinding(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, 32)
	v, err := New(seed, "000000001234abcd\x00")
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{9}, 64)
	sealed, err := v.Seal("licence", secret)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(seed, "000000001234abcd")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := restarted.Open("licence", sealed)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatal("restart recovery failed", err)
	}
	other, _ := New(seed, "000000001234abce")
	wrongKey, _ := New(bytes.Repeat([]byte{8}, 32), "000000001234abcd")
	for _, test := range []struct {
		vault   *Vault
		purpose string
	}{{other, "licence"}, {wrongKey, "licence"}, {v, "pickup"}} {
		if _, err = test.vault.Open(test.purpose, sealed); err == nil {
			t.Fatal("device/key/purpose mismatch accepted")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = v.Open("licence", sealed); err == nil {
		t.Fatal("tampering accepted")
	}
	for _, raw := range [][]byte{nil, {1}, {2, 3, 4}} {
		if _, err = v.Open("licence", raw); err == nil {
			t.Fatal("malformed envelope accepted")
		}
	}
	a, _ := v.Derive("pickup-encryption")
	b, _ := v.Derive("pickup-lookup")
	if bytes.Equal(a, b) {
		t.Fatal("purpose keys reused")
	}
	// Caller mutation must not change the copied installation seed.
	clear(seed)
	again, _ := v.Derive("pickup-encryption")
	if !bytes.Equal(a, again) {
		t.Fatal("seed aliased caller memory")
	}
}
func TestRejectInvalidIdentity(t *testing.T) {
	for _, serial := range []string{"", "0000000000000000", "some-host", "1234", "000000001234abcg"} {
		if _, err := New(make([]byte, 32), serial); err == nil {
			t.Fatalf("invalid identity accepted: %q", serial)
		}
	}
	if _, err := New([]byte{1}, "000000001234abcd"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestLinuxMachineIdentity(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, 32)
	v, err := New(seed, "linux-machine:0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	data, err := v.Seal("pickup", []byte("private"))
	if err != nil {
		t.Fatal(err)
	}
	restarted, _ := New(seed, "linux-machine:0123456789abcdef0123456789abcdef")
	if _, err = restarted.Open("pickup", data); err != nil {
		t.Fatal(err)
	}
	other, _ := New(seed, "linux-machine:0123456789abcdef0123456789abcdee")
	if _, err = other.Open("pickup", data); err == nil {
		t.Fatal("wrong machine accepted")
	}
}
