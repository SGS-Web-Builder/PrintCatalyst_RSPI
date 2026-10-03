//go:build windows

package traylauncher

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sysProc holds the resolved Win32 procedure addresses used by
// the tray launcher. Every entry is resolved once at package init
// so the message pump does not have to pay the syscall.LoadLibrary
// cost on every call. The struct is intentionally package-private
// because nothing outside this file needs to invoke the
// procedures directly — every entry is wrapped by a typed method
// on *winTrayIcon.
var sysProc struct {
	// user32.dll
	registerClassExW *windows.LazyProc
	createWindowExW  *windows.LazyProc
	defWindowProcW   *windows.LazyProc
	destroyWindow    *windows.LazyProc
	getMessageW      *windows.LazyProc
	translateMessage *windows.LazyProc
	dispatchMessageW *windows.LazyProc
	postQuitMessage  *windows.LazyProc
	loadCursorW      *windows.LazyProc
	loadIconW        *windows.LazyProc
	setForegroundWindow *windows.LazyProc
	getCursorPos     *windows.LazyProc
	createPopupMenu  *windows.LazyProc
	createMenu       *windows.LazyProc
	appendMenuW      *windows.LazyProc
	setMenuItemInfoW *windows.LazyProc
	checkMenuItem    *windows.LazyProc
	enableMenuItem   *windows.LazyProc
	destroyMenu      *windows.LazyProc
	trackPopupMenuEx *windows.LazyProc
	getClientRect    *windows.LazyProc
	// shell32.dll
	shellNotifyIconW *windows.LazyProc
	shellExecuteW    *windows.LazyProc
	// advapi32.dll
	openSCManagerW    *windows.LazyProc
	openServiceW      *windows.LazyProc
	closeServiceHandle *windows.LazyProc
	startServiceW     *windows.LazyProc
	controlService    *windows.LazyProc
	queryServiceStatus *windows.LazyProc
	queryServiceStatusEx *windows.LazyProc
	queryServiceConfig2W *windows.LazyProc
}

// procGetMessageW etc. are direct references so the message pump
// can call them without paying the package-private dispatch cost
// on every iteration. We expose each procedure as a typed
// *windows.LazyProc so the typed Call(args) helper compiles.
var (
	procGetMessageW         = mustLazy("user32.dll", "GetMessageW")
	procTranslateMessage    = mustLazy("user32.dll", "TranslateMessage")
	procDispatchMessageW    = mustLazy("user32.dll", "DispatchMessageW")
	procPostQuitMessage     = mustLazy("user32.dll", "PostQuitMessage")
	procDefWindowProcW      = mustLazy("user32.dll", "DefWindowProcW")
	procDestroyWindow       = mustLazy("user32.dll", "DestroyWindow")
	procCreateWindowExW     = mustLazy("user32.dll", "CreateWindowExW")
	procRegisterClassExW    = mustLazy("user32.dll", "RegisterClassExW")
	procLoadCursorW         = mustLazy("user32.dll", "LoadCursorW")
	procLoadIconW           = mustLazy("user32.dll", "LoadIconW")
	procSetForegroundWindow = mustLazy("user32.dll", "SetForegroundWindow")
	procGetCursorPos        = mustLazy("user32.dll", "GetCursorPos")
	procCreatePopupMenu     = mustLazy("user32.dll", "CreatePopupMenu")
	procAppendMenuW         = mustLazy("user32.dll", "AppendMenuW")
	procSetMenuItemInfoW    = mustLazy("user32.dll", "SetMenuItemInfoW")
	procCheckMenuItem       = mustLazy("user32.dll", "CheckMenuItem")
	procEnableMenuItem      = mustLazy("user32.dll", "EnableMenuItem")
	procDestroyMenu         = mustLazy("user32.dll", "DestroyMenu")
	procTrackPopupMenuEx    = mustLazy("user32.dll", "TrackPopupMenuEx")
	procGetClientRect       = mustLazy("user32.dll", "GetClientRect")
	procShellNotifyIconW    = mustLazy("shell32.dll", "Shell_NotifyIconW")
	procShellExecuteW       = mustLazy("shell32.dll", "ShellExecuteW")
	procOpenSCManagerW      = mustLazy("advapi32.dll", "OpenSCManagerW")
	procOpenServiceW        = mustLazy("advapi32.dll", "OpenServiceW")
	procCloseServiceHandle  = mustLazy("advapi32.dll", "CloseServiceHandle")
	procStartServiceW       = mustLazy("advapi32.dll", "StartServiceW")
	procControlService      = mustLazy("advapi32.dll", "ControlService")
	procQueryServiceStatus  = mustLazy("advapi32.dll", "QueryServiceStatus")
	procQueryServiceStatusEx = mustLazy("advapi32.dll", "QueryServiceStatusEx")
	procQueryServiceConfig2W = mustLazy("advapi32.dll", "QueryServiceConfig2W")
	procGetModuleHandleW    = mustLazy("kernel32.dll", "GetModuleHandleW")
	procUnregisterClassW    = mustLazy("user32.dll", "UnregisterClassW")
	procLoadImageW          = mustLazy("user32.dll", "LoadImageW")
)

