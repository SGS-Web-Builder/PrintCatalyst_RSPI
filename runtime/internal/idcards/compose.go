package idcards

import (
	"errors"
	"image"
	"image/color"
	"math"
)

// Compose renders a perspective-corrected source image into a destination
// rectangle on the sheet. The four source corners map to the four
// destination corners via the computed homography; bilinear sampling is
// used so sub-pixel destination positions produce smooth output.
//
// The destination image must already exist; this function only writes into
// the rectangle defined by `destRect` (in pixel coordinates). Pixels outside
// that rectangle are never touched so multiple Compose calls can paint
// non-overlapping placements onto the same sheet.
//
// Returned is the perspective-corrected card image (cropped to the bounding
// rectangle of the placement) so callers can produce standalone previews
// alongside the composed sheet.
func Compose(dest *image.RGBA, destRect image.Rectangle, src image.Image, srcQuad Quad) image.Image {
	destQuad := QuadFromRect(RectangleF{
		X:      float64(destRect.Min.X),
		Y:      float64(destRect.Min.Y),
		Width:  float64(destRect.Dx()),
		Height: float64(destRect.Dy()),
	})
	// Compute the homography that maps destination pixels back to source
	// pixels so we can inverse-warp (avoids holes and resolves the
	// destination→source direction in a single pass).
	h, err := ComputeHomography(destQuad, srcQuad)
	if err != nil {
		// Fallback: paste the source rectangle without perspective correction.
		return pasteFallback(dest, destRect, src, srcQuad)
	}
	srcBounds := src.Bounds()
	dstW := destRect.Dx()
	dstH := destRect.Dy()
	cropped := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			// Map destination pixel back to source via the dest→src homography.
			srcPt := ApplyHomography(h, Point{X: float64(x + destRect.Min.X), Y: float64(y + destRect.Min.Y)})
			sampled := bilinearSample(src, srcBounds, srcPt)
			cropped.Set(x, y, sampled)
			dest.Set(x+destRect.Min.X, y+destRect.Min.Y, sampled)
		}
	}
	return cropped
}

// pasteFallback is used when the source quad is degenerate (collinear
// corners or near-zero area). It pastes the source image's bounding box
// directly into the destination rectangle without any perspective
// correction so the operator still sees the card they uploaded, even if it
// is slightly mis-rotated.
func pasteFallback(dest *image.RGBA, destRect image.Rectangle, src image.Image, srcQuad Quad) image.Image {
	pts := srcQuad.Points()
	minX := math.MaxFloat64
	maxX := -math.MaxFloat64
	minY := math.MaxFloat64
	maxY := -math.MaxFloat64
	for _, p := range pts {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	srcBounds := src.Bounds()
	srcRect := image.Rect(
		int(math.Max(minX, float64(srcBounds.Min.X))),
		int(math.Max(minY, float64(srcBounds.Min.Y))),
		int(math.Min(maxX, float64(srcBounds.Max.X))),
		int(math.Min(maxY, float64(srcBounds.Max.Y))),
	)
	if srcRect.Empty() {
		return image.NewRGBA(destRect)
	}
	dstW := destRect.Dx()
	dstH := destRect.Dy()
	cropped := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx := srcRect.Min.X + x*srcRect.Dx()/dstW
			sy := srcRect.Min.Y + y*srcRect.Dy()/dstH
			if sx >= srcBounds.Max.X {
				sx = srcBounds.Max.X - 1
			}
			if sy >= srcBounds.Max.Y {
				sy = srcBounds.Max.Y - 1
			}
			c := src.At(sx, sy)
			cropped.Set(x, y, c)
			dest.Set(x+destRect.Min.X, y+destRect.Min.Y, c)
		}
	}
	return cropped
}

