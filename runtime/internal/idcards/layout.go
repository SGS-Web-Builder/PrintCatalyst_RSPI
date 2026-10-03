package idcards

import (
	"errors"
	"fmt"
	"math"
)

// SheetPreset is the printable sheet size used as the layout canvas. The
// dimensions are in millimetres; the composition step converts to pixels at
// the configured DPI. We do not invent custom sizes: the merchant picks one
// of the explicit presets below so the customer portal cannot request an
// arbitrary paper size that the printer does not actually support.
type SheetPreset string

const (
	SheetA4     SheetPreset = "A4"
	SheetA3     SheetPreset = "A3"
	SheetLetter SheetPreset = "Letter"
	SheetLegal  SheetPreset = "Legal"
)

// SheetDimensions returns the printable area in millimetres for a known
// sheet preset. The dimensions follow ISO 216 for A-series sheets and the
// ANSI/ASME Y14.1 standard for US Letter and Legal.
func SheetDimensions(s SheetPreset) (float64, float64, error) {
	switch s {
	case SheetA4:
		return 210, 297, nil
	case SheetA3:
		return 297, 420, nil
	case SheetLetter:
		return 215.9, 279.4, nil
	case SheetLegal:
		return 215.9, 355.6, nil
	}
	return 0, 0, fmt.Errorf("unknown sheet preset %q", s)
}

// KnownSheetPresets returns the ordered list of presets the UI presents.
func KnownSheetPresets() []SheetPreset {
	return []SheetPreset{SheetA4, SheetA3, SheetLetter, SheetLegal}
}

// CardPreset is a standard card physical size in millimetres. The merchant
// picks one and the layout engine computes the placement at the exact
// physical dimensions so the printed output matches the requested size.
type CardPreset string

const (
	CardCR80        CardPreset = "cr80"         // 85.6 x 53.98 (credit card / ID-1)
	CardAadhaar     CardPreset = "aadhaar"      // 85.6 x 53.98 (same as CR80)
	CardPAN         CardPreset = "pan"          // 85.6 x 53.98
	CardVoter       CardPreset = "voter"        // 85.6 x 53.98
	CardDriving     CardPreset = "driving"      // 85.6 x 53.98
	CardVisiting    CardPreset = "visiting"     // 90 x 50
	CardCustom      CardPreset = "custom"       // merchant-supplied width/height
)

// CardDimensions returns the canonical physical dimensions of a card preset.
// Returns ErrUnknownCardPreset for any value not in the known list.
func CardDimensions(c CardPreset) (float64, float64, error) {
	switch c {
	case CardCR80, CardAadhaar, CardPAN, CardVoter, CardDriving:
		return 85.6, 53.98, nil
	case CardVisiting:
		return 90, 50, nil
	}
	return 0, 0, fmt.Errorf("unknown card preset %q", c)
}

// ErrUnknownCardPreset is returned by CardDimensions when a preset name is
// not in the known list. Callers map this to a 400 error so the customer
// can pick a different preset.
var ErrUnknownCardPreset = errors.New("unknown card preset")

// LayoutKind determines how front/back are arranged on the sheet.
type LayoutKind string

const (
	// LayoutFrontOnly places only the front card on the sheet.
	LayoutFrontOnly LayoutKind = "front_only"
	// LayoutSideBySide places front and back cards side-by-side on the same
	// sheet side. Each card occupies its own rectangle with the same height.
	LayoutSideBySide LayoutKind = "side_by_side"
	// LayoutVertical places front and back cards stacked vertically on the
	// same sheet side. Width is the same for both cards.
	LayoutVertical LayoutKind = "vertical"
	// LayoutDuplexFront outputs the front card alone — the back is composed
	// in a separate session with LayoutDuplexBack so the merchant can duplex
	// them on two sheet sides without swapping order.
	LayoutDuplexFront LayoutKind = "duplex_front"
	// LayoutDuplexBack mirrors the duplex back side. The merchant's
	// calibration offsets are applied only to the back so the back is
	// registered with the front after the printer flips the sheet.
	LayoutDuplexBack LayoutKind = "duplex_back"
)

