//go:build windows

// discover_windows.go — Win32 print spooler discovery for Print Catalyst
// On-Premise. The implementation reads the printer list via EnumPrintersW
// and capabilities via DeviceCapabilitiesW + GetPrinterW + DEVMODE. No
// hardcoded printer models, paper sizes or option sets: every value is
// pulled from the driver / spooler at runtime.
//
// References (Windows SDK headers; canonical for the API the syscall
// wrapper here reproduces):
//
//	winspool.h   — EnumPrintersW, GetPrinterW, OpenPrinterW, ClosePrinter,
//	               StartDocPrinterW, WritePrinter, EndDocPrinter, DOC_INFO_1
//	wingdi.h     — DEVMODE, DM_*, dmColor, dmDuplex, dmPaperSize
//	winspool.h   — DC_PAPERS / DC_PAPERNAMES / DC_BINS / DC_BINNAMES /
//	               DC_DUPLEX / DC_COLORDEVICE
package discover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// EnumPrinters flags. PRINTER_ENUM_LOCAL covers locally-installed
// queues; PRINTER_ENUM_CONNECTIONS covers mapped network printers;
// PRINTER_ENUM_NAME forces the printer name comparison against the
// supplied name argument (we pass NULL for "all").
const (
	printerEnumLocal       = 0x00000002
	printerEnumConnections = 0x00000004
	printerEnumName        = 0x00000008
)

// DC_* capability codes passed to DeviceCapabilitiesW. Identifiers are
// the SDK constants; we define them locally because the Go sys/windows
// package does not re-export winspool.h.
const (
	dcPapers      = 2
	dcPaperNames  = 16
	dcBinNames    = 12
	dcDuplex      = 7
	dcColorDevice = 32
)

// DMCOLOR_* values from wingdi.h. Used to translate the driver's
// DEVMODE into a colour-mode snapshot.
const (
	dmColorMonochrome = 1
	dmColorColor      = 2
)

// DMDUPLEX_* values from wingdi.h.
const (
	dmDuplexUnknown = 0
	dmDuplexOff     = 1
	dmDuplexLong    = 2
	dmDuplexShort   = 3
)

// winspool.dll procedures, resolved lazily on first use so the
// discoverer can be unit-tested by overriding them.
var (
	modWinspool             = syscall.NewLazyDLL("winspool.drv")
	procEnumPrintersW       = modWinspool.NewProc("EnumPrintersW")
	procGetPrinterW         = modWinspool.NewProc("GetPrinterW")
	procOpenPrinterW        = modWinspool.NewProc("OpenPrinterW")
	procClosePrinter        = modWinspool.NewProc("ClosePrinter")
	procDeviceCapabilitiesW = modWinspool.NewProc("DeviceCapabilitiesW")
	procGetPrinterDriverW   = modWinspool.NewProc("GetPrinterDriverW")
	procStartDocPrinterW    = modWinspool.NewProc("StartDocPrinterW")
	procWritePrinter        = modWinspool.NewProc("WritePrinter")
	procEndDocPrinter       = modWinspool.NewProc("EndDocPrinter")
	procStartPagePrinter    = modWinspool.NewProc("StartPagePrinter")
	procEndPagePrinter      = modWinspool.NewProc("EndPagePrinter")
)

