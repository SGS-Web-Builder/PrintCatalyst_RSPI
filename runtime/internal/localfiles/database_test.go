package localfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareDatabaseProtectsExistingFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"data.sqlite", "data.sqlite-wal", "data.sqlite-shm", "data.sqlite-journal"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("preserve existing content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := files.PrepareDatabase("data.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	assertRestrictedFile(t, filepath.Dir(path))
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		assertRestrictedFile(t, path+suffix)
		body, err := os.ReadFile(path + suffix)
		if err != nil || string(body) != "preserve existing content" {
			t.Fatalf("data changed: %q, %v", body, err)
		}
	}
}

func TestPrepareDatabaseRejectsUnsafeTarget(t *testing.T) {
	files, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.PrepareDatabase("../escape.sqlite"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.Mkdir(filepath.Join(files.root, "directory.sqlite"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := files.PrepareDatabase("directory.sqlite"); err == nil {
		t.Fatal("accepted non-file database")
	}
}