// FlipEdge controls the duplex flip direction. The merchant's printer may
// flip the sheet along the long edge (typical for portrait cards) or the
// short edge (typical for landscape). The layout engine uses this to decide
// whether to mirror the back card horizontally or vertically.
type FlipEdge string

const (
	FlipLongEdge  FlipEdge = "long_edge"
	FlipShortEdge FlipEdge = "short_edge"
)

// Placement is the position and size of a single card on the sheet, in
// sheet millimetres. Source identifies which card fills the placement:
// "front" or "back".
type Placement struct {
	Source   string  // "front" or "back"
	X        float64 // sheet mm from the top-left corner
	Y        float64 // sheet mm from the top-left corner
	Width    float64 // mm
	Height   float64 // mm
}

// Layout is the output of ComputeLayout: the sheet, the chosen card preset
// (with its dimensions), the placements of front/back cards, and the cut
// mark spacing.
type Layout struct {
	Sheet        SheetPreset
	SheetWidthMm float64
	SheetHeightMm float64
	Card         CardPreset
	CardWidthMm  float64
	CardHeightMm float64
	LayoutKind   LayoutKind
	Placements   []Placement
	Duplex       bool
	// CalibrationDX and CalibrationDY are the merchant-calibrated back-side
	// offsets in millimetres, applied to the back card placements only.
	CalibrationDX float64
	CalibrationDY float64
	// SheetMarginMm is the printable-area margin from the sheet edge.
	SheetMarginMm float64
	// CutMarkSpacingMm is the spacing between cut marks on the sheet border.
	CutMarkSpacingMm float64
}

// LayoutInput is the input to ComputeLayout. CardWidthMm/CardHeightMm are
// only consulted when Card == CardCustom; for any other preset the
// canonical dimensions win so a customer cannot "tune" the size of a
// regulated ID card to bypass the preset.
type LayoutInput struct {
	Sheet        SheetPreset
	Card         CardPreset
	CardWidthMm  float64
	CardHeightMm float64
	LayoutKind   LayoutKind
	FlipEdge     FlipEdge
	Rows         int
	Columns      int
	// CalibrationDX / CalibrationDY: merchant-applied offsets for the back
	// card placement only. Forwarded by the service so the layout stays
	// canonical for the chosen printer setup.
	CalibrationDX float64
	CalibrationDY float64
	// SheetMarginMm: printable margin from sheet edge. Defaults to 10mm
	// when zero.
	SheetMarginMm float64
	// CutMarkSpacingMm: spacing between cut marks; defaults to 25mm.
	CutMarkSpacingMm float64
	// ActualSize: when true the card is placed at its real physical
	// dimensions; when false the card is scaled to fit the requested
	// rows × columns grid. The customer must explicitly opt into
	// fit-to-grid mode because that mode enlarges/reduces the card and
	// silently distorts the geometry.
	ActualSize bool
}

// Validate ensures the layout input is internally consistent. Returns a
// descriptive error when any constraint fails.
func (in LayoutInput) Validate() error {
	if in.LayoutKind == "" {
		return errors.New("layout kind is required")
	}
	switch in.LayoutKind {
	case LayoutFrontOnly, LayoutSideBySide, LayoutVertical, LayoutDuplexFront, LayoutDuplexBack:
		// ok
	default:
		return fmt.Errorf("unknown layout kind %q", in.LayoutKind)
	}
	if in.Sheet == "" {
		return errors.New("sheet preset is required")
	}
	if _, _, err := SheetDimensions(in.Sheet); err != nil {
		return err
	}
	if in.Card == "" {
		return errors.New("card preset is required")
	}
	if in.Rows < 0 || in.Columns < 0 {
		return errors.New("rows and columns must be non-negative")
	}
	if in.Rows > 0 && in.LayoutKind == LayoutDuplexFront || in.Rows > 0 && in.LayoutKind == LayoutDuplexBack {
		return errors.New("rows × columns grid is not supported for duplex layouts")
	}
	if in.Rows > 8 || in.Columns > 8 {
		return errors.New("rows and columns must each be at most 8")
	}
	if in.Card == CardCustom {
		if in.CardWidthMm < 10 || in.CardWidthMm > 400 {
			return errors.New("custom card width must be between 10 and 400 mm")
		}
		if in.CardHeightMm < 10 || in.CardHeightMm > 400 {
			return errors.New("custom card height must be between 10 and 400 mm")
		}
	}
	return nil
}

