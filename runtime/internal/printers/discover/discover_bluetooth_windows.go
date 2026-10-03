//go:build windows

// discover_bluetooth.go — Bluetooth printer discovery for Print Catalyst
// On-Premise on Windows. The implementation uses the Windows Bluetooth API
// (BluetoothAPIs.dll / bthprops.cpl) to enumerate paired Bluetooth devices
// and check whether they advertise the Bluetooth Print Service (BPP) profile
// (UUID 0x1122). Devices that are paired but have no BPP profile are skipped
// because the spooler cannot address them without a vendor-specific RFCOMM
// channel, which is not representable as a standard queue URI.
//
// For thermal receipt printers that expose a serial-port-over-Bluetooth
// channel (Epson ESC/POS, Star TSP series, Zjiang ESC/POS), the merchant
// pairs the device in Windows Settings, then installs the vendor driver
// pointing at the assigned COM port. The driver creates a standard spooler
// queue, which the Win32 discoverer (discover_windows.go) picks up
// automatically. This Bluetooth discoverer is therefore additive — it
// surfaces devices that are paired but have not yet been given a spooler
// queue, so the dashboard can show the merchant "this Bluetooth printer is
// paired but not yet enrolled".
//
// References:
//
//	bthdef.h      — BTH_DEVICE_INFO, BTH_QUERY_SERVICE, SDP_SERVICE_UUID
//	BluetoothAPIs.h — BluetoothFindDeviceClose, BluetoothFindFirstDevice,
//	                  BluetoothFindNextDevice, BluetoothGetService,
//
// All constants are reproduced locally because the Go windows package
// does not re-export bthprops.cpl.
package discover

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// Bluetooth printer service UUID — Bluetooth Print Profile (BPP).
// Devices advertising this UUID are standard BPP printers and can be
// addressed as bluetooth://<addr>/<uuid>.
var bppServiceUUID = [16]byte{
	0x00, 0x00, 0x11, 0x22, // data1
	0x00, 0x00,             // data2
	0x10, 0x00,             // data3
	0x80, 0x00,             // clock_hi + clock_offset
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // reserved + 6-byte address placeholder
}

// bluetooth.dll procedures, resolved lazily.
var (
	modBluetooth = syscall.NewLazyDLL("BluetoothAPIs.dll")

	procBluetoothFindFirstDevice  = modBluetooth.NewProc("BluetoothFindFirstDevice")
	procBluetoothFindNextDevice   = modBluetooth.NewProc("BluetoothFindNextDevice")
	procBluetoothFindDeviceClose  = modBluetooth.NewProc("BluetoothFindDeviceClose")
	procBluetoothGetDeviceInfo    = modBluetooth.NewProc("BluetoothGetDeviceInfo")
	procBluetoothEnumerateInstalledServices = modBluetooth.NewProc("BluetoothEnumerateInstalledServicesW")
)

// BTH_ADDR is a 48-bit Bluetooth device address packed into a uint64.
type bthAddr uint64

// blueToString returns the colon-separated six-byte address string.
func (a bthAddr) String() string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
		byte(a>>40), byte(a>>32), byte(a>>24), byte(a>>16), byte(a>>8), byte(a))
}

// BLUETOOTH_DEVICE_SEARCH_FLAGS from bthdef.h.
const (
	bluetoothDeviceSearchAll    = 0xFFFFFFFF
	bluetoothDeviceSearchAuth   = 0x00000001
	bluetoothDeviceSearchRemembered = 0x00000002
	bluetoothDeviceSearchKnown  = 0x00000004
	bluetoothDeviceSearchRemote = 0x00000008
	bluetoothDeviceSearchNone   = 0x00000010
)

// BLUETOOTH_DEVICE_INFO_STRUCT size on x64. We only read the
// first few fields that are common to all versions.
const (
	sizeOfBluetoothDeviceInfo = 480
	offsetBDI_Address          = 8
	offsetBDI_ClassOfDevice    = 16
	offsetBDI_Name             = 56
)

// bluetoothDeviceRecord is the managed shape we keep in known[]
// between ticks. The raw syscall output is translated into this
// immediately so the rest of the package never touches []byte.
type bluetoothDeviceRecord struct {
	address       bthAddr
	name          string
	classOfDevice uint32
	hasBPP        bool
	remembered    bool
}

// BluetoothDiscoverer uses the Windows Bluetooth API to enumerate
// paired and remembered Bluetooth devices, filters for BPP-capable
// printers, and emits one Discovered record per qualifying device.
// It is safe for concurrent use; the constructor takes no parameters
// because all device enumeration goes through the Windows API.
type BluetoothDiscoverer struct {
	mu    sync.Mutex
	known map[string]Discovered
}

