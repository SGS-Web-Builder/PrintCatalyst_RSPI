// Package passport implements the local Passport Photo Studio. It runs the
// composition, background replacement, head/eye guide overlay and sheet
// layout entirely on the merchant's machine. No image bytes leave the device.
//
// The phase 6 slice intentionally avoids an ML-based face detector. The
// master spec requires that any face-detection or segmentation model be
// licence-reviewed and checksummed before it is shipped; the integration
// point is the FaceDetector interface in detect.go so a future slice can
// drop in an approved model without changing the persistence or HTTP
// surface. The current implementation provides a deterministic geometric
// stub that returns the centre region of the source image scaled to the
// preset's head-height band. That stub is sufficient to exercise every
// persistence, composition, sheet layout, background colour and head/eye
// guide assertion in this slice.
package passport

import (
	"image"
	"image/color"
)

// DocumentPreset identifies a country's required photo dimensions and head
// layout. The preset is the source of truth for both the physical size of
// the rendered photo and the proportion of the photo that the applicant's
// head must occupy. The values come from publicly-available government
// guidelines; they are not guarantees of acceptance and the studio displays
// a compliance disclaimer in the UI.
type DocumentPreset string

const (
	PresetIndiaPassport     DocumentPreset = "india_passport"      // 35 x 45 mm
	PresetIndiaVisa         DocumentPreset = "india_visa"          // 35 x 45 mm (US/UK/Schengen variants reuse the same preset)
	PresetUSPassport        DocumentPreset = "us_passport"         // 2 x 2 in (50.8 x 50.8 mm)
	PresetSchengenVisa      DocumentPreset = "schengen_visa"       // 35 x 45 mm
	PresetUKPassport        DocumentPreset = "uk_passport"         // 35 x 45 mm
	PresetCanadaPassport    DocumentPreset = "canada_passport"     // 50 x 70 mm
	PresetAustraliaPassport DocumentPreset = "australia_passport"  // 35 x 45 mm
	PresetCustom            DocumentPreset = "custom"              // merchant-supplied w x h in mm
)

// PresetSpec is the canonical physical specification of one passport
// photo preset. WidthMm and HeightMm are the final print size; HeadHeightMm
// is the required head height (chin to crown) inside the frame;
// EyeLineFromBottomMm is the vertical distance from the photo's bottom
// edge to the centre of the applicant's eyes.
type PresetSpec struct {
	Preset             DocumentPreset
	DisplayName        string
	CountryCode        string
	WidthMm            float64
	HeightMm           float64
	HeadHeightMm       float64
	EyeLineFromBottomMm float64
	BackgroundColor    color.NRGBA
}

// KnownPresets returns the ordered list of presets the UI presents in the
// new-session form.
func KnownPresets() []DocumentPreset {
	return []DocumentPreset{
		PresetIndiaPassport,
		PresetIndiaVisa,
		PresetSchengenVisa,
		PresetUKPassport,
		PresetUSPassport,
		PresetCanadaPassport,
		PresetAustraliaPassport,
		PresetCustom,
	}
}

// whiteBackground is the standard passport background. It is exported through
// the PresetSpec so callers can render a preview swatch without re-creating
// the colour value.
var whiteBackground = color.NRGBA{R: 255, G: 255, B: 255, A: 255}

// PresetFor returns the canonical PresetSpec for a known preset name. Custom
// presets are populated by the merchant and stored on the session row rather
// than on this list.
func PresetFor(p DocumentPreset) (PresetSpec, error) {
	switch p {
	case PresetIndiaPassport:
		return PresetSpec{
			Preset:             p,
			DisplayName:        "India passport (35 × 45 mm)",
			CountryCode:        "IN",
			WidthMm:            35,
			HeightMm:           45,
			HeadHeightMm:       31,
			EyeLineFromBottomMm: 27,
			BackgroundColor:    whiteBackground,
		}, nil
	case PresetIndiaVisa, PresetSchengenVisa, PresetUKPassport, PresetAustraliaPassport:
		// All three are 35 x 45 mm. The country label is the only difference
		// the merchant sees in the UI.
		names := map[DocumentPreset]string{
			PresetIndiaVisa:    "India visa (35 × 45 mm)",
			PresetSchengenVisa: "Schengen visa (35 × 45 mm)",
			PresetUKPassport:   "UK passport (35 × 45 mm)",
			PresetAustraliaPassport: "Australia passport (35 × 45 mm)",
		}
		return PresetSpec{
			Preset:             p,
			DisplayName:        names[p],
			CountryCode:        "",
			WidthMm:            35,
			HeightMm:           45,
			HeadHeightMm:       31,
			EyeLineFromBottomMm: 27,
			BackgroundColor:    whiteBackground,
		}, nil
	case PresetUSPassport:
		return PresetSpec{
			Preset:             p,
			DisplayName:        "US passport (2 × 2 in)",
			CountryCode:        "US",
			WidthMm:            50.8,
			HeightMm:           50.8,
			HeadHeightMm:       31.75, // 1.25 in
			EyeLineFromBottomMm: 30.48, // ~1.20 in
			BackgroundColor:    whiteBackground,
		}, nil
	case PresetCanadaPassport:
		return PresetSpec{
			Preset:             p,
			DisplayName:        "Canada passport (50 × 70 mm)",
			CountryCode:        "CA",
			WidthMm:            50,
			HeightMm:           70,
			HeadHeightMm:       39,
			EyeLineFromBottomMm: 42,
			BackgroundColor:    whiteBackground,
		}, nil
	}
	return PresetSpec{}, ErrUnknownPreset
}

