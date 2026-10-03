//go:build windows

package discover

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// enumPrinters calls EnumPrintersW with the supplied flags and
// returns every PRINTER_INFO_2 entry. The supplied name is the
// printer-name filter (use nil for "all printers"). The function
// retries with the exact buffer size reported by the first call so
// a partial buffer never escapes the caller.
func enumPrinters(flags uint32, name *uint16, level uint32) ([]printerInfo2, error) {
	if err := ctxErr(context.Background()); err != nil {
		return nil, err
	}
	var cbNeeded, cReturned uint32
	// First call — buffer size probe. EnumPrinters takes seven
	// arguments, so we use Syscall9 (which carries up to nine
	// uintptrs) and pass the unused trailing two as 0.
	//
	// IMPORTANT: errno 122 (ERROR_INSUFFICIENT_BUFFER) is the *expected*
	// result of the size-probe call. We must compare with `==`, not
	// `errors.Is`: syscall.Errno.Is only recognises a handful of
	// oserror sentinels (ErrPermission / ErrExist / ErrNotExist /
	// ErrUnsupported) and falls through to a plain equality check for
	// everything else. errors.Is(Errno(122), Errno(122)) therefore
	// returns false, and the "expected" branch was never being entered,
	// which caused every probe to surface as
	// `EnumPrintersW probe: errno=122` even on a perfectly healthy
	// spooler. That is what was driving the
	// `windows_winspool_error` synthetic record on every 5-second tick.
	r1, _, _ := syscall.Syscall9(procEnumPrintersW.Addr(), 7,
		uintptr(flags),
		uintptr(unsafe.Pointer(name)),
		uintptr(level),
		0, // pPrinterEnum: NULL → size probe
		0, // cbBuf: 0
		uintptr(unsafe.Pointer(&cbNeeded)),
		uintptr(unsafe.Pointer(&cReturned)),
		0, 0,
	)
	if r1 == 0 {
		// EnumPrinters returns FALSE with ERROR_INSUFFICIENT_BUFFER
		// (122) on the size-probe call. Anything else is typically a
		// real failure, but we check cbNeeded below before giving up.
		if errno := syscall.GetLastError(); errno == syscall.Errno(122) {
			// expected: spooler is alive and asked for a larger buffer
			fmt.Printf("DEBUG enumPrinters: size probe got expected errno 122, cbNeeded=%d\n", cbNeeded)
		} else if errno != syscall.Errno(0) {
			fmt.Printf("DEBUG enumPrinters: size probe got errno=%d, cbNeeded=%d\n", errno, cbNeeded)
			// Don't return error yet - check cbNeeded below
		} else {
			fmt.Printf("DEBUG enumPrinters: size probe r1=0 but errno=0, cbNeeded=%d\n", cbNeeded)
		}
	} else {
		fmt.Printf("DEBUG enumPrinters: size probe r1=%d (success), cbNeeded=%d\n", r1, cbNeeded)
	}
	if cbNeeded == 0 {
		// Size-probe succeeded but no printers matched the flag set
		// (or PRINTER_ENUM_DEFAULT and zero-length probe path).
		// The fetch path also reports cReturned == 0 on the probe
		// call, so we must NOT gate on cReturned here — the fetch
		// below is what actually populates it.
		fmt.Printf("DEBUG enumPrinters: cbNeeded=0, returning empty list (flags=0x%x)\n", flags)
		return nil, nil
	}
	buf := make([]byte, cbNeeded)
	r2, _, _ := syscall.Syscall9(procEnumPrintersW.Addr(), 7,
		uintptr(flags),
		uintptr(unsafe.Pointer(name)),
		uintptr(level),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(cbNeeded),
		uintptr(unsafe.Pointer(&cbNeeded)),
		uintptr(unsafe.Pointer(&cReturned)),
		0, 0,
	)
	if r2 == 0 {
		if errno := syscall.GetLastError(); errno != syscall.Errno(0) {
			return nil, fmt.Errorf("EnumPrintersW fetch: errno=%d", errno)
		}
		return nil, errors.New("EnumPrintersW failed (no error code returned)")
	}
	out := make([]printerInfo2, 0, cReturned)
	for i := uint32(0); i < cReturned; i++ {
		offset := uintptr(i) * unsafe.Sizeof(printerInfo2{})
		if offset+unsafe.Sizeof(printerInfo2{}) > uintptr(len(buf)) {
			break
		}
		info := *(*printerInfo2)(unsafe.Pointer(&buf[offset]))
		out = append(out, info)
	}
	return out, nil
}

