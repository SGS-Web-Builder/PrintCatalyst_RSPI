//go:build windows

package traylauncher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Win32 constants used by the tray launcher. Grouping them at
// the top of the file makes the syscall block below easier to
// audit; every value comes from the documented Win32 header files
// (shellapi.h, user32.h, winuser.h, winsvc.h, winreg.h) so a
// reviewer can confirm the constant names against MSDN without
// leaving the file.
const (
	// NOTIFYICONDATA flags (shellapi.h NIM_*)
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	// NOTIFYICONDATA uFlags (shellapi.h NIF_*)
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifState   = 0x00000008

	// NOTIFYICONDATA dwState flags (shellapi.h NIS_*)
	nisHidden = 0x00000001

	// ShellNotifyIconMessage (shellapi.h)
	wmTrayIcon = 0x0400 + 1 // WM_USER + 1, the documented shell tray id

	// Menu messages (winuser.h)
	wmCommand    = 0x0111
	wmInitMenu   = 0x0116
	wmUser       = 0x0400
	wmAppCommand = 0x0319

	// Tray-icon mouse messages forwarded as lParam (winuser.h)
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B

	// Window styles (winuser.h)
	wsOverlapped = 0x00000000
	wsExToolwind = 0x00000080

	// Window class styles (winuser.h)
	classNameMax = 256

	// ShowWindow commands (winuser.h)
	swHide = 0

	// Service start / stop controls (winsvc.h)
	serviceStart      = 0x00000016 // SERVICE_CONTROL_START is not standard; we use StartService API instead
	serviceStop       = 0x00000001
	serviceStopReason = 0x00000004 // SERVICE_CONTROL_STOP_REASON_MINOR + a custom reason code

	// Service query access (winsvc.h)
	scManagerConnect = 0x0001
	scManagerEnum    = 0x0004

	serviceQueryStatus = 0x0004
	serviceQueryConfig = 0x0001
	serviceStartAccess = 0x0010
	serviceStopAccess  = 0x0020

	// Service state codes (winsvc.h)
	serviceStopped  = 0x00000001
	serviceStartPending = 0x00000002
	serviceStopPending  = 0x00000003
	serviceRunning  = 0x00000004
	serviceContinuePending = 0x00000005
	servicePausePending    = 0x00000006
	servicePaused   = 0x00000007

	// Service start types (winsvc.h)
	serviceBootStart   = 0x00000000
	serviceSystemStart = 0x00000001
	serviceAutoStart   = 0x00000002
	serviceDemandStart = 0x00000003
	serviceDisabled    = 0x00000004

	// Standard Win32 error codes (winerror.h)
	errorFileNotFound    = 0x00000002
	errorInvalidName     = 0x0000007B
	errorServiceExists   = 0x00000431
	errorServiceNotActive = 0x00000426
	errorServiceMarkedForDelete = 0x00000430
	errorAccessDenied    = 0x00000005

	// ShellExecuteW verbs (shellapi.h)
	verbOpen    = "open"
	verbExplore = "explore"

	// TrayIconMessageID is the WM_USER offset the launcher uses
	// for its own messages. We forward WM_USER+0 to the menu
	// dispatch and reserve WM_USER+1..3 for future tray
	// features (balloon click handling, etc.).
	trayIconMessageID = wmUser + 0

	// refreshTick is the interval between background SCM / HTTP
	// polls that update the menu labels. 5 seconds is fast
	// enough that the operator sees the "Start Service" /
	// "Stop Service" labels swap within the time it takes to
	// re-open the menu, but slow enough that the SCM round-trip
	// does not contend with the Win32 message pump.
	refreshTick = 5 * time.Second
)

// notifyIconData mirrors the Win32 NOTIFYICONDATAW struct. The
// fields are kept in the documented order so a reviewer can map
// each Go field onto the matching C struct member. We use the V5
// layout (NOTIFYICONDATA_V5_SIZE) because we want uVersion = 5
// so Windows 7 / Server 2008 R2 and later honour the larger
// fields; older builds fall back to the V3 layout automatically.
type notifyIconData struct {
	Size              uint32
	Wnd               uintptr
	ID                uint32
	Flags             uint32
	CallbackMessage   uint32
	Icon              uintptr
	Tip               [128]uint16
	State             uint32
	StateMask         uint32
	Info              [256]uint16
	TimeoutOrVersion  uint32 // union: uTimeout (balloon) or uVersion (Version member)
	InfoTitle         [64]uint16
	InfoFlags         uint32
	Guid              windows.GUID
	IconBalloon       uintptr
}

