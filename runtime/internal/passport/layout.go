package passport

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
)

// SheetPreset names a printable paper size for the photo sheet. The studio
// uses A4 as the default; the merchant can switch to A3 or Letter when
// running a batch job.
type SheetPreset string

const (
	SheetA4     SheetPreset = "A4"
	SheetA3     SheetPreset = "A3"
	SheetLetter SheetPreset = "Letter"
	Sheet4x6     SheetPreset = "4x6"
)

// SheetDimensions returns the printable width and height in millimetres.
// Values follow ISO 216 for A-series, ANSI/ASME Y14.1 for US Letter and the
// 4 x 6 in photo paper convention.
func SheetDimensions(s SheetPreset) (float64, float64, error) {
	switch s {
	case SheetA4:
		return 210, 297, nil
	case SheetA3:
		return 297, 420, nil
	case SheetLetter:
		return 215.9, 279.4, nil
	case Sheet4x6:
		return 101.6, 152.4, nil
	}
	return 0, 0, fmt.Errorf("unknown sheet preset %q", s)
}

// KnownSheetPresets is the ordered list of presets the UI presents.
func KnownSheetPresets() []SheetPreset {
	return []SheetPreset{SheetA4, SheetA3, SheetLetter, Sheet4x6}
}

// SheetLayout plans the placement of passport photos on a sheet. The
// planning step is pure: it returns the per-photo pixel rectangles and the
// photo count without writing any output. The composer in compose.go uses
// this layout to crop and place the source image at each rectangle.
//
// CutMarginMm is the bleed reserved around each photo so a pair of scissors
// or a paper trimmer does not cut into the applicant's hairline. The
// default is 3 mm which matches typical desktop photo printers.
type SheetLayout struct {
	Sheet       SheetPreset
	SheetWMm    float64
	SheetHMm    float64
	PhotoWMm    float64
	PhotoHMm    float64
	CutMarginMm float64
	DPI         int
	Rows        int
	Cols        int
	PhotoCount  int
}

// PlanSheet computes how many photos of the given size fit on the chosen
// sheet, leaving a uniform cut margin between them. The result is the
// canonical row × column placement so the rendered sheet matches the
// merchant's printer capability (no off-by-one padding that pushes a photo
// off the sheet).
func PlanSheet(sheet SheetPreset, photoWMm, photoHMm float64, dpi int) (SheetLayout, error) {
	if photoWMm <= 0 || photoHMm <= 0 {
		return SheetLayout{}, ErrInvalid
	}
	if dpi <= 0 {
		dpi = 300
	}
	w, h, err := SheetDimensions(sheet)
	if err != nil {
		return SheetLayout{}, err
	}
	cut := 3.0
	usableW := w - 2*cut
	usableH := h - 2*cut
	cols := int(usableW / (photoWMm + cut))
	rows := int(usableH / (photoHMm + cut))
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return SheetLayout{
		Sheet:       sheet,
		SheetWMm:    w,
		SheetHMm:    h,
		PhotoWMm:    photoWMm,
		PhotoHMm:    photoHMm,
		CutMarginMm: cut,
		DPI:         dpi,
		Rows:        rows,
		Cols:        cols,
		PhotoCount:  rows * cols,
	}, nil
}

// PixelSize converts the sheet's physical dimensions to the pixel grid at
// the layout's DPI. The composer uses this to allocate the destination
// image.
func (l SheetLayout) PixelSize() (int, int) {
	pxPerMm := float64(l.DPI) / 25.4
	w := int(float64(l.SheetWMm) * pxPerMm)
	h := int(float64(l.SheetHMm) * pxPerMm)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// PhotoRect returns the pixel rectangle for the n-th photo on the sheet.
// Index 0 is the top-left corner; the rectangles march left-to-right,
// top-to-bottom. The cut margin is preserved as a uniform gap between
// photos.
func (l SheetLayout) PhotoRect(index int) image.Rectangle {
	if index < 0 || index >= l.PhotoCount {
		return image.Rectangle{}
	}
	row := index / l.Cols
	col := index % l.Cols
	pxPerMm := float64(l.DPI) / 25.4
	x0 := (l.CutMarginMm + float64(col)*(l.PhotoWMm+l.CutMarginMm)) * pxPerMm
	y0 := (l.CutMarginMm + float64(row)*(l.PhotoHMm+l.CutMarginMm)) * pxPerMm
	x1 := x0 + l.PhotoWMm*pxPerMm
	y1 := y0 + l.PhotoHMm*pxPerMm
	return image.Rect(int(x0), int(y0), int(x1), int(y1))
}

// FillSolid paints the destination image with the supplied background colour.
// The composer calls this before placing the photos so the cut-margins and
// any unused sheet area show the chosen background.
func FillSolid(dst *image.RGBA, c color.NRGBA) {
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// CopyPhoto draws the source photo at the destination rectangle. The source
// is expected to be the perspective-corrected face crop at the destination's
// physical aspect ratio. The draw call uses nearest-neighbour sampling so
// the printed pixels are exactly the source pixels (no smoothing that could
// hide a mis-aligned face).
func CopyPhoto(dst *image.RGBA, rect image.Rectangle, src image.Image) {
	draw.Draw(dst, rect, src, src.Bounds().Min, draw.Src)
}