package localserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestRemoteUpdateSignatureBindsExactArchive(t *testing.T) {
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("synthetic archive")
	hash := sha256.Sum256(data)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("PrintCatalyst-Pi-update-v1\n"+hex.EncodeToString(hash[:])+"\n")))
	key := []byte(base64.StdEncoding.EncodeToString(pub))
	if _, e = verifyUpdate(data, signature, key); e != nil {
		t.Fatal(e)
	}
	if _, e = verifyUpdate(append(data, '!'), signature, key); e == nil {
		t.Fatal("modified archive accepted")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, e = verifyUpdate(data, signature, []byte(base64.StdEncoding.EncodeToString(other))); e == nil {
		t.Fatal("untrusted publisher accepted")
	}
}