// bilinearSample reads the source image at the given (possibly fractional)
// coordinates using bilinear interpolation. Coordinates outside the source
// bounds are clamped to the nearest edge pixel — perspective corners can
// legitimately request slightly-outside samples when the destination quad
// sits at the sheet border.
func bilinearSample(src image.Image, bounds image.Rectangle, p Point) color.NRGBA {
	x0 := int(math.Floor(p.X))
	y0 := int(math.Floor(p.Y))
	x1 := x0 + 1
	y1 := y0 + 1
	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x1 >= bounds.Max.X {
		x1 = bounds.Max.X - 1
	}
	if y1 >= bounds.Max.Y {
		y1 = bounds.Max.Y - 1
	}
	fx := p.X - math.Floor(p.X)
	fy := p.Y - math.Floor(p.Y)
	if fx < 0 {
		fx = 0
	}
	if fy < 0 {
		fy = 0
	}
	c00 := samplePixel(src, x0, y0)
	c10 := samplePixel(src, x1, y0)
	c01 := samplePixel(src, x0, y1)
	c11 := samplePixel(src, x1, y1)
	top := lerpColor(c00, c10, fx)
	bottom := lerpColor(c01, c11, fx)
	return lerpColor(top, bottom, fy)
}

func samplePixel(src image.Image, x, y int) color.NRGBA {
	r, g, b, a := src.At(x, y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func lerpColor(a, b color.NRGBA, t float64) color.NRGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return color.NRGBA{
		R: uint8(math.Round(float64(a.R)*(1-t) + float64(b.R)*t)),
		G: uint8(math.Round(float64(a.G)*(1-t) + float64(b.G)*t)),
		B: uint8(math.Round(float64(a.B)*(1-t) + float64(b.B)*t)),
		A: uint8(math.Round(float64(a.A)*(1-t) + float64(b.A)*t)),
	}
}

// RenderSheet composes the front and (optionally) back images onto a sheet
// at the given DPI. The output is a stable, predictable image whose pixel
// dimensions are (sheetMm × DPI / 25.4) wide by high.
//
// The DPI argument is the resolution of the OUTPUT sheet (not the source
// scans). 300 DPI matches what the test fixtures use and what most
// double-sided ID card printers expect.
func RenderSheet(layout Layout, front image.Image, frontQuad Quad, back image.Image, backQuad Quad, dpi float64) (*image.RGBA, error) {
	if dpi < 72 || dpi > 1200 {
		return nil, errors.New("dpi must be between 72 and 1200")
	}
	pxW := mmToPixels(layout.SheetWidthMm, dpi)
	pxH := mmToPixels(layout.SheetHeightMm, dpi)
	sheet := image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	// White background.
	for y := 0; y < pxH; y++ {
		for x := 0; x < pxW; x++ {
			sheet.Set(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	drawCutMarks(sheet, dpi, layout.CutMarkSpacingMm, pxW, pxH)
	for _, p := range layout.Placements {
		destRect := image.Rect(
			mmToPixels(p.X, dpi),
			mmToPixels(p.Y, dpi),
			mmToPixels(p.X+p.Width, dpi),
			mmToPixels(p.Y+p.Height, dpi),
		)
		switch p.Source {
		case "front":
			if front != nil {
				Compose(sheet, destRect, front, frontQuad)
			}
		case "back":
			if back != nil {
				Compose(sheet, destRect, back, backQuad)
			}
		}
	}
	return sheet, nil
}

// mmToPixels converts a measurement in millimetres to a pixel count at the
// given DPI (1 inch = 25.4 mm). The result is rounded so the output
// dimensions are stable across hardware platforms.
func mmToPixels(mm float64, dpi float64) int {
	return int(math.Round(mm * dpi / 25.4))
}

// drawCutMarks paints short tick marks around the sheet border at the given
// spacing. The marks help the operator align the printed sheet with a
// trimmer.
func drawCutMarks(sheet *image.RGBA, dpi float64, spacingMm float64, pxW, pxH int) {
	if spacingMm <= 0 {
		return
	}
	colour := color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	markLen := mmToPixels(3, dpi)
	if markLen < 2 {
		markLen = 2
	}
	spacingPx := mmToPixels(spacingMm, dpi)
	if spacingPx < 8 {
		spacingPx = 8
	}
	for x := 0; x < pxW; x += spacingPx {
		for dy := 0; dy < markLen; dy++ {
			sheet.Set(x, dy, colour)
			sheet.Set(x, pxH-1-dy, colour)
		}
	}
	for y := 0; y < pxH; y += spacingPx {
		for dx := 0; dx < markLen; dx++ {
			sheet.Set(dx, y, colour)
			sheet.Set(pxW-1-dx, y, colour)
		}
	}
}
