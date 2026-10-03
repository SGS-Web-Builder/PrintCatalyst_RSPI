//go:build windows

package discover

import (
	"testing"
	"unsafe"
)

func TestDevModeWindowsLayout(t *testing.T) {
	var dm devModeHeader
	if unsafe.Offsetof(dm.Fields) != 72 || unsafe.Offsetof(dm.Color) != 92 || unsafe.Offsetof(dm.Duplex) != 94 {
		t.Fatal("DEVMODEW fields do not match the Windows printer ABI")
	}
	dm.Size = 220
	dm.Color = 2
	dm.Duplex = 3
	got, err := readDevMode(printerInfo2{DevMode: &dm})
	if err != nil || got.Color != 2 || got.Duplex != 3 {
		t.Fatalf("read DEVMODE: %+v %v", got, err)
	}
	dm.Size = 72
	if _, err = readDevMode(printerInfo2{DevMode: &dm}); err == nil {
		t.Fatal("accepted a truncated DEVMODE")
	}
}

func TestScalarColourAndDuplexCapabilities(t *testing.T) {
	for _, value := range []int32{-1, 0, 1} {
		supported, err := capabilityFlagValue(value)
		if (err != nil) != (value < 0) || supported != (value == 1) {
			t.Fatalf("scalar %d: %v %v", value, supported, err)
		}
		colours, sides := capabilityModes(supported, supported)
		if supported && (len(colours) != 2 || len(sides) != 3) {
			t.Fatal("driver capability was lost")
		}
		if !supported && (len(colours) != 1 || len(sides) != 1) {
			t.Fatal("invented unsupported capability")
		}
	}
}
