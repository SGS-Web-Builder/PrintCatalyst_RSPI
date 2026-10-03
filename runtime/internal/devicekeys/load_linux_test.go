//go:build linux

package devicekeys

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialFilePolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	key := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readCredential(path)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatal("private key rejected", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readCredential(path); err == nil {
		t.Fatal("public key file accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = readCredential(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err = readCredential(dir); err == nil {
		t.Fatal("directory accepted")
	}
	if err = os.WriteFile(path, append(key, 0), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readCredential(path); err == nil {
		t.Fatal("oversized key accepted")
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, err = Load(); err == nil {
		t.Fatal("missing credential silently generated")
	}
}