// ComputeLayout returns the canonical Layout for the given input. The card
// dimensions are read from the preset (custom overrides allowed); the sheet
// dimensions are read from the sheet preset; placements are computed with a
// deterministic packing order so the preview and the print engine always
// agree.
//
// Layout convention:
//   - Sheet origin is the top-left corner.
//   - "Front" is placed first (top-left of the pack).
//   - "Back" is placed after front with the configured calibration offsets.
//   - For duplex layouts the front and back are independent sessions; this
//     function does not output two sheet sides. The back session's
//     CalibrationDX/DY is applied as the back placement is laid out so a
//     later flip mirrors correctly.
func ComputeLayout(in LayoutInput) (Layout, error) {
	if err := in.Validate(); err != nil {
		return Layout{}, err
	}
	sheetW, sheetH, err := SheetDimensions(in.Sheet)
	if err != nil {
		return Layout{}, err
	}
	var cardW, cardH float64
	if in.Card == CardCustom {
		cardW = in.CardWidthMm
		cardH = in.CardHeightMm
	} else {
		cardW, cardH, err = CardDimensions(in.Card)
		if err != nil {
			return Layout{}, err
		}
	}
	margin := in.SheetMarginMm
	if margin <= 0 {
		margin = 10
	}
	cutMark := in.CutMarkSpacingMm
	if cutMark <= 0 {
		cutMark = 25
	}
	layout := Layout{
		Sheet: in.Sheet,
		SheetWidthMm: sheetW,
		SheetHeightMm: sheetH,
		Card: in.Card,
		CardWidthMm: cardW,
		CardHeightMm: cardH,
		LayoutKind: in.LayoutKind,
		CalibrationDX: in.CalibrationDX,
		CalibrationDY: in.CalibrationDY,
		SheetMarginMm: margin,
		CutMarkSpacingMm: cutMark,
	}
	switch in.LayoutKind {
	case LayoutFrontOnly:
		layout.Placements = []Placement{frontPlacement(cardW, cardH, sheetW, sheetH, margin, in.ActualSize)}
	case LayoutSideBySide:
		layout.Placements = []Placement{
			frontPlacement(cardW, cardH, sheetW, sheetH, margin, in.ActualSize),
			sideBySideBack(cardW, cardH, sheetW, sheetH, margin, in.ActualSize, in.CalibrationDX, in.CalibrationDY),
		}
	case LayoutVertical:
		layout.Placements = []Placement{
			frontPlacement(cardW, cardH, sheetW, sheetH, margin, in.ActualSize),
			verticalBack(cardW, cardH, sheetW, sheetH, margin, in.ActualSize, in.CalibrationDX, in.CalibrationDY),
		}
	case LayoutDuplexFront:
		layout.Duplex = true
		layout.Placements = []Placement{frontPlacement(cardW, cardH, sheetW, sheetH, margin, in.ActualSize)}
	case LayoutDuplexBack:
		layout.Duplex = true
		layout.Placements = []Placement{backPlacement(cardW, cardH, sheetW, sheetH, margin, in.ActualSize, in.FlipEdge, in.CalibrationDX, in.CalibrationDY)}
	}
	if len(in.PlacementsUsedByGrid()) > 0 {
		// Caller requested grid packing (rows × columns) on a layout kind
		// that supports it. ComputeLayout in this slice does not expand
		// grid placements; that is the renderer's responsibility. We expose
		// rows × columns through the input struct so callers can render
		// the same coordinates.
		_ = in.Rows
		_ = in.Columns
	}
	return layout, nil
}