// deviceCapabilitiesStrings calls DeviceCapabilitiesW with one of the
// DC_* codes that produces fixed-width string arrays. Used for
// DC_PAPERNAMES (64 chars per name) and DC_BINNAMES (24 chars per name).
// The result is one Go string per item; trailing NULs are stripped.
func deviceCapabilitiesStrings(_ context.Context, pDevice, pPort *uint16, capability uint16) ([]string, int, error) {
	fmt.Printf("DEBUG deviceCapabilitiesStrings: capability=%d\n", capability)
	// DeviceCapabilitiesW with pOutput == NULL returns the COUNT
	// of items (not buffer size in characters). For DC_PAPERNAMES
	// and DC_BINNAMES, each item is a fixed-width string.
	ret, _, _ := syscall.Syscall6(procDeviceCapabilitiesW.Addr(), 5,
		uintptr(unsafe.Pointer(pDevice)),
		uintptr(unsafe.Pointer(pPort)),
		uintptr(capability),
		0, // pOutput: NULL → count probe
		0, // pDevMode
		0,
	)
	count := int(int32(ret))
	fmt.Printf("DEBUG deviceCapabilitiesStrings: count probe returned %d\n", count)
	if count < 0 {
		return nil, 0, errors.New("DeviceCapabilitiesW probe returned error")
	}
	if count == 0 {
		fmt.Printf("DEBUG deviceCapabilitiesStrings: no items available (this is normal for some capabilities)\n")
		return nil, 0, nil // Not an error, just no items
	}

	// Determine the fixed width per item based on capability code
	var charsPerItem int
	switch capability {
	case 16: // DC_PAPERNAMES
		charsPerItem = 64
	case 12: // DC_BINNAMES
		charsPerItem = 24
	default:
		return nil, 0, fmt.Errorf("unsupported capability %d for deviceCapabilitiesStrings", capability)
	}

	// Allocate buffer: count items × chars per item
	bufSize := count * charsPerItem
	fmt.Printf("DEBUG deviceCapabilitiesStrings: allocating buffer size=%d (count=%d × charsPerItem=%d)\n", bufSize, count, charsPerItem)
	out := make([]uint16, bufSize)

	fmt.Printf("DEBUG deviceCapabilitiesStrings: calling DeviceCapabilitiesW fetch...\n")
	ret2, _, _ := syscall.Syscall6(procDeviceCapabilitiesW.Addr(), 5,
		uintptr(unsafe.Pointer(pDevice)),
		uintptr(unsafe.Pointer(pPort)),
		uintptr(capability),
		uintptr(unsafe.Pointer(&out[0])),
		0,
		0,
	)
	fmt.Printf("DEBUG deviceCapabilitiesStrings: fetch returned %d\n", int32(ret2))
	if int32(ret2) < 0 {
		return nil, 0, errors.New("DeviceCapabilitiesW fetch returned negative count")
	}

	fmt.Printf("DEBUG deviceCapabilitiesStrings: parsing %d items...\n", count)
	// Parse fixed-width strings
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		start := i * charsPerItem
		end := start + charsPerItem
		if end > len(out) {
			break
		}
		// Find actual string length (up to first NUL)
		actualEnd := start
		for actualEnd < end && out[actualEnd] != 0 {
			actualEnd++
		}
		if actualEnd > start {
			items = append(items, string(utf16.Decode(out[start:actualEnd])))
		}
	}
	fmt.Printf("DEBUG deviceCapabilitiesStrings: parsed %d items successfully\n", len(items))
	return items, count, nil
}