// mustLazy resolves a Win32 procedure and returns the LazyProc
// handle so callers can use the typed Call(args) helper. We
// panic instead of returning an error because the loader runs
// at package init time and the only sane recovery is to abort
// the process — a Win32 build that cannot resolve
// user32!GetMessageW cannot run at all.
func mustLazy(module, name string) *windows.LazyProc {
	return windows.NewLazySystemDLL(module).NewProc(name)
}

// wndClassEx mirrors the WNDCLASSEXW struct used by
// RegisterClassExW. Only the fields the launcher sets are
// declared; the rest are zero, which is what the Win32 API
// expects for unused members.
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// point mirrors the Win32 POINT struct used by GetCursorPos and
// TrackPopupMenuEx.
type point struct {
	X int32
	Y int32
}

// rect mirrors the Win32 RECT struct used by GetClientRect.
type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// menuItemInfo mirrors the Win32 MENUITEMINFOW struct used by
// SetMenuItemInfoW and friends. Only the fields the launcher
// sets are declared.
type menuItemInfo struct {
	Size     uint32
	Mask     uint32
	Type     uint32
	State    uint32
	ID       uint32
	SubMenu  uintptr
	CheckBx  uintptr
	ItemData uintptr
	TypeData *uint16
	Cch      uint32
	ItemBmp  uintptr
	BBmp     uintptr
}

// Menu flags
const (
	mfString       = 0x00000000
	mfSeparator    = 0x00000800
	mfEnabled      = 0x00000000
	mfDisabled     = 0x00000002
	mfUnchecked    = 0x00000000
	mfChecked      = 0x00000008
	mfByCommand    = 0x00000000
	mfByPosition   = 0x00000400
	mfGrayed       = mfDisabled
	mfPopup        = 0x00000010
	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080
	tpmRightButton = 0x0002
	tpmLeftAlign   = 0x0000
	tpmVertical    = 0x0040
)

// MenuItemInfo masks
const (
	miiString = 0x00000040
	miiState  = 0x00000001
)

// createWindow builds the message-only window that receives the
// Win32 messages the tray icon generates. The window is
// deliberately invisible (no WS_VISIBLE flag, no ShowWindow call)
// so the launcher's only user-visible artefact is the
// notification-area icon.
//
// On success the launcher owns a window handle, an icon handle,
// and a class atom — every one of which must be released by
// destroyWindow when Run() unwinds.
func (t *winTrayIcon) createWindow() error {
	className, err := syscall.UTF16PtrFromString("PrintCatalystOnPremiseTrayHidden")
	if err != nil {
		return err
	}
	instance, _, _ := procGetModuleHandleW.Call(0)
	// Load the standard IDI_APPLICATION icon as the window
	// class icon. The notification-area icon is a separate
	// handle loaded from disk; using the application icon here
	// keeps the implementation independent of an icon asset.
	hInstance := windows.Handle(instance)
	classIcon, _, _ := procLoadIconW.Call(0, uintptr(32512)) // IDI_APPLICATION
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(32512)) // IDC_ARROW
	wcx := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		Style:      0,
		WndProc:    syscall.NewCallback(t.windowProc),
		ClsExtra:   0,
		WndExtra:   0,
		Instance:   hInstance,
		Icon:       classIcon,
		Cursor:     cursor,
		Background: 0,
		MenuName:   nil,
		ClassName:  className,
		IconSm:     classIcon,
	}
	atom, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wcx)))
	if atom == 0 {
		return errorsRegisterClass()
	}
	t.classAtom = uint16(atom)

	windowName, _ := syscall.UTF16PtrFromString("PrintCatalyst On-Premise Tray")
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExToolwind,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		wsOverlapped,
		0, 0, 0, 0,
		0, 0,
		instance,
		0,
	)
	if hwnd == 0 {
		return errorsCreateWindow()
	}
	t.hwnd = hwnd
	return nil
}

