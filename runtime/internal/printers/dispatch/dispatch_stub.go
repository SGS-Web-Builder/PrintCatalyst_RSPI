//go:build !windows

// dispatch_stub.go provides stubs for the Windows-only winspool backend
// on non-Windows platforms (macOS, Linux). This lets `go build` succeed
// on development machines without requiring a Windows VM.

package dispatch

// WinspoolAvailable is false on non-Windows platforms so main.go skips
// dispatcher backend initialization during development.
func WinspoolAvailable() bool { return false }

// NewWinspoolBackend returns nil on non-Windows platforms so the Go
// compiler does not error out when referencing it in if blocks.
func NewWinspoolBackend() PrinterBackend { return nil }