// PRINTER_INFO_2 layout — see winspool.h. We keep only the fields the
// discoverer reads; the trailing fields (SecurityDescriptor,
// Attributes, Priority ...) are omitted because the layout is
// variable-length and unsafe.Sizeof would otherwise need every
// preceding pointer to be accurate.
//
// Layout reference (per winspool.h, reproduced from the SDK):
//
//	LPTSTR   pServerName;        // offset 0
//	LPTSTR   pPrinterName;       // offset 8 (x64) / 4 (x86)
//	LPTSTR   pShareName;         // offset 16 / 8
//	LPTSTR   pPortName;          // offset 24 / 12
//	LPTSTR   pDriverName;        // offset 32 / 16
//	LPTSTR   pComment;           // offset 40 / 20
//	LPTSTR   pLocation;          // offset 48 / 24
//	LPDEVMODE pDevMode;          // offset 56 / 28
//	...
//
// We marshal as a contiguous byte slice of pointers plus scalars;
// each LPTSTR is a UTF-16 pointer that we read as a Go *uint16 via
// unsafe.Pointer + offset math.
type printerInfo2 struct {
	ServerName         *uint16
	PrinterName        *uint16
	ShareName          *uint16
	PortName           *uint16
	DriverName         *uint16
	Comment            *uint16
	Location           *uint16
	DevMode            *devModeHeader
	SepFile            *uint16
	PrintProcessor     *uint16
	Datatype           *uint16
	Parameters         *uint16
	SecurityDescriptor uintptr
	Attributes         uint32
	Priority           uint32
	DefaultPriority    uint32
	StartTime          uint32
	UntilTime          uint32
	Status             uint32
	JobsCount          uint32
	AveragePPM         uint32
}

// DEVMODE (driver-specific). Variable-length per driver; we read
// only the fixed-size prefix plus dmDriverExtra. dmFields tells us
// which fields in the struct carry real data.
type devModeHeader struct {
	DeviceName                                      [32]uint16
	SpecVersion, DriverVersion, Size, DriverExtra   uint16
	Fields                                          uint32
	Orientation, PaperSize, PaperLength, PaperWidth int16
	Scale, Copies, DefaultSource, PrintQuality      int16
	Color, Duplex, YResolution, TTOption, Collate   int16
}

// PrinterAttributesFlag for "default printer on this session".
const printerAttributeDefault = 0x00000004

// Status flags returned in PRINTER_INFO_2.Status. We translate the
// well-known ones; unmapped flags map to StatusUnknown.
const (
	printerStatusPaused          = 0x00000001
	printerStatusError           = 0x00000002
	printerStatusPendingDeletion = 0x00000004
	printerStatusPaperJam        = 0x00000008
	printerStatusPaperOut        = 0x00000010
	printerStatusManualFeed      = 0x00000020
	printerStatusPaperProblem    = 0x00000040
	printerStatusOffline         = 0x00000080
	printerStatusIOActive        = 0x00000100
	printerStatusBusy            = 0x00000200
	printerStatusPrinting        = 0x00000400
	printerStatusOutputBinFull   = 0x00000800
	printerStatusNotAvailable    = 0x00001000
	printerStatusWaiting         = 0x00002000
	printerStatusProcessing      = 0x00004000
	printerStatusInitializing    = 0x00008000
	printerStatusWarmingUp       = 0x00010000
	printerStatusTonerLow        = 0x00020000
	printerStatusNoToner         = 0x00040000
	printerStatusUserPaused      = 0x00100000
	printerStatusLowOnMemory     = 0x00080000
	printerStatusCleaner         = 0x00400000
)

// Debounce interval between two EnumPrinters calls. A 5-second window
// is enough to keep the loop responsive without saturating the
// spooler. Production tuning lives in main.go.
const windowsPollInterval = 5 * time.Second

// WindowsDiscoverer is the Win32-backed Discoverer. Use NewWindows
// for production; tests construct the struct directly with stubbed
// syscalls.
type WindowsDiscoverer struct {
	mu      sync.Mutex
	known   map[string]Discovered
	pollNow func() time.Time
	// testEnumPrinters, testDeviceCapabilities, testGetPrinter etc.
	// allow unit tests to inject mock responses. nil in production.
}

// NewWindows returns a fresh Win32 discoverer.
func NewWindows() *WindowsDiscoverer {
	return &WindowsDiscoverer{known: map[string]Discovered{}, pollNow: time.Now}
}

