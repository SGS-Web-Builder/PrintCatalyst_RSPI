//go:build !windows

package traylauncher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// stubLauncher is the dev-machine implementation of Launcher. It
// prints the menu items to stderr so a developer running `go run
// ./cmd/print-catalyst-on-premise-tray` on macOS or Linux can see
// the launcher's actions without a Windows VM. The implementation
// never starts a real tray icon because no portable equivalent
// exists, but it does honour the same Options contract so the
// runtime binary's cross-platform build pipeline keeps working.
//
// The stub returns Status{Running: true, DashboardReachable:
// true} so a developer build that exercises the same code paths
// against a local runtime still sees a "service running" label in
// the menu.
type stubLauncher struct {
	options Options
}

// New is the dev-machine factory. It mirrors the Win32
// signature exactly so the caller does not have to fork on
// platform.
func New(options Options) (Launcher, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	options.fillDefaults()
	return &stubLauncher{options: options}, nil
}

// Run prints the menu items once and then blocks until the
// operator sends a signal. The function never returns an error:
// the dev stub is purely informational, so the caller's
// expectation is that Run() blocks until interrupted.
func (s *stubLauncher) Run() error {
	fmt.Fprintf(os.Stderr, "[tray-stub] Print Catalyst On-Premise tray dev stub\n")
	fmt.Fprintf(os.Stderr, "[tray-stub] Dashboard URL: %s\n", s.options.DashboardURL)
	fmt.Fprintf(os.Stderr, "[tray-stub] Data directory: %s\n", s.options.DataDirectory)
	fmt.Fprintf(os.Stderr, "[tray-stub] Auto-start: %t\n", s.options.AutoStart)
	for _, action := range []MenuAction{
		ActionOpenDashboard,
		ActionOpenDataFolder,
		ActionOpenTestPrint,
		ActionStartService,
		ActionStopService,
		ActionToggleAutoStart,
		ActionExit,
	} {
		fmt.Fprintf(os.Stderr, "[tray-stub] menu: %s\n", action.String())
	}
	fmt.Fprintf(os.Stderr, "[tray-stub] Press Ctrl+C to exit.\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-signals:
		fmt.Fprintf(os.Stderr, "[tray-stub] signal received, exiting.\n")
		return nil
	case <-ctx.Done():
		return nil
	case <-time.After(1 * time.Hour):
		// Dev stub is not expected to run for an hour, but
		// we keep the timeout defensive so a runaway dev
		// session does not leak goroutines.
		return nil
	}
}

// Status returns the documented stub answer. A dev build that
// runs alongside `go run ./cmd/print-catalyst-on-premise`
// typically has the runtime listening on 127.0.0.1:8080, so we
// report Running = true so the menu labels match what the
// operator sees in services.msc on a real Windows install.
func (s *stubLauncher) Status() Status {
	return Status{
		Running:             true,
		StartType:           "manual",
		DashboardReachable:  true,
	}
}

// sentinel so go vet flags unused imports when this file is
// compiled into a Windows build (the build tag guards against
// that, but the import list still has to be valid).
var _ = errors.New
