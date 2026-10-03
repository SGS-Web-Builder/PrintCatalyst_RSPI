//go:build !windows

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenProtectsExistingDatabaseDirectoryAndSidecars(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runtime.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{root, path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("%s permissions %o expose local data", name, info.Mode().Perm())
		}
	}
}
