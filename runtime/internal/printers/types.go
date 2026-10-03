// Package printers defines the normalized printer capability model used by the
// local runtime. The model is intentionally backend-agnostic. On Windows
// (the only supported host) the runtime consumes two backends: the local
// Win32 print spooler (EnumPrintersW / DeviceCapabilitiesW) and any IPP or
// IPPS network endpoint the merchant adds by URL. Every preference the
// dashboard displays — paper size, tray, duplex, colour, copies, media
// type, orientation — comes from the driver / IPP attribute, never from a
// hard-coded list in this package.
package printers

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Backend identifies where the printer was discovered.
type Backend string

const (
	// BackendIPP / BackendIPPS are RFC 8011 IPP endpoints the merchant
	// adds by URL (the Win32 discoverer also surfaces them when the
	// local spooler proxies to an IPP queue). Every preference the
	// dashboard exposes for an IPP queue is read from the IPP
	// attributes the device returned.
	BackendIPP        Backend = "ipp"
	BackendIPPS       Backend = "ipps"
	// BackendWindows is the Win32 print spooler (EnumPrintersW +
	// DeviceCapabilitiesW). This is the primary discovery source on
	// the merchant's PC.
	BackendWindows    Backend = "windows"
	// BackendBluetooth is a Bluetooth device that advertises the
	// Bluetooth Print Service (BPP, UUID 0x1122). The discoverer
	// enumerates paired Bluetooth devices via the Windows Bluetooth
	// API and emits printers that have the BPP service record.
	// Thermal receipt printers that pair over Bluetooth but do not
	// advertise BPP are surfaced with StatusError so the merchant
	// knows they need manual COM-port / spooler configuration.
	BackendBluetooth  Backend = "bluetooth"
	// BackendMock is reserved for tests and explicit fixtures. It
	// never appears in production data.
	BackendMock      Backend = "mock"
)

// Status reflects what we know about the printer right now. The runtime
// updates this on every successful poll or via the OS printer-change
// notification where one is available.
type Status string

const (
	StatusUnknown Status = "unknown"
	StatusReady   Status = "ready"
	StatusOffline Status = "offline"
	StatusError   Status = "error"
)

// Printer is a discovered printer. Raw identifiers (queue, driver, URI) are
// preserved verbatim alongside the normalized fields so we can re-bind
// capabilities after an upgrade.
type Printer struct {
	ID            string    `json:"id"`
	Backend       Backend   `json:"backend"`
	QueueName     string    `json:"queueName"`
	DisplayName   string    `json:"displayName"`
	DriverName    string    `json:"driverName"`
	DriverVersion string    `json:"driverVersion"`
	URI           string    `json:"uri"`
	Location      string    `json:"location"`
	Fingerprint   string    `json:"fingerprint"`
	Status        Status    `json:"status"`
	Enabled       bool      `json:"enabled"`
	IsDefault     bool      `json:"isDefault"`
	CreatedAt     int64     `json:"createdAt"`
	LastSeenAt    int64     `json:"lastSeenAt"`
	Capabilities  *Snapshot `json:"capabilities,omitempty"`
}

// Capabilities is the normalized capability snapshot for one printer.
type Snapshot struct {
	PrinterID    string         `json:"printerId"`
	Fingerprint  string         `json:"fingerprint"`
	CapturedAt   int64          `json:"capturedAt"`
	ColourModes  []string       `json:"colourModes"`  // "monochrome", "colour"
	SidesModes   []string       `json:"sidesModes"`   // "one-sided", "two-sided-long-edge", "two-sided-short-edge"
	PaperSizes   []PaperSize    `json:"paperSizes"`
	Trays        []Tray         `json:"trays"`
	Finishing    []FinishingOption `json:"finishing"`
	Raw          map[string]any `json:"raw"` // original backend attributes, for diagnostics
}

// PaperSize is a normalized paper size for the printer.
type PaperSize struct {
	Key         string `json:"key"`         // "A4", "Letter", "Legal", "Custom.215.9x279.4", …
	RawLabel    string `json:"rawLabel"`    // driver / IPP label, preserved verbatim
	WidthMM     int    `json:"widthMm"`     // 0 when unknown
	HeightMM    int    `json:"heightMm"`    // 0 when unknown
	IsCustom    bool   `json:"isCustom"`    // true for user-defined dimensions
	MinWidthMM  int    `json:"minWidthMm"`  // for custom-size limits
	MaxWidthMM  int    `json:"maxWidthMm"`
	MinHeightMM int    `json:"minHeightMm"`
	MaxHeightMM int    `json:"maxHeightMm"`
}

// Tray is a paper source. We preserve the raw label because the driver /
// IPP attribute is the authoritative identifier — we never invent one.
type Tray struct {
	Key        string `json:"key"`        // normalized, e.g. "tray-1", "bypass", "auto"
	RawLabel   string `json:"rawLabel"`
	FeedOrientation string `json:"feedOrientation,omitempty"` // "portrait", "landscape", "auto"
	Capacity   int    `json:"capacity"` // sheets; 0 when unknown
}

// FinishingOption is one advertised finishing capability.
type FinishingOption struct {
	Type       FinishingType `json:"type"`
	Key        string        `json:"key"`
	RawLabel   string        `json:"rawLabel"`
}

// FinishingType is a small enum so we can render / validate without strings.
type FinishingType string

const (
	FinishingDuplex    FinishingType = "duplex"
	FinishingStaple    FinishingType = "staple"
	FinishingPunch     FinishingType = "punch"
	FinishingFold      FinishingType = "fold"
	FinishingBooklet   FinishingType = "booklet"
	FinishingOutputBin FinishingType = "output_bin"
)