// NewBluetooth returns a Bluetooth discoverer. Returns nil when the
// Windows Bluetooth API is unavailable on this host (e.g. no adapter).
func NewBluetooth() *BluetoothDiscoverer {
	// Probe that the Bluetooth API is present by calling
	// BluetoothFindFirstDevice with a zeroed filter. This will fail
	// immediately if there is no adapter, without allocating any
	// meaningful handle.
	var handle uint32
	searchParams := uint32(bluetoothDeviceSearchNone)
	deviceBuf := newBluetoothDeviceInfo()
	r0, _, _ := procBluetoothFindFirstDevice.Call(
		uintptr(searchParams),
		0, // pGuid — nil means all devices
		uintptr(unsafe.Pointer(&deviceBuf[0])),
		uintptr(unsafe.Pointer(&handle)),
	)
	if r0 == 0 && handle == 0 {
		// No Bluetooth adapter — silently skip Bluetooth discovery.
		// This is a normal state on desktops without a Bluetooth radio.
		return nil
	}
	if r0 != 0 && handle != 0 {
		// Got a valid handle — close it immediately; we only wanted
		// to verify the API is present.
		procBluetoothFindDeviceClose.Call(uintptr(handle))
	}
	return &BluetoothDiscoverer{
		known: map[string]Discovered{},
	}
}

// Backend identifies the discoverer.
func (d *BluetoothDiscoverer) Backend() printers.Backend { return printers.BackendBluetooth }

// Watch implements Discoverer by polling the Bluetooth adapter every
// 15 seconds and emitting changes onto out. The interval is longer than
// the Win32/IPP discoverers because device enumeration on the Bluetooth
// stack is relatively expensive. The function returns when ctx is
// cancelled.
func (d *BluetoothDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	// Bluetooth adapter state can change (radio off → on, new pairing,
	// etc.) without any Windows change notification, so we poll.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
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

// newBluetoothDeviceInfo allocates a BTH_DEVICE_INFO_STRUCT with
// dwSize set to the correct version value (48 bytes on x64).
func newBluetoothDeviceInfo() []byte {
	buf := make([]byte, sizeOfBluetoothDeviceInfo)
	binary.LittleEndian.PutUint32(buf[0:4], sizeOfBluetoothDeviceInfo)
	return buf
}

// tick enumerates all remembered Bluetooth devices and emits a Discovered
// record for each BPP-capable printer.
func (d *BluetoothDiscoverer) tick(ctx context.Context, out chan<- Discovered) {
	now := d.pollNow()
	devices, err := d.enumerateDevices(ctx)
	if err != nil {
		// Bluetooth unavailable or adapter off — emit a synthetic
		// record so the dashboard can show "Bluetooth unavailable"
		// instead of silently skipping the backend.
		out <- Discovered{
			Backend:     printers.BackendBluetooth,
			QueueName:   "_bluetooth_error",
			DisplayName: "_bluetooth_error",
			Status:      printers.StatusError,
			LastSeenAt:  now,
			Error:       fmt.Errorf("bluetooth enumeration: %w", err),
		}
		return
	}

	seen := make(map[string]struct{}, len(devices))
	for _, dev := range devices {
		key := dev.address.String()
		seen[key] = struct{}{}
		queue := fmt.Sprintf("BTP-%s", dev.address.String())
		display := dev.name
		if display == "" {
			display = queue
		}
		backend := printers.BackendBluetooth
		uri := fmt.Sprintf("bluetooth://%s/00001122-0000-1000-8000-00805f9b34fb", dev.address.String())
		if !dev.hasBPP {
			// Not a BPP printer — emit with error so the dashboard
			// can surface "paired but not a BPP printer".
			disco := Discovered{
				Backend:     backend,
				QueueName:   queue,
				DisplayName: display,
				URI:         uri,
				Status:      printers.StatusError,
				LastSeenAt:  now,
				Error:       errors.New("device is paired Bluetooth but does not advertise the Bluetooth Print Service (BPP) profile"),
			}
			d.mu.Lock()
			prev, existed := d.known[key]
			d.known[key] = disco
			d.mu.Unlock()
			if !existed || !bluetoothDiscoEqual(prev, disco) {
				out <- disco
			}
			continue
		}
		disco := Discovered{
			Backend:     backend,
			QueueName:   queue,
			DisplayName: display,
			URI:         uri,
			Status:      printers.StatusReady,
			LastSeenAt:  now,
			Capabilities: &printers.Snapshot{
				ColourModes: []string{"monochrome"}, // BPP printers are thermal receipt; monochrome
				SidesModes:  []string{"one-sided"},
				PaperSizes:  []printers.PaperSize{{Key: "Roll-80mm", RawLabel: "Thermal Roll 80mm"}},
				Trays:       []printers.Tray{{Key: "tray-auto", RawLabel: "Auto Feed"}},
				Finishing:   []printers.FinishingOption{},
				Raw:         map[string]any{"bluetooth": true, "bpp": true, "classOfDevice": dev.classOfDevice},
			},
		}
		d.mu.Lock()
		prev, existed := d.known[key]
		d.known[key] = disco
		d.mu.Unlock()
		if !existed || !bluetoothDiscoEqual(prev, disco) {
			out <- disco
		}
	}

	// Emit "offline" for previously-known devices that have been
	// unpaired or are no longer remembered by Windows.
	d.mu.Lock()
	for key, prev := range d.known {
		if _, ok := seen[key]; !ok && prev.QueueName != "_bluetooth_error" {
			prev.Status = printers.StatusOffline
			prev.Error = errors.New("bluetooth device no longer remembered by Windows")
			d.known[key] = prev
			out <- prev
		}
	}
	d.mu.Unlock()
}

func (d *BluetoothDiscoverer) pollNow() time.Time { return time.Now() }

// enumerateDevices calls BluetoothFindFirstDevice / BluetoothFindNextDevice
// and returns every device that either is remembered by Windows or is a
// currently-connected RFCOMM partner.
func (d *BluetoothDiscoverer) enumerateDevices(_ context.Context) ([]bluetoothDeviceRecord, error) {
	searchFlags := uint32(bluetoothDeviceSearchRemembered | bluetoothDeviceSearchKnown | bluetoothDeviceSearchAuth)
	// zeroed BLUETOOTH_DEVICE_SEARCH_PARAMS — we use only flags.
	var searchParams uint32 = searchFlags

	deviceBuf := newBluetoothDeviceInfo()
	var handle uint32
	r0, _, err := procBluetoothFindFirstDevice.Call(
		uintptr(searchParams),
		0, // pGuid — nil means all devices
		uintptr(unsafe.Pointer(&deviceBuf[0])),
		uintptr(unsafe.Pointer(&handle)),
	)
	if r0 == 0 {
		if handle != 0 {
			procBluetoothFindDeviceClose.Call(uintptr(handle))
		}
		return nil, fmt.Errorf("BluetoothFindFirstDevice: %w", err)
	}
	defer procBluetoothFindDeviceClose.Call(uintptr(handle))

	var records []bluetoothDeviceRecord
	for {
		rec, err := d.deviceFromBuf(deviceBuf)
		if err == nil {
			// Check if the device advertises BPP.
			rec.hasBPP = d.hasBPPService(rec.address)
			records = append(records, rec)
		}
		deviceBuf = newBluetoothDeviceInfo()
		r0, _, _ = procBluetoothFindNextDevice.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&deviceBuf[0])),
		)
		if r0 == 0 {
			break
		}
	}
	return records, nil
}

