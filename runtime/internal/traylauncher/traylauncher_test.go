package traylauncher

import (
	"runtime"
	"strings"
	"testing"
)

// TestOptionsValidate verifies that the launcher rejects
// obviously-broken Options structs and accepts the documented
// happy-path ones. The check lives in traylauncher.go (Validate)
// and is exercised here so a refactor that accidentally relaxes
// a constraint (e.g. by silently substituting an empty
// DataDirectory) fails this test.
func TestOptionsValidate(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		wantErr bool
	}{
		{
			name: "all fields present",
			options: Options{
				DashboardURL:  "http://127.0.0.1:8080/",
				DataDirectory: "C:\\ProgramData\\PrintCatalyst\\OnPremise",
				TestPrintURL:  "http://127.0.0.1:8080/printers/test-print",
			},
			wantErr: false,
		},
		{
			name: "missing dashboard URL",
			options: Options{
				DataDirectory: "C:\\ProgramData\\PrintCatalyst\\OnPremise",
				TestPrintURL:  "http://127.0.0.1:8080/printers/test-print",
			},
			wantErr: true,
		},
		{
			name: "missing data directory",
			options: Options{
				DashboardURL: "http://127.0.0.1:8080/",
				TestPrintURL: "http://127.0.0.1:8080/printers/test-print",
			},
			wantErr: true,
		},
		{
			name: "missing test print URL",
			options: Options{
				DashboardURL:  "http://127.0.0.1:8080/",
				DataDirectory: "C:\\ProgramData\\PrintCatalyst\\OnPremise",
			},
			wantErr: true,
		},
		{
			name: "whitespace-only fields rejected",
			options: Options{
				DashboardURL:  "  ",
				DataDirectory: "\t",
				TestPrintURL:  "",
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.options.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %t", err, tc.wantErr)
			}
		})
	}
}

// TestOptionsFillDefaults verifies that fillDefaults substitutes
// the documented defaults without touching fields the caller
// supplied. The check guards against a refactor that overwrites
// an operator-supplied display name with the hard-coded fallback.
func TestOptionsFillDefaults(t *testing.T) {
	options := Options{
		DashboardURL:       "http://127.0.0.1:8080/",
		DataDirectory:      "C:\\Data",
		TestPrintURL:       "http://127.0.0.1:8080/test",
		ServiceDisplayName: "Custom Display Name",
	}
	options.fillDefaults()
	if options.ServiceDisplayName != "Custom Display Name" {
		t.Errorf("fillDefaults overwrote a caller-supplied ServiceDisplayName: got %q", options.ServiceDisplayName)
	}

	options = Options{
		DashboardURL:  "http://127.0.0.1:8080/",
		DataDirectory: "C:\\Data",
		TestPrintURL:  "http://127.0.0.1:8080/test",
	}
	options.fillDefaults()
	if options.ServiceDisplayName != "Print Catalyst On-Premise" {
		t.Errorf("fillDefaults did not apply the default ServiceDisplayName: got %q", options.ServiceDisplayName)
	}
}

// TestMenuActionString confirms that the String() round-trips
// the documented label. The check guards against a refactor
// that renames a MenuAction (and therefore the menu item the
// operator sees) without a corresponding documentation update.
func TestMenuActionString(t *testing.T) {
	cases := []struct {
		action MenuAction
		want   string
	}{
		{ActionOpenDashboard, "Open Dashboard"},
		{ActionOpenDataFolder, "Open Data Folder"},
		{ActionOpenTestPrint, "Print Test Page…"},
		{ActionStartService, "Start Service"},
		{ActionStopService, "Stop Service"},
		{ActionToggleAutoStart, "Start with Windows"},
		{ActionExit, "Exit"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.action.String(); got != tc.want {
				t.Errorf("MenuAction(%d).String() = %q, want %q", int(tc.action), got, tc.want)
			}
		})
	}

	// An out-of-range value must produce the "unknown action"
	// sentinel so the menu builder never panics on a future
	// addition that is not yet reflected in String.
	if got := MenuAction(0).String(); !strings.Contains(got, "unknown") {
		t.Errorf("MenuAction(0).String() = %q, want 'unknown'", got)
	}
}

// TestServiceConstantsAreStable guards against a rename of the
// constants the Win32 SCM, registry, and Start-menu shortcut all
// share. Renaming any of these without updating every consumer
// would break the installer's auto-start wiring or the tray's
// "Start Service" action.
func TestServiceConstantsAreStable(t *testing.T) {
	if ServiceName != "PrintCatalystOnPremise" {
		t.Errorf("ServiceName drift: got %q", ServiceName)
	}
	if AppUserModelID != "PrintCatalyst.OnPremise.Tray" {
		t.Errorf("AppUserModelID drift: got %q", AppUserModelID)
	}
	if AutoStartRegistryValue != "PrintCatalystOnPremiseTray" {
		t.Errorf("AutoStartRegistryValue drift: got %q", AutoStartRegistryValue)
	}
	if TrayIconFileName != "tray-icon.ico" {
		t.Errorf("TrayIconFileName drift: got %q", TrayIconFileName)
	}
}

// TestNewReturnsStubOnNonWindows verifies the dev-machine
// launcher factory returns a non-nil Launcher on every platform.
// The platform-specific implementation is exercised by the
// Windows-only tests in traylauncher_windows_test.go; this test
// guards the cross-platform build.
func TestNewReturnsStubOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows stub contract")
	}
	options := Options{
		DashboardURL:  "http://127.0.0.1:8080/",
		DataDirectory: t.TempDir(),
		TestPrintURL:  "http://127.0.0.1:8080/dashboard/#/printers/test-print",
	}
	launcher, err := New(options)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if launcher == nil {
		t.Fatal("New returned nil launcher")
	}
	if got := launcher.Status(); !got.Running {
		t.Errorf("dev stub Status().Running = false, want true so the menu shows the documented labels")
	}
}
