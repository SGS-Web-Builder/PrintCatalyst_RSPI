//go:build !windows

// discover_stub_dev.go — development-time stub for the Win32 discoverer.
// This file exists ONLY so the runtime still compiles when a developer
// runs `go build` (or `go test`) on macOS / Linux. The production target
// is Windows-only; discover_windows.go provides the real implementation
// under the `//go:build windows` tag and is the file that actually ships.
//
// The stub emits a single synthetic record per tick so the dashboard can
// be exercised against an in-process test fixture, then idles on
// ctx.Done(). No real Win32 syscall is ever invoked from this stub.
package discover

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// errStubNoDriver is the diagnostic the dev stub attaches to its synthetic
// record. The dashboard renders it as "developer stub, no real driver" so
// a developer running on macOS can immediately tell the difference
// between a discovered Windows printer and the dev placeholder.
var errStubNoDriver = errors.New("developer stub: no Win32 driver available on this host")

// WindowsDiscoverer is the developer-mode stub. The real struct lives in
// discover_windows.go under the windows build tag; this local definition
// keeps the symbol resolvable on non-Windows dev hosts so the cross-build
// pipeline can compile-test the rest of the package.
type WindowsDiscoverer struct {
	mu    sync.Mutex
	known map[string]Discovered
}

// NewWindows returns a Win32 discoverer stub when not on Windows. The
// return type matches discover_windows.go so call sites compile without
// any platform branches.
func NewWindows() *WindowsDiscoverer {
	return &WindowsDiscoverer{known: map[string]Discovered{}}
}

// Backend identifies the discoverer.
func (d *WindowsDiscoverer) Backend() printers.Backend { return printers.BackendWindows }

// Watch emits a single development stub printer (named after the host
// process) and then idles. The stub printer has no capabilities because
// no real driver is available on non-Windows hosts; the dashboard renders
// it as "discovered, capabilities pending" so the merchant can see the
// discovery path is alive.
func (d *WindowsDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()
	d.emit(out)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.emit(out)
		}
	}
}

func (d *WindowsDiscoverer) emit(out chan<- Discovered) {
	rec := Discovered{
		Backend:     printers.BackendWindows,
		QueueName:   "DEV-STUB-PRINTER",
		DisplayName: "Developer Stub (no real driver)",
		DriverName:  "stub",
		URI:         "win32://dev-stub",
		Status:      printers.StatusError,
		LastSeenAt:  time.Now(),
		Error:       errStubNoDriver,
	}
	select {
	case out <- rec:
	default:
	}
}
