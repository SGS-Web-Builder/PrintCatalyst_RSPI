//go:build !windows

package eventlog

import (
	"fmt"
	"io"
)

// openPlatform is the non-Windows implementation of Open. We always
// fall back to a protected rotating log file because Linux/macOS
// have no built-in event log; operators investigating a service
// failure need a documented place to look.
func openPlatform(dataDirectory string) (Writer, error) {
	if dataDirectory == "" {
		return nil, fmt.Errorf("eventlog: non-Windows platforms require a data directory for the file fallback")
	}
	// 4 MiB per file, three backups = 16 MiB total. Anything larger
	// would risk filling the protected data directory; anything
	// smaller would make rotation too aggressive to be useful during
	// long debugging sessions.
	return NewFileWriter(dataDirectory, 4<<20)
}

// noopWriter is reserved for future use when the runtime needs to
// disable diagnostic logging entirely (for example when running
// inside an isolated test that should not touch the filesystem).
type noopWriter struct{ io.Writer }

func (noopWriter) Info(string) error    { return nil }
func (noopWriter) Warning(string) error { return nil }
func (noopWriter) Error(string) error   { return nil }
func (noopWriter) Close() error         { return nil }