// deviceFromBuf unpacks a BTH_DEVICE_INFO_STRUCT into a bluetoothDeviceRecord.
func (d *BluetoothDiscoverer) deviceFromBuf(buf []byte) (bluetoothDeviceRecord, error) {
	if len(buf) < sizeOfBluetoothDeviceInfo {
		return bluetoothDeviceRecord{}, errors.New("buffer too small for BTH_DEVICE_INFO_STRUCT")
	}
	addr := bthAddr(binary.LittleEndian.Uint64(buf[offsetBDI_Address:]))
	// Extract name: up to 248 bytes of UTF-16 from offsetBDI_Name.
	name := readUTF16String(buf[offsetBDI_Name:])
	name = strings.TrimSpace(name)
	classOfDevice := binary.LittleEndian.Uint32(buf[offsetBDI_ClassOfDevice:])
	return bluetoothDeviceRecord{
		address:       addr,
		name:          name,
		classOfDevice: classOfDevice,
	}, nil
}

// hasBPPService calls BluetoothEnumerateInstalledServices to check whether
// the device exposes the BPP service UUID. Returns false on any error
// (adapter off, radio off, device out of range) so we are conservative.
func (d *BluetoothDiscoverer) hasBPPService(addr bthAddr) bool {
	deviceBuf := newBluetoothDeviceInfo()
	binary.LittleEndian.PutUint64(deviceBuf[offsetBDI_Address:], uint64(addr))
	deviceBuf[0] = 0                          // dwSize LSB = 48 (our buffer is larger but set explicitly)
	deviceBuf[1] = 0                          // dwSize MSB continuation — already zero from make
	binary.LittleEndian.PutUint32(deviceBuf[0:4], sizeOfBluetoothDeviceInfo)

	var serviceCount uint32
	uuid := bppServiceUUID
	r0, _, _ := procBluetoothEnumerateInstalledServices.Call(
		uintptr(unsafe.Pointer(&deviceBuf[0])),
		0, // hRadio — nil means any radio
		uintptr(unsafe.Pointer(&uuid[0])),
		uintptr(unsafe.Pointer(&serviceCount)),
	)
	_ = uuid
	// serviceCount > 0 means BPP is in the device's SDP record.
	return r0 != 0 && serviceCount > 0
}

// readUTF16String reads a null-terminated UTF-16 LE string from a byte
// slice and returns it as a Go string.
func readUTF16String(buf []byte) string {
	// Find the null terminator (two zero bytes).
	end := 0
	for end+1 < len(buf) {
		if buf[end] == 0 && buf[end+1] == 0 {
			break
		}
		end += 2
	}
	if end == 0 {
		return ""
	}
	// Convert UTF-16 LE to Go string.
	pairs := make([]uint16, end/2)
	for i := range pairs {
		pairs[i] = binary.LittleEndian.Uint16(buf[i*2:])
	}
	return syscall.UTF16ToString(pairs)
}

func bluetoothDiscoEqual(a, b Discovered) bool {
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