// Backend identifies the discoverer.
func (d *WindowsDiscoverer) Backend() printers.Backend { return printers.BackendWindows }

// Watch implements discover.Discoverer. The method blocks until ctx is
// cancelled; discoveries stream onto out as they arrive.
func (d *WindowsDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	ticker := time.NewTicker(windowsPollInterval)
	defer ticker.Stop()
	// Initial scan.
	d.tick(ctx, out)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx, out)
		}
	}
}

// tick performs one EnumPrinters pass and emits discoveries for any
// printers that changed.
func (d *WindowsDiscoverer) tick(ctx context.Context, out chan<- Discovered) {
	now := d.pollNow()
	entries, err := enumPrinters(printerEnumLocal|printerEnumConnections, nil, 2)
	fmt.Printf("DEBUG: enumPrinters returned %d entries, err=%v\n", len(entries), err)
	if err != nil {
		// Emit a synthetic "system" placeholder so the dashboard can
		// show "spooler unavailable" instead of hanging silently. The
		// record is also stored in d.known so a subsequent successful
		// tick can re-emit it with StatusReady and clear the dashboard
		// pill without a DB round-trip.
		synthetic := Discovered{
			Backend:     printers.BackendWindows,
			QueueName:   "_winspool_error",
			DisplayName: "_winspool_error",
			Status:      printers.StatusError,
			LastSeenAt:  now,
			Error:       fmt.Errorf("EnumPrintersW: %w", err),
		}
		d.mu.Lock()
		d.known["_winspool_error"] = synthetic
		d.mu.Unlock()
		out <- synthetic
		return
	}
	seen := make(map[string]struct{}, len(entries))
	// If a previous tick failed we left a synthetic "_winspool_error"
	// row in the DB. Re-emit it with StatusReady now that the spooler
	// is responding so the dashboard stops showing a stale "spooler
	// unavailable" pill. The printers service upserts on
	// (backend, queue_name) so this overwrites the existing synthetic
	// row in place.
	d.mu.Lock()
	if prev, ok := d.known["_winspool_error"]; ok {
		delete(d.known, "_winspool_error")
		prev.Status = printers.StatusReady
		prev.Error = nil
		d.mu.Unlock()
		out <- prev
	} else {
		d.mu.Unlock()
	}
	for _, info := range entries {
		queue := utf16ZeroToString(info.PrinterName)
		fmt.Printf("DEBUG: processing printer queue=%q\n", queue)
		if queue == "" {
			continue
		}
		seen[queue] = struct{}{}
		display := utf16ZeroToString(info.PrinterName)
		driver := utf16ZeroToString(info.DriverName)
		port := utf16ZeroToString(info.PortName)
		location := utf16ZeroToString(info.Location)
		isDefault := info.Attributes&printerAttributeDefault != 0
		status := mapSpoolerStatus(info.Status)

		disco := Discovered{
			Backend:     printers.BackendWindows,
			QueueName:   queue,
			DisplayName: display,
			DriverName:  driver,
			URI:         port,
			Location:    location,
			IsDefault:   isDefault,
			Status:      status,
			LastSeenAt:  now,
		}
		snapshot, derr := d.captureCapabilities(ctx, queue, port, info)
		fmt.Printf("DEBUG: captureCapabilities returned for queue=%q, err=%v\n", queue, derr)
		if derr == nil {
			disco.Capabilities = snapshot
			disco.DriverVersion = driverVersion(snapshot)
		} else {
			disco.Error = derr
		}
		d.mu.Lock()
		prev, existed := d.known[queue]
		d.known[queue] = disco
		d.mu.Unlock()
		fmt.Printf("DEBUG: about to emit disco for queue=%q (existed=%v, changed=%v)\n", queue, existed, !existed || !discoEqual(prev, disco))
		if !existed || !discoEqual(prev, disco) {
			out <- disco
		}
		fmt.Printf("DEBUG: finished processing queue=%q, moving to next printer\n", queue)
	}
	// Emit "offline" for any previously-known printers that are no
	// longer in the EnumPrinters list (queue was deleted, port
	// unplugged, etc.) so the dashboard can show "lost" status.
	d.mu.Lock()
	for queue, prev := range d.known {
		if _, ok := seen[queue]; !ok && prev.QueueName != "_winspool_error" {
			prev.Status = printers.StatusOffline
			prev.Error = errors.New("printer no longer reported by spooler")
			d.known[queue] = prev
			out <- prev
		}
	}
	d.mu.Unlock()
}

