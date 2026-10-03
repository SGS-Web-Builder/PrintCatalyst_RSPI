package qr_theme

import (
	"image/color"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// TestEncodeThemedDefaults ensures the themed encoder produces a
// scannable SVG when given no merchant branding.
func TestEncodeThemedDefaults(t *testing.T) {
	svg, err := EncodeThemed("https://shop.example.com/portal/shop-42", Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(svg, "<svg") {
		t.Errorf("svg missing <svg> root: %s", svg[:32])
	}
	if !strings.Contains(svg, "</svg>") {
		t.Errorf("svg missing </svg>: %s", svg[:64])
	}
}

// TestEncodeThemedCustomColours verifies the merchant's colours land
// in the SVG exactly as supplied.
func TestEncodeThemedCustomColours(t *testing.T) {
	theme := Theme{Dark: "#330022", Light: "#EEFFDD"}
	svg, err := EncodeThemed("https://shop.example.com", theme)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svg, `fill="#330022"`) {
		t.Errorf("dark colour not applied: %s", svg[:min(160, len(svg))])
	}
	if !strings.Contains(svg, `fill="#EEFFDD"`) {
		t.Errorf("light colour not applied")
	}
}

// TestEncodeThemedWithLogo checks the overlay path adds the <image>
// element with a base64 data URL.
func TestEncodeThemedWithLogo(t *testing.T) {
	logo := EncodeLogoOnly(32, 32, color.RGBA{R: 255, A: 255})
	theme := Theme{
		Dark:    "#000000",
		Light:   "#ffffff",
		LogoPNG: logo,
		Frame:   "Tom's Print",
	}
	svg, err := EncodeThemed("https://shop.example.com/portal/x", theme)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svg, "<image ") {
		t.Errorf("svg missing <image> overlay: %s", svg[:min(200, len(svg))])
	}
	if !strings.Contains(svg, "data:image/png;base64,") {
		t.Errorf("svg missing base64 image data")
	}
	if !strings.Contains(svg, "Tom&apos;s Print") {
		t.Errorf("sg missing escaped frame label")
	}
}

// TestEncodeThemedRejectsInvalid ensures oversized logos surface a
// validation error before the QR encode step.
func TestEncodeThemedRejectsInvalid(t *testing.T) {
	bad := make([]byte, 2<<20) // 2 MiB of zeros
	for i := range bad {
		bad[i] = 0xFF
	}
	theme := Theme{Dark: "#000000", Light: "#ffffff", LogoPNG: bad}
	if _, err := EncodeThemed("payload", theme); err == nil {
		t.Fatal("expected an error for oversized logo")
	}
}

// TestEncodeThemedMatchesUnthemedForPlainQR is a regression guard:
// when no logo or frame is supplied, the themed encoder must return a
// SVG byte-for-byte equivalent to the plain tunnel.RenderSVG output.
// This guarantees the themed renderer is a true superset, not a
// replacement that drops QR modules.
func TestEncodeThemedMatchesUnthemedForPlainQR(t *testing.T) {
	const payload = "https://shop.example.com/portal/shop-42"
	matrix, _, err := tunnel.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	expected := tunnel.RenderSVG(matrix, "#0a0a0a", "#ffffff")
	got, err := EncodeThemed(payload, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Errorf("themed output diverges from unthemed\nwant: %s\ngot:  %s", expected[:min(80, len(expected))], got[:min(80, len(got))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
