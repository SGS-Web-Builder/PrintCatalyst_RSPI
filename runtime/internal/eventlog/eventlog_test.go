package eventlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileWriterInfoPersistsLine(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewFileWriter(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if err := writer.Info("startup ok"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Warning("permission denied on /var/run"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Error("database unavailable"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "logs", "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, want := range []string{"INFO startup ok", "WARN permission denied", "ERROR database unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log missing %q; got:\n%s", want, text)
		}
	}
}

func TestFileWriterRefusesEmptyDataDirectory(t *testing.T) {
	if _, err := NewFileWriter("", 0); err == nil {
		t.Fatal("expected error when data directory is empty")
	}
}

func TestFileWriterRotatesWhenSizeExceeded(t *testing.T) {
	dir := t.TempDir()
	// Set the rotation threshold small enough that the second
	// write triggers it. Each line is ~60 bytes so 200 bytes is
	// two-and-a-bit lines.
	writer, err := NewFileWriter(dir, 200)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for i := 0; i < 10; i++ {
		if err := writer.Info("padding-padding-padding-padding-padding-padding-padding"); err != nil {
			t.Fatal(err)
		}
	}
	// runtime.log.1 must exist after the rotation ran at least
	// once.
	if _, err := os.Stat(filepath.Join(dir, "logs", "runtime.log.1")); err != nil {
		t.Fatalf("expected rotated log file: %v", err)
	}
}

func TestFileWriterCollapsesNewlines(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewFileWriter(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	// Newlines in the message would otherwise inject fake log
	// rows and break grep-based triage.
	if err := writer.Error("first\nsecond\nthird"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "logs", "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(contents))
	if strings.Count(text, "\n") != 0 {
		t.Fatalf("expected one line, got %d: %q", strings.Count(text, "\n"), text)
	}
}

func TestFileWriterConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewFileWriter(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			_ = writer.Info("info message")
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		_ = writer.Warning("warn message")
	}
	<-done
}

func TestMultiWriterFansOut(t *testing.T) {
	dir := t.TempDir()
	a, err := NewFileWriter(dir+"-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFileWriter(dir+"-b", 0)
	if err != nil {
		t.Fatal(err)
	}
	multi := &MultiWriter{writers: []Writer{a, b}}
	t.Cleanup(func() { _ = multi.Close() })
	if err := multi.Info("hello"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-a", "-b"} {
		path := filepath.Join(dir+suffix, "logs", "runtime.log")
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
		if !strings.Contains(string(contents), "INFO hello") {
			t.Fatalf("sub-writer %s missing message", path)
		}
	}
}