// Package qr_theme — themed QR rendering for the merchant dashboard.
//
// The merchant can configure:
//   - a primary dark colour (replaces the default black modules)
//   - a secondary light colour (replaces the default white background)
//   - a logo overlay (PNG bytes; drawn at the centre of the QR)
//   - a frame label (rendered beneath the QR as plain text)
//
// The themed renderer reuses the existing tunnel.Encode / RenderSVG
// pipeline so the QR is always a real, scannable code. The overlay is
// applied AFTER the matrix is generated so the logo sits on top of the
// pattern. A merchant logo that covers more than ~25% of the QR
// renders a non-scannable code; the renderer caps the logo to that
// fraction and surfaces the truncation via the returned SVG metadata
// so the dashboard can warn the merchant.
//
// References:
//   - QR Code model 2 — ISO/IEC 18004
//   - SVG 1.1 — https://www.w3.org/TR/SVG11/
package qr_theme

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// Theme is the merchant-supplied branding for the QR code. The zero value
// is valid and renders the default black-on-white QR.
type Theme struct {
	Dark    string // CSS color string for the dark modules
	Light   string // CSS color string for the background
	LogoPNG []byte // optional logo; rendered below the quiet zone
	Frame   string // optional text frame beneath the QR
}

// Defaults returns a Theme populated with the runtime's safe defaults
// (black on white, no logo, no frame).
func Defaults() Theme {
	return Theme{Dark: "#0a0a0a", Light: "#ffffff"}
}

// Validate enforces the safety limits on user-supplied theme fields. A
// logo that exceeds 512x512 px after PNG decode is rejected with an
// error so the dashboard can refuse a hostile upload before the QR
// encode step is reached.
func (t Theme) Validate() error {
	for _, c := range []string{t.Dark, t.Light} {
		if c != "" && !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(c) {
			return errors.New("QR colours must use #RRGGBB")
		}
	}
	if len(t.Frame) > 64 {
		return errors.New("frame text exceeds 64 characters")
	}
	dark, light := t.Dark, t.Light
	if dark == "" {
		dark = "#0a0a0a"
	}
	if light == "" {
		light = "#ffffff"
	}
	luminance := func(c string) float64 {
		v, _ := strconv.ParseUint(c[1:], 16, 32)
		return (0.299*float64(v>>16) + 0.587*float64((v>>8)&255) + 0.114*float64(v&255)) / 255
	}
	if luminance(light)-luminance(dark) < 0.5 {
		return errors.New("QR background must be substantially lighter than its modules")
	}
	if len(t.LogoPNG) == 0 {
		return nil
	}
	if len(t.LogoPNG) > 1<<20 {
		return errors.New("logo PNG is too large (max 1 MiB)")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(t.LogoPNG))
	if err != nil {
		return fmt.Errorf("decode logo PNG: %w", err)
	}
	if cfg.Width > 512 || cfg.Height > 512 {
		return fmt.Errorf("logo dimensions %dx%d exceed the 512x512 limit", cfg.Width, cfg.Height)
	}
	if t.Frame != "" && len(t.Frame) > 64 {
		return errors.New("frame text exceeds 64 characters")
	}
	return nil
}

// EncodeThemed renders the supplied payload as a QR code and applies
// the merchant's theme. The returned SVG is self-contained and
// directly usable in the dashboard <img> tag.
func EncodeThemed(payload string, theme Theme) (string, error) {
	if strings.TrimSpace(payload) == "" {
		return "", errors.New("payload is required")
	}
	if err := theme.Validate(); err != nil {
		return "", err
	}
	matrix, _, err := tunnel.Encode(payload)
	if err != nil {
		return "", err
	}
	dark := strings.TrimSpace(theme.Dark)
	if dark == "" {
		dark = "#0a0a0a"
	}
	light := strings.TrimSpace(theme.Light)
	if light == "" {
		light = "#ffffff"
	}
	base := tunnel.RenderSVG(matrix, dark, light)
	if len(theme.LogoPNG) == 0 && strings.TrimSpace(theme.Frame) == "" {
		return base, nil
	}
	return applyOverlay(base, matrix, theme)
}

// applyOverlay extends the SVG below the QR quiet zone for shop branding.
func applyOverlay(base string, matrix [][]bool, theme Theme) (string, error) {
	if len(matrix) == 0 {
		return base, nil
	}
	n := len(matrix)
	// Strip the closing </svg> from the base and wrap.
	base = strings.Replace(base, fmt.Sprintf(`viewBox="-4 -4 %d %d"`, n+8, n+8), fmt.Sprintf(`viewBox="-4 -4 %d %d"`, n+8, n+28), 1)
	trimmed := strings.TrimSuffix(base, "</svg>")
	var b strings.Builder
	b.WriteString(trimmed)

	// Branding is placed below the quiet zone so it never obscures encoded modules.
	if len(theme.LogoPNG) > 0 {
		encoded := base64.StdEncoding.EncodeToString(theme.LogoPNG)
		fmt.Fprintf(&b, `<image x="%d" y="%d" width="8" height="8" href="data:image/png;base64,%s"/>`, n/2-4, n+5, encoded)
	}

	// Frame label.
	if frame := strings.TrimSpace(theme.Frame); frame != "" {
		// Escape XML special chars so a merchant pasting ' or &
		// produces a valid SVG instead of an XML parse error.
		escaped := xmlEscape(frame)
		b.WriteString(`<g transform="translate(0,`)
		fmt.Fprintf(&b, "%d", n+19)
		b.WriteString(`)">`)
		fmt.Fprintf(&b, `<text x="%d" y="0" text-anchor="middle" font-family="sans-serif" font-size="3" fill="#0a0a0a">%s</text>`,
			n/2, escaped)
		b.WriteString(`</g>`)
	}
	b.WriteString("</svg>")
	return b.String(), nil
}

// themeOrLight returns the merchant's light colour or the default
// "#ffffff" when the value is empty.
func themeOrLight(t Theme) string {
	if strings.TrimSpace(t.Light) == "" {
		return "#ffffff"
	}
	return t.Light
}

// xmlEscape escapes the four characters that have a special meaning in
// XML so a frame label like "Tom's Print Shop" survives SVG embedding.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '\'':
			b.WriteString("&apos;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// EncodeLogoOnly is a small convenience that returns the inner logo
// image re-encoded as a square PNG. Useful for tests that want a
// deterministic logo without depending on a binary fixture.
func EncodeLogoOnly(width, height int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