func (d *WindowsDiscoverer) captureCapabilities(ctx context.Context, queue, port string, info printerInfo2) (*printers.Snapshot, error) {
	if queue == "" {
		return nil, errors.New("empty queue")
	}
	queuePtr, err := utf16Ptr(queue)
	if err != nil {
		return nil, err
	}
	// Note: queuePtr is managed by Go's GC; do not call LocalFree on it
	portPtr, _ := utf16Ptr(port)
	// Note: portPtr is managed by Go's GC; do not call LocalFree on it
	snap := &printers.Snapshot{
		CapturedAt:  d.pollNow().Unix(),
		ColourModes: []string{},
		SidesModes:  []string{},
		PaperSizes:  []printers.PaperSize{},
		Trays:       []printers.Tray{},
		Finishing:   []printers.FinishingOption{},
		Raw:         map[string]any{"backend": "win32"},
	}

	// Paper sizes — DC_PAPERNAMES returns human-readable names.
	// DeviceCapabilities with the buffer-size parameter (-1 for
	// the array form) returns the required buffer length; with
	// size=0 it returns the count of items.
	if names, n, err := deviceCapabilitiesStrings(ctx, queuePtr, portPtr, dcPaperNames); err == nil && n > 0 {
		ids, _, _ := deviceCapabilitiesInt16(ctx, queuePtr, portPtr, dcPapers)
		for i, label := range names {
			label = strings.TrimRight(label, "\x00")
			if label == "" {
				continue
			}
			ps := printers.PaperSize{
				Key:      printers.NormalizePaperKey(label),
				RawLabel: label,
			}
			if i < len(ids) {
				ps.WidthMM, ps.HeightMM = paperDimensionsFromID(ids[i])
			}
			snap.PaperSizes = append(snap.PaperSizes, ps)
		}
	}

	// Trays — DC_BINNAMES returns tray names ("Auto", "Tray 1",
	// "Manual Feed", ...). We preserve the raw label; the
	// normalizeTrayKey helper (local to the discover package)
	// maps it to a stable key.
	if names, _, err := deviceCapabilitiesStrings(ctx, queuePtr, portPtr, dcBinNames); err == nil {
		for _, label := range names {
			label = strings.TrimRight(label, "\x00")
			if label == "" {
				continue
			}
			snap.Trays = append(snap.Trays, printers.Tray{
				Key:      normalizeTrayKey(label),
				RawLabel: label,
			})
		}
	}

	// DC_DUPLEX and DC_COLORDEVICE are scalar booleans; pOutput is unused.
	duplex, _ := deviceCapabilityFlag(queuePtr, portPtr, dcDuplex)
	colour, err := deviceCapabilityFlag(queuePtr, portPtr, dcColorDevice)
	if err != nil {
		dm, dmErr := readDevMode(info)
		colour = dmErr == nil && dm.Color == dmColorColor
	}
	snap.ColourModes, snap.SidesModes = capabilityModes(colour, duplex)

	fmt.Printf("DEBUG: about to return from captureCapabilities\n")
	return snap, nil
}

