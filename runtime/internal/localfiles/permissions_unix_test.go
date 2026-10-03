//go:build !windows

package localfiles

import (
	"os"
	"testing"
)

func assertRestrictedFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("permissions = %o, want owner-only", info.Mode().Perm())
	}
}