// BackgroundKind names the background colour the merchant wants applied.
// BackgroundReplaceWhite and BackgroundReplaceLightBlue are pre-defined for
// convenience; BackgroundKeepOriginal preserves the source background. A
// later slice will add a refinement brush that lets the merchant retouch the
// edge mask.
type BackgroundKind string

const (
	BackgroundKeepOriginal     BackgroundKind = "keep"
	BackgroundReplaceWhite     BackgroundKind = "white"
	BackgroundReplaceLightBlue BackgroundKind = "light_blue"
)

// BackgroundColor returns the colour a preset's background should be filled
// with after composition. The colourspace conversion is performed at render
// time so the UI can display an instant preview swatch without a round
// trip to the renderer.
func BackgroundColor(kind BackgroundKind) (color.NRGBA, error) {
	switch kind {
	case BackgroundKeepOriginal:
		return color.NRGBA{}, nil
	case BackgroundReplaceWhite:
		return whiteBackground, nil
	case BackgroundReplaceLightBlue:
		return color.NRGBA{R: 226, G: 231, B: 240, A: 255}, nil
	}
	return color.NRGBA{}, ErrInvalidBackground
}

// FaceRegion is the bounding box of the applicant's head within the source
// document. Coordinates are in source-image pixels. Confidence is 0..1; when
// the operator confirms the region manually it is rewritten with 1.0.
type FaceRegion struct {
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Confidence float64 `json:"confidence"`
	Manual     bool    `json:"manual"`
}

// Bounds returns the FaceRegion as an image.Rectangle, clamped to non-negative
// values. Negative inputs (which can occur when a draft preset is mid-edit)
// are normalised to zero so downstream code can rely on a non-empty rectangle.
func (f FaceRegion) Bounds() image.Rectangle {
	x := f.X
	y := f.Y
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	w := f.Width
	h := f.Height
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return image.Rect(x, y, x+w, y+h)
}

// Session is the durable record of one Passport Photo Studio session. The
// original customer document is referenced by ID; the operator-confirmed
// face region, chosen preset, background colour, sheet layout and the
// rendered photo are stored on the related tables.
type Session struct {
	ID             string
	OrderID        string
	DocumentID     string
	Preset         DocumentPreset
	WidthMm        float64
	HeightMm       float64
	Background     BackgroundKind
	Sheets         int
	Copies         int
	DPI            int
	HeadHeightMm   float64
	EyeLineFromBottomMm float64
	ComplianceNote string
	Status         string
	Error          string
	CreatedAt      int64
	UpdatedAt      int64
	RemovedAt      int64

	FaceRegion *FaceRegion
	Outputs    []Output
}

// Output is the durable record of one composed passport photo sheet. The
// side field is reserved for future use (e.g. a contact sheet of multiple
// photos); for now the studio always renders "single".
type Output struct {
	ID            string `json:"id"`
	SessionID     string `json:"sessionId"`
	Side          Side   `json:"side"`
	SheetWidthMm  float64 `json:"sheetWidthMm"`
	SheetHeightMm float64 `json:"sheetHeightMm"`
	PixelWidth    int    `json:"pixelWidth"`
	PixelHeight   int    `json:"pixelHeight"`
	StoragePath   string `json:"storagePath"`
	SHA256        string `json:"sha256"`
	PhotoCount    int    `json:"photoCount"`
	CreatedAt     int64   `json:"createdAt"`
}

// Side identifies which composed sheet an output row belongs to. Today the
// studio renders only "single" sheets, but the type is reserved for the
// future preview/print sheet distinction.
type Side string

const (
	SideSingle Side = "single"
)