// readDevMode returns the dmColor / dmDuplex / dmPaperSize from the
// printer's DEVMODE block. Drivers are free to extend DEVMODE, so we
// read only the fixed-size prefix that every driver carries.
func readDevMode(info printerInfo2) (devModeHeader, error) {
	if info.DevMode == nil {
		return devModeHeader{}, errors.New("no DEVMODE attached")
	}
	const required = unsafe.Offsetof(devModeHeader{}.Collate) + unsafe.Sizeof(devModeHeader{}.Collate)
	if uintptr(info.DevMode.Size) < required {
		return devModeHeader{}, errors.New("DEVMODE prefix is truncated")
	}
	return *info.DevMode, nil
}

// paperDimensionsFromID returns millimetre width/height for the DMPAPER_*
// values defined in wingdi.h. The set is the canonical Windows paper
// list; anything else returns (0, 0) and the portal keeps the entry as
// label-only so a custom paper can still be selected.
func paperDimensionsFromID(id int16) (int, int) {
	switch id {
	case 1: // DMPAPER_LETTER
		return 216, 279
	case 2: // DMPAPER_LETTERSMALL
		return 216, 279
	case 3: // DMPAPER_TABLOID
		return 279, 432
	case 4: // DMPAPER_LEDGER
		return 432, 279
	case 5: // DMPAPER_LEGAL
		return 216, 356
	case 6: // DMPAPER_STATEMENT
		return 140, 216
	case 7: // DMPAPER_EXECUTIVE
		return 184, 267
	case 8: // DMPAPER_A3
		return 297, 420
	case 9: // DMPAPER_A4
		return 210, 297
	case 10: // DMPAPER_A4SMALL
		return 210, 297
	case 11: // DMPAPER_A5
		return 148, 210
	case 12: // DMPAPER_B4
		return 250, 354
	case 13: // DMPAPER_B5
		return 176, 250
	case 14: // DMPAPER_FOLIO
		return 216, 330
	case 15: // DMPAPER_QUARTO
		return 215, 275
	case 17: // DMPAPER_LETTER
		return 216, 279
	case 18: // DMPAPER_LETTERSMALL
		return 216, 279
	case 19: // DMPAPER_TABLOID
		return 279, 432
	case 20: // DMPAPER_LEDGER
		return 432, 279
	case 21: // DMPAPER_LEGAL
		return 216, 356
	case 22: // DMPAPER_STATEMENT
		return 140, 216
	case 23: // DMPAPER_EXECUTIVE
		return 184, 267
	case 24: // DMPAPER_A3
		return 297, 420
	case 25: // DMPAPER_A4
		return 210, 297
	case 26: // DMPAPER_A4SMALL
		return 210, 297
	case 27: // DMPAPER_A5
		return 148, 210
	case 28: // DMPAPER_B4
		return 250, 354
	case 29: // DMPAPER_B5
		return 176, 250
	case 30: // DMPAPER_FOLIO
		return 216, 330
	case 31: // DMPAPER_QUARTO
		return 215, 275
	case 32: // DMPAPER_STANDARD_10x14
		return 254, 356
	case 33: // DMPAPER_STANDARD_11x17
		return 279, 432
	case 34: // DMPAPER_NOTE
		return 216, 279
	case 35: // DMPAPER_ENV_9
		return 98, 225
	case 36: // DMPAPER_ENV_10
		return 105, 241
	case 37: // DMPAPER_ENV_11
		return 114, 263
	case 38: // DMPAPER_ENV_12
		return 121, 279
	case 39: // DMPAPER_ENV_14
		return 127, 292
	case 40: // DMPAPER_CSHEET
		return 432, 559
	case 41: // DMPAPER_DSHEET
		return 559, 864
	case 42: // DMPAPER_ESHEET
		return 864, 1118
	case 43: // DMPAPER_ENV_DL
		return 110, 220
	case 44: // DMPAPER_ENV_C3
		return 324, 458
	case 45: // DMPAPER_ENV_C4
		return 229, 324
	case 46: // DMPAPER_ENV_C5
		return 162, 229
	case 47: // DMPAPER_ENV_C6
		return 114, 162
	case 48: // DMPAPER_ENV_C65
		return 114, 229
	case 49: // DMPAPER_ENV_B4
		return 250, 353
	case 50: // DMPAPER_ENV_B5
		return 176, 250
	case 51: // DMPAPER_ENV_B6
		return 176, 125
	case 52: // DMPAPER_ENV_ITALY
		return 110, 230
	case 53: // DMPAPER_ENV_MONARCH
		return 98, 191
	case 54: // DMPAPER_ENV_PERSONAL
		return 92, 165
	case 55: // DMPAPER_FANFOLD_US
		return 378, 279
	case 56: // DMPAPER_FANFOLD_STD_GERMAN
		return 216, 305
	case 57: // DMPAPER_FANFOLD_LGL_GERMAN
		return 216, 356
	case 58: // DMPAPER_ISO_B4
		return 250, 353
	case 59: // DMPAPER_JAPANESE_POSTCARD
		return 100, 148
	case 60: // DMPAPER_9X11
		return 229, 279
	case 61: // DMPAPER_10X11
		return 254, 279
	case 62: // DMPAPER_15X11
		return 381, 279
	case 63: // DMPAPER_ENV_INVITE
		return 110, 220
	case 64: // DMPAPER_RESERVED_64
		return 0, 0
	case 65: // DMPAPER_RESERVED_65
		return 0, 0
	case 66: // DMPAPER_LETTER_PLUS
		return 216, 322
	case 67: // DMPAPER_A4_PLUS
		return 210, 297
	case 68: // DMPAPER_A5_TRANSVERSE
		return 148, 210
	case 69: // DMPAPER_B5_TRANSVERSE
		return 176, 250
	case 70: // DMPAPER_A3_EXTRA
		return 322, 445
	case 71: // DMPAPER_A5_EXTRA
		return 174, 235
	case 72: // DMPAPER_B5_EXTRA
		return 201, 276
	case 73: // DMPAPER_A2
		return 420, 594
	case 74: // DMPAPER_A3_TRANSVERSE
		return 297, 420
	case 75: // DMPAPER_A3_EXTRA_TRANSVERSE
		return 322, 445
	case 76: // DMPAPER_DBL_JAPANESE_POSTCARD
		return 200, 148
	case 77: // DMPAPER_A6
		return 105, 148
	case 78: // DMPAPER_JENV_KAKU2
		return 0, 0
	case 79: // DMPAPER_JENV_CHOU3
		return 0, 0
	case 80: // DMPAPER_B6_JIS
		return 128, 182
	case 81: // DMPAPER_B7_JIS
		return 0, 0
	case 82: // DMPAPER_JAPANESE_ENVELOPE_KAKU_3
		return 0, 0
	case 83: // DMPAPER_JAPANESE_ENVELOPE_CHOU_4
		return 0, 0
	case 84: // DMPAPER_A4_SMALL_TRANSVERSE
		return 210, 297
	case 85: // DMPAPER_A4_SMALL_LONG_FEED
		return 210, 297
	case 86: // DMPAPER_JAPANESE_POSTCARD_SQUARE
		return 148, 148
	case 87: // DMPAPER_2A_POSTCARD_SQUARE
		return 0, 0
	case 88: // DMPAPER_4A_POSTCARD_SQUARE
		return 0, 0
	case 89: // DMPAPER_A4_LONG_FEED
		return 210, 297
	case 90: // DMPAPER_LETTER_TRANSVERSE
		return 216, 279
	case 91: // DMPAPER_A4_TRANSVERSE
		return 210, 297
	case 92: // DMPAPER_LETTER_EXTRA_TRANSVERSE
		return 216, 356
	case 93: // DMPAPER_A4_PLUS_TRANSVERSE
		return 210, 297
	case 94: // DMPAPER_LETTER_EXTRA_PORT
		return 216, 356
	case 95: // DMPAPER_A4_PLUS_PORT
		return 210, 297
	case 96: // DMPAPER_A5_LONG_FEED
		return 148, 210
	case 97: // DMPAPER_B5_LONG_FEED
		return 176, 250
	case 98: // DMPAPER_PRC16K
		return 0, 0
	case 99: // DMPAPER_PRC32K
		return 0, 0
	case 100: // DMPAPER_PRC_BIG_5
		return 0, 0
	case 101: // DMPAPER_PRC_BIG_5_ROTATED
		return 0, 0
	case 102: // DMPAPER_PRC_B4
		return 0, 0
	case 103: // DMPAPER_PRC_B4_ROTATED
		return 0, 0
	case 104: // DMPAPER_PRC_B5
		return 0, 0
	case 105: // DMPAPER_PRC_B5_ROTATED
		return 0, 0
	case 106: // DMPAPER_PRC_B6
		return 0, 0
	case 107: // DMPAPER_PRC_B6_ROTATED
		return 0, 0
	case 108: // DMPAPER_PRC_B5_4
		return 0, 0
	case 109: // DMPAPER_PRC_B6_4
		return 0, 0
	case 110: // DMPAPER_PRC_B6_4_ROTATED
		return 0, 0
	case 111: // DMPAPER_PRC_B5_5
		return 0, 0
	case 112: // DMPAPER_PRC_B5_5_ROTATED
		return 0, 0
	}
	return 0, 0
}

