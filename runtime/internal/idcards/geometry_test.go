package idcards

import (
	"math"
	"testing"
)

func TestQuadFromRectProducesCanonicalOrder(t *testing.T) {
	rect := RectangleF{X: 10, Y: 20, Width: 30, Height: 40}
	quad := QuadFromRect(rect)
	if quad.TL != (Point{10, 20}) {
		t.Fatalf("TL = %+v", quad.TL)
	}
	if quad.TR != (Point{40, 20}) {
		t.Fatalf("TR = %+v", quad.TR)
	}
	if quad.BR != (Point{40, 60}) {
		t.Fatalf("BR = %+v", quad.BR)
	}
	if quad.BL != (Point{10, 60}) {
		t.Fatalf("BL = %+v", quad.BL)
	}
	if got := quad.Area(); math.Abs(got-1200) > 1e-9 {
		t.Fatalf("area = %f, want 1200", got)
	}
}

func TestComputeHomographyIdentityForMatchingQuads(t *testing.T) {
	src := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 100, Height: 60})
	dst := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 100, Height: 60})
	h, err := ComputeHomography(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Point{{0, 0}, {100, 0}, {100, 60}, {0, 60}, {50, 30}} {
		got := ApplyHomography(h, p)
		if math.Abs(got.X-p.X) > 1e-6 || math.Abs(got.Y-p.Y) > 1e-6 {
			t.Fatalf("identity warp moved %+v to %+v", p, got)
		}
	}
}

func TestComputeHomographyRectangularisesSkewedQuad(t *testing.T) {
	// A trapezoid in source space should map back to a perfect rectangle.
	src := Quad{
		TL: Point{10, 0},
		TR: Point{90, 5},
		BR: Point{95, 55},
		BL: Point{5, 50},
	}
	dst := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 100, Height: 60})
	h, err := ComputeHomography(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range src.Points() {
		got := ApplyHomography(h, p)
		expected := dst.Points()[i]
		if math.Abs(got.X-expected.X) > 1e-6 || math.Abs(got.Y-expected.Y) > 1e-6 {
			t.Fatalf("src %d: %+v -> %+v, want %+v", i, p, got, expected)
		}
	}
}

func TestComputeHomographyInverseRoundTrip(t *testing.T) {
	src := Quad{
		TL: Point{11, 3},
		TR: Point{99, 7},
		BR: Point{104, 56},
		BL: Point{4, 49},
	}
	dst := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 100, Height: 60})
	h, err := ComputeHomography(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := ComputeInverseHomography(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Point{{0, 0}, {25, 15}, {100, 60}, {50, 30}} {
		forward := ApplyHomography(h, p)
		back := ApplyHomography(inv, forward)
		if math.Abs(back.X-p.X) > 1e-6 || math.Abs(back.Y-p.Y) > 1e-6 {
			t.Fatalf("round trip %+v -> %+v -> %+v", p, forward, back)
		}
	}
}

func TestComputeHomographyRejectsDegenerateQuad(t *testing.T) {
	src := Quad{
		TL: Point{0, 0}, TR: Point{10, 0}, BR: Point{20, 0}, BL: Point{30, 0},
	}
	dst := QuadFromRect(RectangleF{X: 0, Y: 0, Width: 10, Height: 10})
	if _, err := ComputeHomography(src, dst); err == nil {
		t.Fatal("expected degenerate quad to error")
	}
}

func TestQuadAreaNegativeForClockwise(t *testing.T) {
	ccw := Quad{TL: Point{0, 0}, TR: Point{10, 0}, BR: Point{10, 10}, BL: Point{0, 10}}
	cw := Quad{TL: Point{0, 0}, TR: Point{0, 10}, BR: Point{10, 10}, BL: Point{10, 0}}
	if ccw.Area() <= 0 {
		t.Fatalf("ccw area = %f, want positive", ccw.Area())
	}
	if cw.Area() >= 0 {
		t.Fatalf("cw area = %f, want negative", cw.Area())
	}
}

func TestRectangleContains(t *testing.T) {
	rect := RectangleF{X: 10, Y: 10, Width: 100, Height: 60}
	cases := []struct {
		p    Point
		want bool
	}{
		{Point{10, 10}, true},
		{Point{109, 69}, true},
		{Point{110, 70}, false},
		{Point{5, 30}, false},
		{Point{60, 5}, false},
	}
	for _, tc := range cases {
		if got := rect.Contains(tc.p); got != tc.want {
			t.Fatalf("Contains(%+v) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestMakeQuadRejectsNonFinite(t *testing.T) {
	if _, err := MakeQuad(Point{X: math.NaN()}, Point{1, 2}, Point{3, 4}, Point{5, 6}); err == nil {
		t.Fatal("expected error for NaN")
	}
	if _, err := MakeQuad(Point{1, 1}, Point{2, 2}, Point{3, 3}, Point{4, math.Inf(1)}); err == nil {
		t.Fatal("expected error for Inf")
	}
	if _, err := MakeQuad(Point{1, 1}, Point{2, 2}, Point{3, 3}, Point{4, 5}); err != nil {
		t.Fatalf("unexpected error for valid quad: %v", err)
	}
}
