// Package traylauncher implements the Print Catalyst On-Premise
// Windows tray launcher. The launcher is a separate user-mode
// executable that runs alongside the service binary: it shows a
// notification-area icon, lets the operator open the dashboard
// with one click, surfaces the service start/stop status, and
// auto-starts when the merchant logs in.
//
// The launcher does NOT host any business logic. Every privileged
// action (start/stop the service, read printer state, etc.) is
// routed through the platform's documented boundary — Win32 SCM
// for service control, the loopback HTTP API for read-only state
// — so a compromise of the launcher cannot escalate into a
// compromise of the runtime.
//
// The package compiles on every supported platform so the rest of
// the codebase can build cleanly on a developer macOS / Linux
// machine. The non-Windows build tag returns a no-op Launcher so
// `go build ./...` and `go test ./...` succeed outside Windows
// without dragging in golang.org/x/sys/windows.
package traylauncher

import (
	"errors"
	"fmt"
	"strings"
)

// ServiceName is the Windows service name registered by the MSI.
// Kept exported so the tray can call ControlService on the same
// service the MSI installs. The constant must match the value in
// platform_windows.go and Product.wxs; a typo here would make the
// "Start Service" / "Stop Service" menu items no-ops on the real
// installation.
const ServiceName = "PrintCatalystOnPremise"

// AppUserModelID is the Windows Application User Model ID the
// launcher registers for the dashboard shortcut. Using a stable
// AUMID lets the operator pin the tray-launched dashboard window
// to the taskbar and lets Windows group taskbar buttons
// consistently across restarts. The value is owned by the runtime
// and matches the Start-menu shortcut installed by the MSI.
const AppUserModelID = "PrintCatalyst.OnPremise.Tray"

// AutoStartRegistryValue is the value name written under
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run when the
// tray's "Start on login" toggle is enabled. The constant is
// exported so the uninstall step can clean up without a string
// drift between the install and remove paths.
const AutoStartRegistryValue = "PrintCatalystOnPremiseTray"

// TrayIconFileName is the on-disk filename the launcher expects
// for the notification-area icon. The MSI installs the icon next
// to the tray executable under the same install directory so the
// launcher can resolve it via a relative path regardless of the
// Windows install drive letter.
const TrayIconFileName = "tray-icon.ico"

// Options bundles the operator-facing configuration the tray
// needs at startup. The struct is intentionally flat — every
// field is read once at startup and never mutated — so the Win32
// implementation can copy it into a value-typed callback without
// holding onto a reference.
type Options struct {
	// DashboardURL is the URL the tray opens when the operator
	// clicks "Open Dashboard". The runtime binds to loopback
	// only, so the default is http://127.0.0.1:8080/. Operators
	// who change PC_PORT override this through the same flag.
	DashboardURL string

	// DataDirectory is the directory the launcher's "Open Data
	// Folder" menu opens in Windows Explorer. The MSI installs
	// %PROGRAMDATA%\PrintCatalyst\OnPremise there; the value is
	// also used as the diagnostic log directory when the Event
	// Log source is unavailable.
	DataDirectory string

	// ServiceDisplayName is the friendly name shown next to the
	// service status label in the tray menu. Defaults to
	// "Print Catalyst On-Premise" when empty so a developer
	// build that bypasses the MSI still surfaces a sensible
	// label.
	ServiceDisplayName string

	// TestPrintURL is the absolute URL the "Print Test Page"
	// menu opens. The runtime serves the test print page as a
	// normal dashboard route; the tray does not need to know
	// anything about the document composition.
	TestPrintURL string

	// AutoStart controls whether the tray registers itself
	// under HKCU\...\Run at startup. The MSI flips this on by
	// default so the tray launches when the merchant signs in;
	// operators can toggle it off from the tray menu.
	AutoStart bool

	// Hidden controls whether the tray creates a visible
	// notification-area icon at startup. Hidden mode is used by
	// the installer's --install-autostart self-registration
	// step so the icon does not flash for every install.
	Hidden bool
}

// Validate normalises the options and returns an error when a
// required field is missing. Validate is called from New() so the
// caller does not have to remember which fields are mandatory;
// the rule is intentionally documented here rather than at every
// call site so future tray features (a "Send logs" action that
// needs the data directory, for example) only have to update this
// one function.
func (o *Options) Validate() error {
	if strings.TrimSpace(o.DashboardURL) == "" {
		return errors.New("traylauncher: DashboardURL is required")
	}
	if strings.TrimSpace(o.DataDirectory) == "" {
		return errors.New("traylauncher: DataDirectory is required")
	}
	if strings.TrimSpace(o.TestPrintURL) == "" {
		return errors.New("traylauncher: TestPrintURL is required")
	}
	return nil
}