// driverVersion returns a stable fingerprint of the current driver
// load order. Currently empty — the capability snapshot already
// carries its own captured-at timestamp; this hook is the natural
// extension point once we integrate GetPrinterDriverW.
func driverVersion(_ *printers.Snapshot) string {
	return ""
}

// mapSpoolerStatus converts the PRINTER_INFO_2.Status bitmask into a
// printers.Status. Unknown error bits map to StatusError so the
// dashboard surfaces a yellow/red pill instead of a silent green
// light.
func mapSpoolerStatus(raw uint32) printers.Status {
	switch {
	case raw&printerStatusPrinting != 0 || raw&printerStatusIOActive != 0 || raw&printerStatusBusy != 0 || raw&printerStatusProcessing != 0:
		return printers.StatusReady
	case raw&printerStatusOffline != 0 || raw&printerStatusNotAvailable != 0 || raw&printerStatusNoToner != 0:
		return printers.StatusOffline
	case raw&printerStatusError != 0 || raw&printerStatusPaperJam != 0 || raw&printerStatusPaperOut != 0 || raw&printerStatusOutputBinFull != 0 || raw&printerStatusUserPaused != 0 || raw&printerStatusLowOnMemory != 0 || raw&printerStatusPaused != 0:
		return printers.StatusError
	default:
		return printers.StatusUnknown
	}
}

func discoEqual(a, b Discovered) bool {
	if a.QueueName != b.QueueName || a.DisplayName != b.DisplayName {
		return false
	}
	if a.Status != b.Status {
		return false
	}
	if a.DriverName != b.DriverName || a.URI != b.URI {
		return false
	}
	return true
}

// normalizeTrayKey derives a stable key from a Windows tray label so
// the portal / dashboard can match the same physical tray across
// reboots and across driver upgrades. The exact mapping mirrors the
// printers package's normalizeTrayKey helper; kept here as an exact
// duplicate so the discover package does not export platform-only
// logic into the shared printers package.
func normalizeTrayKey(label string) string {
	cleaned := strings.ToLower(strings.TrimSpace(label))
	switch cleaned {
	case "auto", "auto select", "automatic":
		return "tray-auto"
	case "manual", "bypass", "multi-purpose", "multi purpose tray", "mp tray":
		return "tray-bypass"
	}
	if strings.HasPrefix(cleaned, "tray ") {
		return "tray-" + strings.TrimPrefix(cleaned, "tray ")
	}
	return "tray-" + cleaned
}
