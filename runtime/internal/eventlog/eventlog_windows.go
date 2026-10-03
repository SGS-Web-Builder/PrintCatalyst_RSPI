//go:build windows

package eventlog

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/eventlog"
)

// openPlatform is the Windows implementation of Open. It writes to
// the registered "Print Catalyst On-Premise" source under the
// Application log when the source exists; otherwise it falls back to
// a protected rotating log file inside the data directory so a
// service started before the MSI has registered the source still
// has a documented place to write diagnostic messages.
//
// The Win32 Event Log records events with explicit severity IDs; the
// cross-platform Writer uses three levels (Info, Warning, Error).
// The IDs are fixed constants — anything else would force the
// installer to ship a matching EventMessageFile and a localised
// message table, which is overkill for a service that already
// streams the same payload to its own rotating log under dataDir.
func openPlatform(dataDirectory string) (Writer, error) {
	// Fast path: the MSI has registered the source and the source is
	// present in the registry. eventlog.Open validates the source
	// exists before returning a usable handle, so a successful Open
	// is itself proof the MSI step ran on this machine.
	if source, err := eventlog.Open(EventSourceName); err == nil {
		wrapped := &winEventLogWriter{log: source}
		if dataDirectory == "" {
			return wrapped, nil
		}
		// The data directory may not be ready on the very first start
		// of an unconfigured installation; keep the Event Log writer
		// and add a file fallback when it is available so the operator
		// can still recover the message via tail dataDir/logs/runtime.log.
		if file, fileErr := NewFileWriter(dataDirectory, 4<<20); fileErr == nil {
			return &MultiWriter{writers: []Writer{wrapped, file}}, nil
		}
		return wrapped, nil
	} else if !isEventLogMissing(err) {
		return nil, fmt.Errorf("eventlog: open %s source: %w", EventSourceName, err)
	}
	// Source not registered (common in dev builds that run the
	// binary directly without an MSI install). Fall back to the file
	// writer so diagnostics remain observable.
	if dataDirectory == "" {
		return nil, errors.New("eventlog: Windows Event Log source is not registered and no data directory is available for the file fallback")
	}
	return NewFileWriter(dataDirectory, 4<<20)
}

// winEventLogWriter adapts *eventlog.Log (the Win32-backed handle
// from golang.org/x/sys) to the cross-platform Writer interface.
// The Info / Warning / Error methods map to the corresponding
// Win32 event types so the messages surface in the right bucket in
// the Windows Event Viewer. The underlying Log uses Error(uint32,
// string); the IDs below are the values documented for the
// registered "Print Catalyst On-Premise" source.
type winEventLogWriter struct {
	log *eventlog.Log
}

const (
	winInfoEventID    uint32 = 1
	winWarningEventID uint32 = 2
	winErrorEventID   uint32 = 3
)

func (w *winEventLogWriter) Info(message string) error {
	return w.log.Info(winInfoEventID, message)
}

func (w *winEventLogWriter) Warning(message string) error {
	return w.log.Warning(winWarningEventID, message)
}

func (w *winEventLogWriter) Error(message string) error {
	return w.log.Error(winErrorEventID, message)
}

func (w *winEventLogWriter) Close() error {
	if w.log == nil {
		return nil
	}
	return w.log.Close()
}

// isEventLogMissing returns true when the Windows error indicates
// the source has not been registered. The numeric value 15005 is
// ERROR_EVENTLOG_CANT_START; a missing-source Open returns that
// code wrapped in a syscall.Errno.
func isEventLogMissing(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.Errno(15005) || errno == windows.ERROR_EVENTLOG_CANT_START
	}
	// Fall back to a registry probe. The EventLog sub-tree is
	// HKLM\SYSTEM\CurrentControlSet\Services\EventLog\Application;
	// a missing key or value means the MSI has not run.
	key, regErr := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\EventLog\Application\`+EventSourceName,
		registry.QUERY_VALUE)
	if regErr != nil {
		return true
	}
	_ = key.Close()
	return false
}
