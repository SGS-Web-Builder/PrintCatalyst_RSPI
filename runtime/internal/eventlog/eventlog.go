// Package eventlog wraps the operating system diagnostic log so the
// runtime can report startup, configuration and shutdown outcomes
// without losing the messages when stderr is detached from a
// console.
//
// On Windows the wrapper writes to the "Print Catalyst On-Premise"
// source registered by the MSI under the Application log. Reading
// the source with `Get-EventLog -LogName Application -Source
// 'Print Catalyst On-Premise'` shows the same messages a desktop
// operator would see during service investigation.
//
// On every other platform the wrapper falls back to writing a
// protected rotating log under [data directory]/logs/runtime.log
// so the same diagnostic surface is available during development
// and on production Linux/macOS installations.
package eventlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EventSourceName is the source registered by the Windows MSI. The
// non-Windows build tag uses the same constant so the value can be
// quoted in logging without diverging between platforms.
const EventSourceName = "Print Catalyst On-Premise"

// Writer is the cross-platform diagnostic surface. Implementations
// must be safe for concurrent use; the service supervisor calls
// Info / Warning / Error from multiple goroutines.
type Writer interface {
	Info(message string) error
	Warning(message string) error
	Error(message string) error
	Close() error
}

// Open returns a Writer that is appropriate for the running
// platform. On Windows it tries to open the registered Event Log
// source; if the source is missing (for example during the first
// start before the MSI has registered it, or in an integration test)
// it falls back to a protected rotating file log under
// dataDirectory/logs so diagnostic messages are never silently lost.
//
// A nil dataDirectory disables the file fallback and a nil or empty
// data directory on Windows returns a Writer that only logs to
// stderr, so tests that do not care about persistence still get a
// functional Writer.
func Open(dataDirectory string) (Writer, error) {
	return openPlatform(dataDirectory)
}

// MultiWriter fans writes out to every wrapped Writer. It is the
// fallback path used by the Windows implementation when the Event
// Log source is not yet registered.
type MultiWriter struct {
	writers []Writer
}

func (multi *MultiWriter) Info(message string) error {
	return multi.fanOut(func(w Writer) error { return w.Info(message) })
}
func (multi *MultiWriter) Warning(message string) error {
	return multi.fanOut(func(w Writer) error { return w.Warning(message) })
}
func (multi *MultiWriter) Error(message string) error {
	return multi.fanOut(func(w Writer) error { return w.Error(message) })
}
func (multi *MultiWriter) Close() error {
	var firstErr error
	for _, w := range multi.writers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (multi *MultiWriter) fanOut(op func(Writer) error) error {
	var firstErr error
	for _, w := range multi.writers {
		if err := op(w); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// FileWriter writes diagnostic messages to a protected rotating log
// file. The writer is intentionally minimal — it is not a general
// purpose logger — because the runtime only needs to surface
// startup, configuration and shutdown events to operators.
type FileWriter struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	maxBytes int64
}

// NewFileWriter opens (or creates) the runtime log file. The file
// lives under dataDirectory/logs with 0o600 permissions so it is
// only readable by the service account.
//
// The writer rotates on every Open if the existing file already
// exceeds maxBytes; older logs are renamed to runtime.log.1 etc.
// maxBytes <= 0 disables rotation and is useful for tests that do
// not want the writer to rename files behind them.
func NewFileWriter(dataDirectory string, maxBytes int64) (*FileWriter, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return nil, errors.New("eventlog: data directory is required for file writer")
	}
	logDir := filepath.Join(dataDirectory, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("eventlog: create log directory: %w", err)
	}
	writer := &FileWriter{path: filepath.Join(logDir, "runtime.log"), maxBytes: maxBytes}
	if err := writer.rotateLocked(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(writer.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open log file: %w", err)
	}
	writer.file = file
	return writer, nil
}

func (w *FileWriter) Info(message string) error    { return w.write("INFO", message) }
func (w *FileWriter) Warning(message string) error { return w.write("WARN", message) }
func (w *FileWriter) Error(message string) error   { return w.write("ERROR", message) }

func (w *FileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// write emits a single line in the format "YYYY-MM-DDTHH:MM:SSZ LEVEL
// message". The timestamp is RFC3339 so it round-trips through
// event viewer tooling on Windows and `date -Iseconds` on POSIX.
func (w *FileWriter) write(level, message string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return errors.New("eventlog: file writer is closed")
	}
	if err := w.rotateLocked(); err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s %s\n", time.Now().UTC().Format(time.RFC3339), level, singleLine(message))
	if _, err := io.WriteString(w.file, line); err != nil {
		return fmt.Errorf("eventlog: write to log file: %w", err)
	}
	return w.file.Sync()
}

// rotateLocked rotates the current log file when its size exceeds the
// configured ceiling. The caller must hold w.mu.
//
// Windows refuses to rename a file that has an open handle, so the
// current os.File is closed before the rename runs and reopened in
// append mode afterwards. POSIX is unaffected — a still-open handle
// can be renamed on Linux/macOS — but the same close-then-reopen
// sequence keeps the rotation logic identical on every supported
// platform.
func (w *FileWriter) rotateLocked() error {
	if w.maxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(w.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("eventlog: stat log file: %w", err)
	}
	if info.Size() < w.maxBytes {
		return nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("eventlog: close log file before rotate: %w", err)
		}
		w.file = nil
	}
	// Rotate up to three backups. Older files are discarded silently
	// because the operator is expected to ship the log directory
	// off-machine via the same protection pipeline as the rest of
	// the data root.
	for index := 3; index >= 1; index-- {
		older := fmt.Sprintf("%s.%d", w.path, index)
		newer := fmt.Sprintf("%s.%d", w.path, index+1)
		if _, err := os.Stat(older); err == nil {
			if index == 3 {
				_ = os.Remove(older)
				continue
			}
			_ = os.Rename(older, newer)
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("eventlog: rotate log file: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("eventlog: reopen log file after rotate: %w", err)
	}
	w.file = file
	return nil
}

func singleLine(message string) string {
	return strings.ReplaceAll(strings.TrimRight(message, "\n"), "\n", " ")
}