// deviceCapabilitiesInt16 calls DeviceCapabilitiesW with one of the
// DC_* codes that produces an int16 array. Used for DC_PAPERS,
// DC_DUPLEX etc.
func deviceCapabilitiesInt16(_ context.Context, pDevice, pPort *uint16, capability uint16) ([]int16, int, error) {
	fmt.Printf("DEBUG deviceCapabilitiesInt16: capability=%d\n", capability)
	ret, _, _ := syscall.Syscall6(procDeviceCapabilitiesW.Addr(), 5,
		uintptr(unsafe.Pointer(pDevice)),
		uintptr(unsafe.Pointer(pPort)),
		uintptr(capability),
		0,
		0,
		0,
	)
	count := int(int32(ret))
	fmt.Printf("DEBUG deviceCapabilitiesInt16: count probe returned %d\n", count)
	if count < 0 {
		return nil, 0, errors.New("DeviceCapabilitiesW int probe returned error")
	}
	if count == 0 {
		fmt.Printf("DEBUG deviceCapabilitiesInt16: no items available (normal for some capabilities)\n")
		return nil, 0, nil // Not an error, just no items
	}
	out := make([]int16, count)
	fmt.Printf("DEBUG deviceCapabilitiesInt16: calling DeviceCapabilitiesW fetch...\n")
	ret2, _, _ := syscall.Syscall6(procDeviceCapabilitiesW.Addr(), 5,
		uintptr(unsafe.Pointer(pDevice)),
		uintptr(unsafe.Pointer(pPort)),
		uintptr(capability),
		uintptr(unsafe.Pointer(&out[0])),
		0,
		0,
	)
	fmt.Printf("DEBUG deviceCapabilitiesInt16: fetch returned %d\n", int32(ret2))
	if int32(ret2) < 0 {
		return nil, 0, errors.New("DeviceCapabilitiesW int fetch returned negative count")
	}
	return out, count, nil
}

// ctxErr is a stub kept so the discoverer signatures stay consistent
// across platforms. Production calls do not cancel mid-tick; cancel
// is checked between ticks.
func ctxErr(ctx context.Context) error {
	return ctx.Err()
}

// utf16DecodeSlice wraps the unicode/utf16 Decode shortcut for a
// contiguous slice.
func utf16DecodeSlice(in []uint16) []uint16 {
	// utf16.Decode produces runes; for our purposes (codepoint range
	// fits in uint16) we just trim trailing whitespace so paper-size
	// labels do not carry CRLF.
	for len(in) > 0 && (in[len(in)-1] == ' ' || in[len(in)-1] == '\r' || in[len(in)-1] == '\n' || in[len(in)-1] == '\t') {
		in = in[:len(in)-1]
	}
	return in
}

// deviceCapabilityFlag reads a scalar capability, not an output array.
func deviceCapabilityFlag(device, port *uint16, capability uint16) (bool, error) {
	value, _, _ := syscall.Syscall6(procDeviceCapabilitiesW.Addr(), 5,
		uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(port)), uintptr(capability), 0, 0, 0)
	return capabilityFlagValue(int32(value))
}

func capabilityFlagValue(value int32) (bool, error) {
	if value < 0 {
		return false, errors.New("printer capability unavailable")
	}
	return value == 1, nil
}
func capabilityModes(colour, duplex bool) ([]string, []string) {
	colours := []string{"monochrome"}
	sides := []string{"one-sided"}
	if colour {
		colours = append(colours, "colour")
	}
	if duplex {
		sides = append(sides, "two-sided-long-edge", "two-sided-short-edge")
	}
	return colours, sides
}
