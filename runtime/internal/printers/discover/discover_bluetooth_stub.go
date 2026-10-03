//go:build !windows

// discover_bluetooth_stub.go — development-time stub for the Bluetooth discoverer.
// This file exists ONLY so the runtime still compiles when a developer
// runs `go build` (or `go test`) on macOS / Linux. The production target
// is Windows-only; discover_bluetooth_windows.go provides the real implementation
// under the `//go:build windows` tag and is the file that actually shipped.
//
// NewBluetooth always returns nil on non-Windows platforms so Bluetooth
// discovery is silently skipped. This matches the behaviour of the Win32
// stub in discover_stub_dev.go — there is no functional Bluetooth support
// on macOS/Linux in this release.
package discover

import (
	"context"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// BluetoothDiscoverer is declared here so the symbol resolves on non-Windows
// dev hosts. The real type and all its methods live in
// discover_bluetooth_windows.go.
type BluetoothDiscoverer struct{}

// NewBluetooth always returns nil on non-Windows hosts.
func NewBluetooth() *BluetoothDiscoverer {
	return nil
}

// Backend implements Discoverer (no-op on non-Windows).
func (*BluetoothDiscoverer) Backend() printers.Backend { return printers.BackendBluetooth }

// Watch implements Discoverer. The stub does nothing since there is no
// Bluetooth adapter on non-Windows hosts.
func (*BluetoothDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	// Drain the context and close the channel immediately.
	<-ctx.Done()
	close(out)
}
