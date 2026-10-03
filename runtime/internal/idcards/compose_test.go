package idcards

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// uniformCard produces a uniformly-coloured image filling the entire
// rectangle. Used as a source for the composition tests so we can assert
// on the exact colour at every destination pixel.
func uniformCard(w, h int, c color.NRGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestComposeIdentityPlacesCardAtDestinationRect(t *testing.T) {
	src := uniformCard(40, 25, color.NRGBA{R: 200, G: 50, B: 50, A: 255})
	dest := image.NewRGBA(image.Rect(0, 0, 200, 200))
	destRect := image.Rect(20, 20, 60, 45)
	Compose(dest, destRect, src, QuadFromRect(RectangleF{X: 0, Y: 0, Width: 40, Height: 25}))
	// Spot-check centre colour.
	r, g, b, a := dest.At(40, 32).RGBA()
	if r>>8 != 200 || g>>8 != 50 || b>>8 != 50 || a>>8 != 255 {
		t.Fatalf("dest centre = %d,%d,%d,%d", r>>8, g>>8, b>>8, a>>8)
	}
	// Pixels outside the destination rect must remain untouched (white).
	ro, _, _, _ := dest.At(0, 0).RGBA()
	if ro>>8 != 0 {
		t.Fatalf("outside pixel touched: %d", ro>>8)
	}
}

func TestComposeSkewedSourceWarpsOntoRect(t *testing.T) {
	src := uniformCard(100, 100, color.NRGBA{R: 10, G: 200, B: 10, A: 255})
	dest := image.NewRGBA(image.Rect(0, 0, 100, 60))
	// Skewed source quad maps onto a clean rectangle.
	srcQuad := Quad{
		TL: Point{10, 5},
		TR: Point{90, 8},
		BR: Point{92, 95},
		BL: Point{8, 92},
	}
	destRect := image.Rect(0, 0, 100, 60)
	Compose(dest, destRect, src, srcQuad)
	// Centre should still be the source colour (homography preserves colour).
	r, g, b, _ := dest.At(50, 30).RGBA()
	if r>>8 != 10 || g>>8 != 200 || b>>8 != 10 {
		t.Fatalf("warped centre = %d,%d,%d", r>>8, g>>8, b>>8)
	}
}

func TestRenderSheetProducesSensibleDimensions(t *testing.T) {
	front := uniformCard(40, 25, color.NRGBA{R: 100, G: 100, B: 100, A: 255})
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sheet, err := RenderSheet(layout, front,
		QuadFromRect(RectangleF{X: 0, Y: 0, Width: 40, Height: 25}),
		nil, Quad{}, 300)
	if err != nil {
		t.Fatal(err)
	}
	if sheet.Bounds().Dx() != mmToPixels(210, 300) {
		t.Fatalf("width = %d", sheet.Bounds().Dx())
	}
	if sheet.Bounds().Dy() != mmToPixels(297, 300) {
		t.Fatalf("height = %d", sheet.Bounds().Dy())
	}
}

func TestRenderSheetSideBySidePlacesTwoCards(t *testing.T) {
	front := uniformCard(40, 25, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	back := uniformCard(40, 25, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutSideBySide, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	srcQuad := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 40, Height: 25})
	sheet, err := RenderSheet(layout, front, srcQuad, back, srcQuad, 300)
	if err != nil {
		t.Fatal(err)
	}
	// Sample front area (left half) and back area (right half).
	fx := mmToPixels(layout.Placements[0].X+layout.Placements[0].Width/2, 300)
	fy := mmToPixels(layout.Placements[0].Y+layout.Placements[0].Height/2, 300)
	r, _, _, _ := sheet.At(fx, fy).RGBA()
	if r>>8 == 0 {
		t.Fatalf("front placement has no red component")
	}
	bx := mmToPixels(layout.Placements[1].X+layout.Placements[1].Width/2, 300)
	by := mmToPixels(layout.Placements[1].Y+layout.Placements[1].Height/2, 300)
	rb, _, bb, _ := sheet.At(bx, by).RGBA()
	if rb>>8 != 0 || bb>>8 == 0 {
		t.Fatalf("back placement not blue: %d,%d", rb>>8, bb>>8)
	}
}

func TestRenderSheetEncodesToPNG(t *testing.T) {
	front := uniformCard(40, 25, color.NRGBA{R: 100, G: 100, B: 100, A: 255})
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	srcQuad := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 40, Height: 25})
	sheet, err := RenderSheet(layout, front, srcQuad, nil, Quad{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := EncodePNG(&buf, sheet); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < 100 {
		t.Fatalf("PNG too small: %d", buf.Len())
	}
}

func TestMmToPixelsIsMonotonic(t *testing.T) {
	if mmToPixels(10, 300) <= mmToPixels(5, 300) {
		t.Fatal("mmToPixels must be monotonic")
	}
	if mmToPixels(10, 600) != 2*mmToPixels(10, 300) {
		t.Fatal("mmToPixels must double when DPI doubles")
	}
}

func TestComposeHandlesOutOfBoundsSamples(t *testing.T) {
	src := uniformCard(40, 25, color.NRGBA{R: 100, G: 100, B: 100, A: 255})
	dest := image.NewRGBA(image.Rect(0, 0, 50, 50))
	// Source quad with corners outside the source image should not panic.
	srcQuad := Quad{
		TL: Point{-5, -5},
		TR: Point{45, -5},
		BR: Point{45, 30},
		BL: Point{-5, 30},
	}
	destRect := image.Rect(10, 10, 50, 35)
	Compose(dest, destRect, src, srcQuad)
	// Spot-check at the centre.
	r, _, _, _ := dest.At(30, 22).RGBA()
	if r>>8 != 100 {
		t.Fatalf("out-of-bounds sample colour = %d", r>>8)
	}
}
