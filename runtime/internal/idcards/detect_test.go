package idcards

import (
	"image"
	"image/color"
	"testing"
)

// makeCardFixture synthesises a synthetic ID-card photo: dark background with
// a bright rectangle at known coordinates. The contrast is high so the
// detection pipeline should always succeed.
func makeCardFixture(t *testing.T, w, h int, cardTL, cardTR, cardBR, cardBL Point) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Dark grey background.
	bg := color.RGBA{R: 30, G: 30, B: 30, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, bg)
		}
	}
	// Fill the card polygon with a contrasting colour using a quick barycentric
	// inclusion test on the quad.
	bounds := image.Rect(0, 0, w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if pointInQuad(Point{X: float64(x), Y: float64(y)}, Quad{TL: cardTL, TR: cardTR, BR: cardBR, BL: cardBL}) {
				if x >= bounds.Min.X && x < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
					img.Set(x, y, color.RGBA{R: 230, G: 230, B: 240, A: 255})
				}
			}
		}
	}
	// Strong black border for the edge detector.
	border := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	drawLine(img, cardTL, cardTR, border)
	drawLine(img, cardTR, cardBR, border)
	drawLine(img, cardBR, cardBL, border)
	drawLine(img, cardBL, cardTL, border)
	return img
}

func pointInQuad(p Point, q Quad) bool {
	// Use the cross-product sign test: p is inside when it is on the same
	// side of every edge as the rest of the polygon. For convex quads this
	// is sufficient.
	pts := q.Points()
	for i := 0; i < 4; i++ {
		a := pts[i]
		b := pts[(i+1)%4]
		cross := (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
		if cross < 0 {
			return false
		}
	}
	return true
}

func TestDetectQuadrilateralRecoversCardFixture(t *testing.T) {
	img := makeCardFixture(t, 400, 300,
		Point{80, 60}, Point{320, 70}, Point{325, 240}, Point{75, 235})
	result, err := DetectQuadrilateral(img)
	if err != nil {
		t.Fatal(err)
	}
	if result.Confidence < MinConfidence {
		t.Fatalf("confidence = %f below %f", result.Confidence, MinConfidence)
	}
	want := []Point{{80, 60}, {320, 70}, {325, 240}, {75, 235}}
	got := result.Quad.Points()
	for i := range want {
		if dist(got[i], want[i]) > 25 {
			t.Fatalf("corner %d = %+v, want ~%+v", i, got[i], want[i])
		}
	}
}

func TestDetectQuadrilateralRefusesBlankImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	if _, err := DetectQuadrilateral(img); err == nil {
		t.Fatal("expected blank image to fail detection")
	}
}

func TestDetectQuadrilateralRefusesSmallImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	if _, err := DetectQuadrilateral(img); err == nil {
		t.Fatal("expected too-small image to fail detection")
	}
}

func TestRectangularityIsOneForRectangle(t *testing.T) {
	rect := Quad{TL: Point{0, 0}, TR: Point{100, 0}, BR: Point{100, 60}, BL: Point{0, 60}}
	score := rectangularity(rect.TL, rect.TR, rect.BR, rect.BL)
	if score < 0.99 {
		t.Fatalf("rectangularity = %f, want ~1", score)
	}
}

func TestRectangularityPenalisesParallelogram(t *testing.T) {
	rect := Quad{TL: Point{0, 0}, TR: Point{100, 0}, BR: Point{130, 60}, BL: Point{30, 60}}
	score := rectangularity(rect.TL, rect.TR, rect.BR, rect.BL)
	if score > 0.85 {
		t.Fatalf("rectangularity of parallelogram = %f, want noticeably lower", score)
	}
}

func TestAspectPlausibilityPrefersRealisticCards(t *testing.T) {
	a4 := Quad{TL: Point{0, 0}, TR: Point{85, 0}, BR: Point{85, 54}, BL: Point{0, 54}} // credit card
	if score := aspectPlausibility(a4.TL, a4.TR, a4.BR, a4.BL); score < 0.95 {
		t.Fatalf("credit-card aspect score = %f", score)
	}
	square := Quad{TL: Point{0, 0}, TR: Point{100, 0}, BR: Point{100, 100}, BL: Point{0, 100}}
	if score := aspectPlausibility(square.TL, square.TR, square.BR, square.BL); score != 0.5 {
		t.Fatalf("square aspect score = %f, want 0.5", score)
	}
}

func TestConvexOrderRejectsSelfIntersecting(t *testing.T) {
	// Bowtie configuration: edges cross.
	bad := Quad{TL: Point{0, 0}, TR: Point{100, 100}, BR: Point{100, 0}, BL: Point{0, 100}}
	if convexOrder(bad.TL, bad.TR, bad.BR, bad.BL) {
		t.Fatal("expected bowtie to be rejected")
	}
}

func TestScoreQuadRejectsTinyAndOversized(t *testing.T) {
	imgArea := 400.0 * 300.0
	huge := Quad{TL: Point{0, 0}, TR: Point{399, 0}, BR: Point{399, 299}, BL: Point{0, 299}}
	if _, s := scoreQuad(huge.TL, huge.TR, huge.BR, huge.BL, imgArea, 1, 1000); s != 0 {
		t.Fatalf("oversized card score = %f, want 0", s)
	}
	tiny := Quad{TL: Point{10, 10}, TR: Point{20, 10}, BR: Point{20, 16}, BL: Point{10, 16}}
	if _, s := scoreQuad(tiny.TL, tiny.TR, tiny.BR, tiny.BL, imgArea, 1, 1000); s != 0 {
		t.Fatalf("tiny card score = %f, want 0", s)
	}
}

func dist(a, b Point) float64 {
	return ((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y))
}