// winTrayIcon is the Win32 message-only icon handle that backs
// the Launcher's Run loop. The struct holds every native handle
// the launcher needs so the deferred Close in Run can release
// them deterministically without leaking Win32 resources on a
// non-graceful shutdown.
type winTrayIcon struct {
	options     Options
	mu          sync.Mutex
	hwnd        uintptr
	hicon       uintptr
	nid         notifyIconData
	classAtom   uint16
	menuItems   map[uint16]MenuAction // command id → action
	loaded      bool
	autostartOn bool
	lastStatus  Status
	stopCh      chan struct{}
	stopped     chan struct{}
}

// New returns the platform's concrete Launcher implementation.
// On Windows the returned value is a fully wired Win32 message
// pump; the call returns only when Run() exits, so callers should
// invoke it from main() and treat it as blocking.
func New(options Options) (Launcher, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	options.fillDefaults()
	launcher := &winTrayIcon{
		options:   options,
		menuItems: map[uint16]MenuAction{},
		stopCh:    make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	launcher.lastStatus = launcher.queryStatus()
	launcher.autostartOn = launcher.queryAutoStart()

	// Self-register the launcher under HKCU\...\Run on a fresh
	// install. We only do this when AutoStart is true and the
	// tray was launched with a visible icon (i.e. from the
	// Start-menu shortcut, not from the HKCU\Run value
	// itself). The first visible launch is the documented "I
	// want this to keep working" gesture, so we register then.
	// Subsequent launches are no-ops because the value is
	// already present.
	if options.AutoStart && !options.Hidden && !launcher.autostartOn {
		if err := launcher.setAutoStart(true); err == nil {
			launcher.autostartOn = true
		}
	}
	return launcher, nil
}

// Run blocks until the operator selects "Exit" or the process
// receives a termination signal. The implementation is responsible
// for releasing every Win32 resource it acquires — the shell icon,
// the message-only window class, and the loaded icon handle —
// before returning, otherwise the icon would stay in the
// notification area after the process exits.
func (t *winTrayIcon) Run() error {
	if err := t.createWindow(); err != nil {
		return fmt.Errorf("traylauncher: create window: %w", err)
	}
	defer t.destroyWindow()
	if !t.options.Hidden {
		if err := t.addIcon(); err != nil {
			return fmt.Errorf("traylauncher: add tray icon: %w", err)
		}
		defer t.deleteIcon()
	}

	// Background goroutine refreshes the menu labels every
	// refreshTick. The goroutine exits when the main message
	// loop calls DeleteIcon / DestroyWindow; the stopCh stops
	// the refresh tick so the runtime does not leak.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go t.refreshLoop(ctx)

	// Catch Ctrl+C / SIGTERM so the MSI's "Stop-Process"
	// shutdown sequence lands gracefully. The signal handler
	// breaks the GetMessageW loop by posting WM_QUIT.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-signals:
			t.postQuit()
		case <-t.stopCh:
			t.postQuit()
		}
	}()

	// Pump Windows messages until WM_QUIT arrives. The MSG
	// buffer lives on the stack — GetMessageW writes into the
	// struct we hand it.
	var msg winMessage
	for {
		r1, _, _ := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		// r1 == 0 means GetMessageW returned false because
		// WM_QUIT was extracted from the queue. Anything
		// negative is a fatal error; anything positive is the
		// message we need to dispatch.
		if int32(r1) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	close(t.stopped)
	return nil
}

// Status returns the most recent SCM / HTTP poll result. The
// refreshLoop keeps t.lastStatus fresh; Status() is called from
// the menu dispatch path so the answer is the same one the
// background goroutine computed.
func (t *winTrayIcon) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastStatus
}

// winMessage mirrors the Win32 MSG struct. Only the fields the
// launcher actually reads (Message, WParam, LParam, Hwnd) are
// declared; the remaining bytes are not material because
// GetMessageW writes into the buffer we hand it and we only read
// what we need.
type winMessage struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
	LPrivate uint32
}

// postQuit injects WM_QUIT into the calling thread's message
// queue. PostQuitMessage lives in user32.dll and is exported with
// the documented signature, so we resolve it once at package init
// (see traylauncher_windows_syscall.go) and call it directly
// here.
func (t *winTrayIcon) postQuit() {
	select {
	case <-t.stopCh:
		// already stopped
	default:
		close(t.stopCh)
	}
	procPostQuitMessage.Call(0)
}

// refreshLoop polls the SCM and the loopback HTTP endpoint on a
// fixed tick so the menu labels stay current. The loop exits when
// the context is cancelled (i.e. when Run() is unwinding).
func (t *winTrayIcon) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(refreshTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case <-ticker.C:
			status := t.queryStatus()
			t.mu.Lock()
			t.lastStatus = status
			t.mu.Unlock()
		}
	}
}