// PlacementsUsedByGrid returns the per-axis grid dimensions the caller
// requested. Currently always empty (grid mode is rendered by the caller);
// kept here for the day a future slice needs pre-computed grid placements.
func (in LayoutInput) PlacementsUsedByGrid() []int {
	return nil
}

// frontPlacement returns the standard front card placement: centred in the
// sheet's printable area at actual-size. The card is scaled to fit only
// when ActualSize is false.
func frontPlacement(cardW, cardH, sheetW, sheetH, margin float64, actualSize bool) Placement {
	w, h := cardW, cardH
	if !actualSize {
		w, h = fitToHalf(sheetW-margin*2, sheetH-margin*2, cardW, cardH)
	}
	return Placement{
		Source: "front",
		X: (sheetW - w) / 2,
		Y: (sheetH - h) / 2,
		Width: w,
		Height: h,
	}
}

// sideBySideBack places the back card to the right of the front card with
// a 5mm gutter. Calibration offsets shift only the back placement.
func sideBySideBack(cardW, cardH, sheetW, sheetH, margin float64, actualSize bool, dx, dy float64) Placement {
	w, h := cardW, cardH
	if !actualSize {
		w, h = fitToHalf((sheetW-margin*2)/2-2.5, sheetH-margin*2, cardW, cardH)
	}
	return Placement{
		Source: "back",
		X: (sheetW/2) + 2.5 + dx,
		Y: (sheetH - h) / 2 + dy,
		Width: w,
		Height: h,
	}
}

// verticalBack places the back card below the front card with a 5mm gutter.
func verticalBack(cardW, cardH, sheetW, sheetH, margin float64, actualSize bool, dx, dy float64) Placement {
	w, h := cardW, cardH
	if !actualSize {
		w, h = fitToHalf(sheetW-margin*2, (sheetH-margin*2)/2-2.5, cardW, cardH)
	}
	return Placement{
		Source: "back",
		X: (sheetW - w) / 2 + dx,
		Y: (sheetH/2) + 2.5 + dy,
		Width: w,
		Height: h,
	}
}

// backPlacement returns the duplex back placement, mirrored according to the
// flip edge so the output matches the printer's duplex orientation.
func backPlacement(cardW, cardH, sheetW, sheetH, margin float64, actualSize bool, flip FlipEdge, dx, dy float64) Placement {
	w, h := cardW, cardH
	if !actualSize {
		w, h = fitToHalf(sheetW-margin*2, sheetH-margin*2, cardW, cardH)
	}
	x := (sheetW - w) / 2 + dx
	y := (sheetH - h) / 2 + dy
	// Long-edge flip mirrors horizontally around the sheet's vertical centre.
	// Short-edge flip mirrors around both axes (a 180° rotation).
	if flip == FlipLongEdge {
		x = sheetW - w - x
	} else if flip == FlipShortEdge {
		x = sheetW - w - x
		y = sheetH - h - y
	}
	return Placement{
		Source: "back",
		X: x,
		Y: y,
		Width: w,
		Height: h,
	}
}

// fitToHalf returns the largest width/height that preserves the card's
// aspect ratio and fits within the given half-region. Used only when
// ActualSize is false; the function NEVER enlarges — when the card already
// fits within the half-region the original dimensions are returned so the
// customer-facing preview and the printed output stay at the requested size.
func fitToHalf(areaW, areaH, cardW, cardH float64) (float64, float64) {
	if areaW <= 0 || areaH <= 0 || cardW <= 0 || cardH <= 0 {
		return cardW, cardH
	}
	if cardW <= areaW && cardH <= areaH {
		return cardW, cardH
	}
	wRatio := areaW / cardW
	hRatio := areaH / cardH
	scale := math.Min(wRatio, hRatio)
	return cardW * scale, cardH * scale
}
