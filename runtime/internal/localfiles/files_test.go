package localfiles

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomicRejectsPathsOutsideRoot(t *testing.T) {
	files, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape.pdf", "/tmp/escape.pdf", `C:\escape.pdf`, ""} {
		if err := files.WriteAtomic(name, strings.NewReader("secret")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("WriteAtomic(%q) error = %v, want ErrUnsafePath", name, err)
		}
	}
}

func TestWriteAtomicReplacesCompleteFileWithRestrictedPermissions(t *testing.T) {
	root := t.TempDir()
	files, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteAtomic("orders/order-1/document.pdf", strings.NewReader("old")); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteAtomic("orders/order-1/document.pdf", strings.NewReader("new-complete")); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "orders", "order-1", "document.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "new-complete" {
		t.Fatalf("contents = %q", contents)
	}
	assertRestrictedFile(t, filepath.Join(root, "orders", "order-1", "document.pdf"))
}

func TestInterruptedWriteLeavesOldFileAndRemovesTemporaryFile(t *testing.T) {
	root := t.TempDir()
	files, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteAtomic("document.pdf", strings.NewReader("retained")); err != nil {
		t.Fatal(err)
	}
	err = files.WriteAtomic("document.pdf", &failingReader{})
	if err == nil {
		t.Fatal("WriteAtomic() succeeded, want reader failure")
	}
	contents, readErr := os.ReadFile(filepath.Join(root, "document.pdf"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "retained" {
		t.Fatalf("contents = %q", contents)
	}
	temporary, err := filepath.Glob(filepath.Join(root, ".pc-write-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v", temporary)
	}
}

func TestWriteAtomicFailsBeforeReadingWhenCapacityIsLow(t *testing.T) {
	reader := &countingReader{}
	files, err := New(t.TempDir(), WithMinimumFreeBytes(1024), withCapacityProbe(func(string) (uint64, error) { return 100, nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = files.WriteAtomic("document.pdf", reader)
	var capacityError *CapacityError
	if !errors.As(err, &capacityError) {
		t.Fatalf("error = %v, want CapacityError", err)
	}
	if reader.reads != 0 {
		t.Fatalf("reader was consumed %d times", reader.reads)
	}
}

func TestOpenAndRemoveStayInsideRoot(t *testing.T) {
	files, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteAtomic("orders/a.pdf", strings.NewReader("pdf")); err != nil {
		t.Fatal(err)
	}
	opened, err := files.Open("orders/a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(opened)
	_ = opened.Close()
	if err != nil || string(contents) != "pdf" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if err := files.Remove("orders/a.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Open("orders/a.pdf"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open after Remove error = %v", err)
	}
}

type failingReader struct{ delivered bool }

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if reader.delivered {
		return 0, errors.New("camera disconnected")
	}
	reader.delivered = true
	return copy(buffer, "partial"), nil
}

type countingReader struct{ reads int }

func (reader *countingReader) Read([]byte) (int, error) {
	reader.reads++
	return 0, io.EOF
}