// fillDefaults applies the documented fall-backs to any option
// field the caller left blank. fillDefaults is only called after
// Validate so a missing required value never gets silently
// replaced with a default.
func (o *Options) fillDefaults() {
	if strings.TrimSpace(o.ServiceDisplayName) == "" {
		o.ServiceDisplayName = "Print Catalyst On-Premise"
	}
}

// Status reports the launcher-facing state of the runtime. The
// tray polls this through the platform boundary (SCM on Windows,
// no-op elsewhere) and renders it as a label inside the menu so
// the operator can confirm the service is running without opening
// the dashboard.
type Status struct {
	// Running reports whether the Print Catalyst On-Premise
	// service is in the Running state. A stopped service still
	// leaves the tray icon visible so the operator can click
	// "Start Service" without first launching the dashboard.
	Running bool

	// StartType is the configured SCM start type ("manual",
	// "auto", "disabled"). The tray uses it to decide whether
	// the "Auto-start on login" toggle should be on by default.
	StartType string

	// DashboardReachable reports whether the dashboard HTTP
	// endpoint responds on the configured loopback port. The
	// tray uses it to grey-out the "Open Dashboard" menu item
	// when the service is stopped, and to surface a "service is
	// stopped" tooltip on the icon.
	DashboardReachable bool
}

// MenuAction identifies the menu entry the operator clicked. The
// Win32 message pump translates the WM_COMMAND id into one of
// these values and dispatches it to the Launcher's Run loop. New
// menu items must be added in lockstep with the constants below
// and the menu definition in traylauncher_windows.go.
type MenuAction int

const (
	// ActionOpenDashboard opens the dashboard URL in the
	// default browser via ShellExecute.
	ActionOpenDashboard MenuAction = iota + 1

	// ActionOpenDataFolder opens the persistent data directory
	// in Windows Explorer via ShellExecute "explore".
	ActionOpenDataFolder

	// ActionOpenTestPrint opens the dashboard URL with a
	// fragment that surfaces the test-print page. The runtime
	// already exposes a printer verification UI; the tray
	// delegates to it so a missing test-print endpoint cannot
	// desynchronise the tray's UX from the dashboard.
	ActionOpenTestPrint

	// ActionStartService starts the Print Catalyst On-Premise
	// Windows service via ControlService(SERVICE_CONTROL_INTERROGATE
	// is not appropriate — we issue a start via the SCM
	// StartService API). Disabled when the service is already
	// running or when the user lacks the SeLockMemoryPrivilege
	// / SCM write privilege.
	ActionStartService

	// ActionStopService stops the Print Catalyst On-Premise
	// Windows service via ControlService(SERVICE_CONTROL_STOP).
	// The Win32 SCM enforces the caller's access rights; the
	// tray surfaces the failure as a balloon notification rather
	// than silently no-op'ing.
	ActionStopService

	// ActionToggleAutoStart toggles the HKCU\...\Run registry
	// value that auto-starts the tray on the merchant's next
	// sign-in. The check-mark next to the menu item mirrors the
	// current registry state.
	ActionToggleAutoStart

	// ActionExit breaks the Win32 message loop and exits the
	// tray process. The MSI never triggers this path; the icon
	// stays visible until the merchant explicitly chooses to
	// hide it.
	ActionExit
)

// String returns the operator-facing label for a MenuAction.
// Used for log lines and for the menu definition itself so the
// resource file is the single source of truth for the menu text.
func (a MenuAction) String() string {
	switch a {
	case ActionOpenDashboard:
		return "Open Dashboard"
	case ActionOpenDataFolder:
		return "Open Data Folder"
	case ActionOpenTestPrint:
		return "Print Test Page…"
	case ActionStartService:
		return "Start Service"
	case ActionStopService:
		return "Stop Service"
	case ActionToggleAutoStart:
		return "Start with Windows"
	case ActionExit:
		return "Exit"
	default:
		return fmt.Sprintf("unknown action %d", int(a))
	}
}

// Launcher is the cross-platform contract for the tray icon's
// host process. main() in the tray command constructs the
// concrete implementation via New() and calls Run until the
// operator closes the icon.
//
// The interface keeps the surface tiny on purpose: every
// non-trivial operation lives inside the platform-specific
// implementation, so the entry-point binary stays readable and
// the Win32 bindings stay where the rest of the Win32 code
// lives (in *_windows.go files).
type Launcher interface {
	// Run blocks until the operator selects "Exit" or the
	// process receives a termination signal. The function
	// returns nil on a clean exit and a non-nil error when the
	// message pump or tray API fails in a way that prevents
	// further operation.
	Run() error

	// Status returns the current launcher-facing state of the
	// service. The Win32 implementation talks to the SCM and
	// the loopback HTTP listener; the stub returns
	// Status{Running: true} so a developer on macOS still sees
	// a visible icon.
	Status() Status
}