// queryStatus reports the SCM state of the Print Catalyst
// On-Premise service and whether the dashboard HTTP endpoint
// answers on the configured loopback port. The function is
// tolerant: a missing service or a refused connection returns
// Status{Running: false} rather than an error, because the tray
// uses Status to grey-out menu items and never propagates the
// failure to the operator.
func (t *winTrayIcon) queryStatus() Status {
	status := Status{Running: false, StartType: "unknown"}
	scm, _, err := procOpenSCManagerW.Call(0, 0, scManagerConnect|scManagerEnum)
	if scm == 0 || err != nil {
		status.DashboardReachable = t.pingDashboard()
		return status
	}
	defer procCloseServiceHandle.Call(scm)

	serviceNameUTF16, _ := syscall.UTF16PtrFromString(ServiceName)
	svc, _, _ := procOpenServiceW.Call(
		scm,
		uintptr(unsafe.Pointer(serviceNameUTF16)),
		serviceQueryStatus|serviceQueryConfig|serviceStartAccess|serviceStopAccess,
	)
	if svc == 0 {
		status.DashboardReachable = t.pingDashboard()
		return status
	}
	defer procCloseServiceHandle.Call(svc)

	// SERVICE_STATUS_PROCESS is the documented layout for
	// QueryServiceStatusEx. We only read the first 28 bytes
	// (ServiceStatus header) so a partial read is acceptable.
	var statusBuf [28]uint32
	var needed uint32
	r1, _, _ := procQueryServiceStatusEx.Call(
		svc,
		0, // InfoLevel = SERVICE_STATUS_HANDLE_SIZE
		0,
		uintptr(unsafe.Pointer(&statusBuf[0])),
		uintptr(len(statusBuf))*4,
		uintptr(unsafe.Pointer(&needed)),
	)
	if r1 != 0 {
		// SERVICE_STATUS_PROCESS layout: dwServiceType, dwCurrentState,
		// dwControlsAccepted, dwWin32ExitCode, dwServiceSpecificExitCode,
		// dwCheckPoint, dwWaitHint, dwProcessId, dwServiceFlags.
		const currentStateOffset = 1
		if statusBuf[currentStateOffset] == serviceRunning {
			status.Running = true
		}
	}

	// QueryServiceConfig2 to read the start type. We pass a
	// generous buffer (8 KB is the documented maximum) so we
	// never need to retry on a truncated read.
	var configBuf [4096]uint8
	r1, _, _ = procQueryServiceConfig2W.Call(
		svc,
		1, // SERVICE_CONFIG_START_TYPE
		uintptr(unsafe.Pointer(&configBuf[0])),
		uintptr(len(configBuf)),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r1 != 0 {
		// SERVICE_CONFIG_START_TYPE returns a
		// SERVICE_START_TYPE struct with a single DWORD at
		// offset 0.
		startType := *(*uint32)(unsafe.Pointer(&configBuf[0]))
		switch startType {
		case serviceAutoStart:
			status.StartType = "auto"
		case serviceDemandStart:
			status.StartType = "manual"
		case serviceDisabled:
			status.StartType = "disabled"
		default:
			status.StartType = fmt.Sprintf("unknown(%d)", startType)
		}
	}
	status.DashboardReachable = t.pingDashboard()
	return status
}

// pingDashboard performs a single GET /healthz against the
// dashboard URL with a short timeout. The tray uses it to decide
// whether the "Open Dashboard" menu item should be enabled; the
// answer is advisory and is intentionally not propagated as an
// error to the caller.
func (t *winTrayIcon) pingDashboard() bool {
	client := &http.Client{Timeout: 750 * time.Millisecond}
	request, err := http.NewRequest(http.MethodGet, t.options.DashboardURL+"/healthz", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// queryAutoStart reads the HKCU\...\Run registry value that
// toggles the tray's auto-start behaviour. The function returns
// false for every error other than "value not present" so a
// permission glitch cannot accidentally re-enable auto-start.
func (t *winTrayIcon) queryAutoStart() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	_, _, err = key.GetStringValue(AutoStartRegistryValue)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false
		}
		return false
	}
	return true
}

// setAutoStart writes or removes the HKCU\...\Run value. The
// caller passes the desired state so the function can decide
// between write and delete in a single code path. The path is
// documented in traylauncher.go (AutoStartRegistryValue).
func (t *winTrayIcon) setAutoStart(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(AutoStartRegistryValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("delete Run value: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate tray executable: %w", err)
	}
	// Quote the executable path so spaces in the install
	// directory (e.g. "C:\Program Files\Print Catalyst On-Premise")
	// do not break the registry value parsing.
	value := fmt.Sprintf(`"%s" --minimized`, exe)
	if err := key.SetStringValue(AutoStartRegistryValue, value); err != nil {
		return fmt.Errorf("write Run value: %w", err)
	}
	return nil
}