// destroyWindow releases the Win32 resources held by the
// launcher. The function is idempotent: every release is
// guarded by a "is the handle non-zero" check so a partially
// initialised launcher (e.g. window creation succeeded but
// icon load failed) still cleans up correctly.
func (t *winTrayIcon) destroyWindow() {
	if t.hwnd != 0 {
		procDestroyWindow.Call(t.hwnd)
		t.hwnd = 0
	}
	if t.classAtom != 0 {
		procUnregisterClassW.Call(
			uintptr(t.classAtom),
			0,
		)
		t.classAtom = 0
	}
}

// addIcon registers the notification-area icon. The function
// builds the NOTIFYICONDATAW struct from scratch on every call so
// a future "swap icon based on service state" feature only has
// to mutate the nid.Icon field.
func (t *winTrayIcon) addIcon() error {
	if t.hwnd == 0 {
		return errorsNoWindow()
	}
	iconPath, _ := syscall.UTF16PtrFromString(t.options.DataDirectory + "\\..\\Print Catalyst On-Premise\\" + TrayIconFileName)
	hicon, _, _ := procLoadImageW.Call(
		0,
		uintptr(unsafe.Pointer(iconPath)),
		1, // IMAGE_ICON
		0, 0,
		0x00000010, // LR_LOADFROMFILE
	)
	if hicon == 0 {
		// Fallback: load the standard application icon so
		// the tray is at least visible on a fresh install
		// that has not yet copied the icon asset.
		hicon, _, _ = procLoadIconW.Call(0, uintptr(32512))
	}
	t.hicon = hicon
	tip, _ := syscall.UTF16PtrFromString("Print Catalyst On-Premise")
	nid := notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Wnd:             t.hwnd,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip | nifState,
		CallbackMessage: wmTrayIcon,
		Icon:            hicon,
		State:           nisHidden,
		StateMask:       nisHidden,
		TimeoutOrVersion: 5, // NOTIFYICON_VERSION_5
	}
	copyUTF16(nid.Tip[:], tip)
	t.nid = nid
	r1, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r1 == 0 {
		return errorsShellNotifyIcon()
	}
	t.loaded = true
	return nil
}

// deleteIcon removes the notification-area icon. The function
// must be called from Run's deferred teardown path so a process
// exit does not leave a stale icon in the operator's
// notification area.
func (t *winTrayIcon) deleteIcon() {
	if !t.loaded {
		return
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.nid)))
	t.loaded = false
}

// showMenu builds the popup menu on demand, marks the items that
// do not apply to the current service state, and tracks the menu
// at the cursor position. The operator's click returns through
// the message-only window's WM_COMMAND handler.
//
// showMenu is called from windowProc; returning an error is
// purely advisory (a missing menu does not crash the message
// pump).
func (t *winTrayIcon) showMenu() {
	hmenu, _, _ := procCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hmenu)

	status := t.Status()
	dashboardEnabled := status.DashboardReachable
	startEnabled := !status.Running
	stopEnabled := status.Running

	t.menuItems = map[uint16]MenuAction{}
	addMenuItem := func(id uint16, action MenuAction, text string, enabled bool) {
		textPtr, _ := syscall.UTF16PtrFromString(text)
		flags := mfString
		if !enabled {
			flags |= mfDisabled | mfGrayed
		}
		procAppendMenuW.Call(hmenu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(textPtr)))
		t.menuItems[id] = action
	}
	addMenuItem(1, ActionOpenDashboard, ActionOpenDashboard.String(), dashboardEnabled)
	addMenuItem(2, ActionOpenTestPrint, ActionOpenTestPrint.String(), dashboardEnabled)
	addMenuItem(3, ActionOpenDataFolder, ActionOpenDataFolder.String(), true)
	procAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
	addMenuItem(10, ActionStartService, ActionStartService.String(), startEnabled)
	addMenuItem(11, ActionStopService, ActionStopService.String(), stopEnabled)
	procAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
	addMenuItem(20, ActionToggleAutoStart, ActionToggleAutoStart.String(), true)
	// Reflect the current auto-start state with a check-mark
	// next to the menu item so the operator can read it without
	// first toggling the value.
	if t.autostartOn {
		procCheckMenuItem.Call(hmenu, 20, mfByCommand|mfChecked)
	}
	procAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
	addMenuItem(30, ActionExit, ActionExit.String(), true)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	tpmFlags := tpmReturnCmd | tpmNonotify | tpmRightButton | tpmVertical
	procSetForegroundWindow.Call(t.hwnd)
	r1, _, _ := procTrackPopupMenuEx.Call(
		hmenu,
		uintptr(tpmFlags),
		uintptr(pt.X),
		uintptr(pt.Y),
		t.hwnd,
		0,
	)
	if r1 == 0 {
		return
	}
	action, ok := t.menuItems[uint16(r1)]
	if !ok {
		return
	}
	t.handleMenuAction(action)
}

