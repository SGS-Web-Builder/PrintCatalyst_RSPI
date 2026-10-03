//go:build linux

package prepared

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxPrivateDirectoryAndSymlinkRejection(t *testing.T) {
	s, dir := fixture(t)
	id, err := s.Publish("order", input())
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.pdf")
	if err = os.WriteFile(external, input()[0].PDF, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, id, "000.pdf")
	if err = os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(external, artifact); err != nil {
		t.Fatal(err)
	}
	if _, data, err := s.Load("order", id); err == nil || data != nil {
		t.Fatal("symlink accepted")
	}
	link := filepath.Join(t.TempDir(), "linked-root")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(link); err == nil {
		other.Close()
		t.Fatal("symlink root accepted")
	}
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(dir); err == nil {
		other.Close()
		t.Fatal("public directory accepted")
	}
}