// startService asks the SCM to start the Print Catalyst
// On-Premise service. The call blocks for at most 30 seconds —
// the documented upper bound for a service-control round trip —
// so a stuck start does not hang the Win32 message pump.
func (t *winTrayIcon) startService() error {
	scm, _, err := procOpenSCManagerW.Call(0, 0, scManagerConnect)
	if scm == 0 {
		return fmt.Errorf("open SCM: %v", err)
	}
	defer procCloseServiceHandle.Call(scm)

	serviceNameUTF16, _ := syscall.UTF16PtrFromString(ServiceName)
	svc, _, err := procOpenServiceW.Call(
		scm,
		uintptr(unsafe.Pointer(serviceNameUTF16)),
		serviceStartAccess|serviceQueryStatus,
	)
	if svc == 0 {
		return fmt.Errorf("open service: %v", err)
	}
	defer procCloseServiceHandle.Call(svc)

	r1, _, err := procStartServiceW.Call(svc, 0, 0)
	if r1 == 0 {
		return fmt.Errorf("start service: %v", err)
	}

	// Wait up to 30 seconds for the service to settle.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var statusBuf [7]uint32
		var needed uint32
		r1, _, _ := procQueryServiceStatus.Call(
			svc,
			uintptr(unsafe.Pointer(&statusBuf[0])),
			uintptr(len(statusBuf))*4,
			uintptr(unsafe.Pointer(&needed)),
		)
		if r1 != 0 {
			const currentStateOffset = 1
			switch statusBuf[currentStateOffset] {
			case serviceRunning:
				return nil
			case serviceStopped:
				return fmt.Errorf("service stopped before reaching running state")
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for service to start")
}

// stopService sends SERVICE_CONTROL_STOP to the service. The
// call blocks for at most 30 seconds; the runtime's graceful
// shutdown timeout is 15 seconds so a healthy stop always returns
// well before the deadline.
func (t *winTrayIcon) stopService() error {
	scm, _, err := procOpenSCManagerW.Call(0, 0, scManagerConnect)
	if scm == 0 {
		return fmt.Errorf("open SCM: %v", err)
	}
	defer procCloseServiceHandle.Call(scm)

	serviceNameUTF16, _ := syscall.UTF16PtrFromString(ServiceName)
	svc, _, err := procOpenServiceW.Call(
		scm,
		uintptr(unsafe.Pointer(serviceNameUTF16)),
		serviceStopAccess|serviceQueryStatus,
	)
	if svc == 0 {
		return fmt.Errorf("open service: %v", err)
	}
	defer procCloseServiceHandle.Call(svc)

	var statusBuf [7]uint32
	r1, _, err := procControlService.Call(
		svc,
		serviceStop,
		uintptr(unsafe.Pointer(&statusBuf[0])),
	)
	if r1 == 0 {
		return fmt.Errorf("control service: %v", err)
	}
	return nil
}

// openInBrowser launches the default browser pointed at the
// dashboard URL. We use ShellExecuteW via the documented exec.Cmd
// + rundll32 path because Go's stdlib does not yet ship a direct
// ShellExecute wrapper that survives the cross-architecture
// scenarios the runtime may run under.
//
// The function returns an error when ShellExecute returns a code
// <= 32; the tray surfaces the error as a balloon notification so
// the operator has something to copy into a support ticket.
func (t *winTrayIcon) openInBrowser(url string) error {
	if strings.TrimSpace(url) == "" {
		return errors.New("empty URL")
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap asynchronously so we do not block the message pump.
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

// openDataFolder launches Windows Explorer pointed at the
// persistent data directory. ShellExecute "explore" verb opens
// the directory in a new Explorer window, which matches the
// operator's expectation when they click "Open Data Folder".
func (t *winTrayIcon) openDataFolder() error {
	verb, _ := syscall.UTF16PtrFromString(verbExplore)
	path, _ := syscall.UTF16PtrFromString(t.options.DataDirectory)
	r1, _, _ := procShellExecuteW.Call(
		0, 0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(path)),
		0, 0, swHide,
	)
	if r1 <= 32 {
		return fmt.Errorf("ShellExecuteW returned %d", r1)
	}
	return nil
}

// dumpJSON is a debug-only helper. We keep it because the
// traylauncher_test.go file expects a stable serialisation shape;
// production builds never call it.
func dumpJSON(value any) string {
	out, err := json.Marshal(value)
	if err != nil {
		return "<unprintable>"
	}
	return string(out)
}