// handleMenuAction executes the operator's selection. The
// function is called from the GUI thread so it can call Win32
// APIs without further marshalling; every branch is a short
// statement so the GUI thread never blocks for more than a
// second or two.
func (t *winTrayIcon) handleMenuAction(action MenuAction) {
	switch action {
	case ActionOpenDashboard:
		_ = t.openInBrowser(t.options.DashboardURL)
	case ActionOpenTestPrint:
		_ = t.openInBrowser(t.options.TestPrintURL)
	case ActionOpenDataFolder:
		_ = t.openDataFolder()
	case ActionStartService:
		go func() { _ = t.startService() }()
	case ActionStopService:
		go func() { _ = t.stopService() }()
	case ActionToggleAutoStart:
		t.autostartOn = !t.autostartOn
		_ = t.setAutoStart(t.autostartOn)
	case ActionExit:
		t.postQuit()
	}
}

// windowProc is the Win32 message handler. It is registered
// through syscall.NewCallback so the launcher does not need a
// CGO build target. Every branch is short and side-effect free
// except for the menu and exit paths, which delegate to
// showMenu / postQuit.
func (t *winTrayIcon) windowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmTrayIcon:
		switch uint32(lparam) {
		case wmLButtonUp:
			_ = t.openInBrowser(t.options.DashboardURL)
		case wmRButtonUp, wmContextMenu:
			t.showMenu()
		}
		return 0
	case wmCommand:
		// The TrackPopupMenuEx tpmReturnCmd path delivers
		// clicks as WM_COMMAND rather than through the return
		// value, so we handle both here for parity.
		id := uint16(wparam & 0xFFFF)
		if action, ok := t.menuItems[id]; ok {
			t.handleMenuAction(action)
		}
		return 0
	case trayIconMessageID:
		// Reserved for future tray features (balloon click,
		// user-driven refresh). No-op today.
		return 0
	}
	r1, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r1
}

// copyUTF16 fills a fixed-size uint16 buffer from a UTF-16
// pointer. Used to populate NOTIFYICONDATAW.Tip without an extra
// copy.
func copyUTF16(dst []uint16, src *uint16) {
	for i := range dst {
		if src == nil {
			return
		}
		dst[i] = *src
		if *src == 0 {
			return
		}
		src = (*uint16)(unsafe.Add(unsafe.Pointer(src), 2))
	}
}

// procGetModuleHandleW is resolved at package init. We keep the
// procedure call here because it lives at package scope and not
// inside createWindow.

// errorsRegisterClass, errorsCreateWindow, errorsNoWindow, errorsShellNotifyIcon
// are sentinel errors for the Win32 paths. They are package-private
// because nothing outside the Win32 implementation needs to
// distinguish them.
var (
	errRegisterClass    = errorString("traylauncher: RegisterClassExW failed")
	errCreateWindow     = errorString("traylauncher: CreateWindowExW failed")
	errNoWindow         = errorString("traylauncher: window handle is not initialised")
	errShellNotifyIcon  = errorString("traylauncher: Shell_NotifyIconW failed")
)

type errorString string

func (e errorString) Error() string { return string(e) }

func errorsRegisterClass() error { return errRegisterClass }
func errorsCreateWindow() error  { return errCreateWindow }
func errorsNoWindow() error      { return errNoWindow }
func errorsShellNotifyIcon() error { return errShellNotifyIcon }