// VerificationStatus is the lifecycle of one capability on one printer.
type VerificationStatus string

const (
	VerificationPending   VerificationStatus = "pending"
	VerificationTested    VerificationStatus = "tested"
	VerificationConfirmed VerificationStatus = "confirmed"
	VerificationVerified  VerificationStatus = "verified"
	VerificationFailed    VerificationStatus = "failed"
)

// Verification is one capability-verification row.
type Verification struct {
	ID             string             `json:"id"`
	PrinterID      string             `json:"printerId"`
	CapabilityType string             `json:"capabilityType"`
	CapabilityKey  string             `json:"capabilityKey"`
	Status         VerificationStatus `json:"status"`
	Evidence       string             `json:"evidence"`
	TestedAt       int64              `json:"testedAt"`
	VerifiedAt     int64              `json:"verifiedAt"`
	VerifiedBy     string             `json:"verifiedBy"`
	InvalidatedAt  int64              `json:"invalidatedAt"`
}

// RawAttributes is the bag of strings the backend reported. It is stored
// verbatim so the normalized snapshot can be regenerated if the rules
// evolve.
type RawAttributes map[string][]string

// Fingerprint produces a stable SHA-256 hash over the (backend, queue, driver,
// version, uri, normalized snapshot) tuple. Two printers with the same
// fingerprint are treated as the same logical device; a fingerprint change
// means the merchant must re-confirm capabilities that depend on it.
func Fingerprint(backend Backend, queue, driver, version, uri string, cap *Snapshot) string {
	h := sha256.New()
	h.Write([]byte(backend))
	h.Write([]byte{0})
	h.Write([]byte(queue))
	h.Write([]byte{0})
	h.Write([]byte(driver))
	h.Write([]byte{0})
	h.Write([]byte(version))
	h.Write([]byte{0})
	h.Write([]byte(uri))
	if cap != nil {
		// Stable, deterministic encoding of the snapshot.
		h.Write([]byte{0})
		h.Write([]byte(cap.Fingerprint))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// CapabilityFingerprint is a tighter fingerprint over the normalized
// capabilities only — used to detect when a re-discovery has changed the
// printer's reported attributes without changing its identity.
func CapabilityFingerprint(snap *Snapshot) string {
	h := sha256.New()
	colours := append([]string(nil), snap.ColourModes...)
	sides := append([]string(nil), snap.SidesModes...)
	sort.Strings(colours)
	sort.Strings(sides)
	for _, c := range colours {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	for _, s := range sides {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	papers := append([]PaperSize(nil), snap.PaperSizes...)
	sort.Slice(papers, func(i, j int) bool { return papers[i].Key < papers[j].Key })
	for _, p := range papers {
		h.Write([]byte(p.Key))
		h.Write([]byte{0})
	}
	trays := append([]Tray(nil), snap.Trays...)
	sort.Slice(trays, func(i, j int) bool { return trays[i].Key < trays[j].Key })
	for _, t := range trays {
		h.Write([]byte(t.Key))
		h.Write([]byte{0})
	}
	finishings := append([]FinishingOption(nil), snap.Finishing...)
	sort.Slice(finishings, func(i, j int) bool {
		if finishings[i].Type != finishings[j].Type {
			return finishings[i].Type < finishings[j].Type
		}
		return finishings[i].Key < finishings[j].Key
	})
	for _, f := range finishings {
		h.Write([]byte(string(f.Type)))
		h.Write([]byte{0})
		h.Write([]byte(f.Key))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// NormalizePaperKey converts a free-form paper label to a normalized key.
// "A4" → "A4"; "a4 portrait" → "A4"; "iso a4" → "A4"; "Letter (8.5x11)"
// → "Letter"; "iso_a4_210x297mm" → "A4"; etc. Unknown inputs fall back to
// the upper-cased trimmed string so we never silently drop a label.
func NormalizePaperKey(label string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(label))
	// Treat underscores the same as spaces so IPP keywords normalise
	// alongside human labels.
	cleaned = strings.ReplaceAll(cleaned, "_", " ")
	for _, prefix := range []string{"ISO ", "NA ", "OM ", "PRC ", "JIS ", "JIS X ", "ROC "} {
		cleaned = strings.TrimPrefix(cleaned, prefix)
	}
	// Strip dimension suffix in parentheses.
	if idx := strings.Index(cleaned, " ("); idx >= 0 {
		cleaned = cleaned[:idx]
	}
	// Strip trailing " <width>x<height>" dimensions, e.g. "A4 210X297MM".
	if idx := strings.Index(cleaned, " "); idx > 0 {
		rest := cleaned[idx+1:]
		if isDimensionSuffix(rest) {
			cleaned = cleaned[:idx]
		}
	}
	cleaned = strings.TrimSpace(cleaned)
	return cleaned
}

// isDimensionSuffix returns true when the input looks like a trailing
// "<width>x<height>[unit]" descriptor, e.g. "210X297MM" or "8.5X11IN".
func isDimensionSuffix(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, "X")
	if len(parts) != 2 {
		return false
	}
	if !isNumeric(parts[0]) || !isNumericPrefix(parts[1]) {
		return false
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	dot := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return true
}

func isNumericPrefix(s string) bool {
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && i > 0:
		case r >= 'A' && r <= 'Z':
			return true
		default:
			return false
		}
	}
	return true
}

// Now returns the current Unix-second timestamp. Exposed as a package-level
// variable so tests can override it.
var Now = func() int64 { return time.Now().Unix() }
