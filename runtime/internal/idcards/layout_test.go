package idcards

import (
	"math"
	"testing"
)

func TestSheetDimensionsKnownPresets(t *testing.T) {
	cases := map[SheetPreset][2]float64{
		SheetA4:     {210, 297},
		SheetA3:     {297, 420},
		SheetLetter: {215.9, 279.4},
		SheetLegal:  {215.9, 355.6},
	}
	for preset, want := range cases {
		w, h, err := SheetDimensions(preset)
		if err != nil {
			t.Fatalf("SheetDimensions(%q) error: %v", preset, err)
		}
		if w != want[0] || h != want[1] {
			t.Fatalf("SheetDimensions(%q) = %fx%f, want %fx%f", preset, w, h, want[0], want[1])
		}
	}
	if _, _, err := SheetDimensions("B5"); err == nil {
		t.Fatal("expected error for unknown sheet preset")
	}
}

func TestCardDimensionsCR80(t *testing.T) {
	w, h, err := CardDimensions(CardCR80)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w-85.6) > 1e-9 || math.Abs(h-53.98) > 1e-9 {
		t.Fatalf("CR80 = %fx%f", w, h)
	}
}

func TestLayoutFrontOnlyCentredOnSheet(t *testing.T) {
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Placements) != 1 {
		t.Fatalf("placements = %d, want 1", len(layout.Placements))
	}
	p := layout.Placements[0]
	if p.Source != "front" {
		t.Fatalf("source = %s", p.Source)
	}
	if math.Abs(p.Width-85.6) > 1e-6 || math.Abs(p.Height-53.98) > 1e-6 {
		t.Fatalf("dims = %fx%f, want 85.6x53.98", p.Width, p.Height)
	}
	// Card centred: x = (210-85.6)/2 = 62.2; y = (297-53.98)/2 = 121.51
	if math.Abs(p.X-62.2) > 0.01 || math.Abs(p.Y-121.51) > 0.01 {
		t.Fatalf("centred position = %fx%f, want ~62.2x121.51", p.X, p.Y)
	}
}

func TestLayoutSideBySidePlacesBackToTheRight(t *testing.T) {
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutSideBySide, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Placements) != 2 {
		t.Fatalf("placements = %d, want 2", len(layout.Placements))
	}
	front := layout.Placements[0]
	back := layout.Placements[1]
	if back.X <= front.X {
		t.Fatalf("back.x (%f) must be greater than front.x (%f)", back.X, front.X)
	}
	if math.Abs(back.Height-front.Height) > 0.01 {
		t.Fatalf("side-by-side heights differ: front %f back %f", front.Height, back.Height)
	}
}

func TestLayoutVerticalPlacesBackBelow(t *testing.T) {
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutVertical, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	front := layout.Placements[0]
	back := layout.Placements[1]
	if back.Y <= front.Y {
		t.Fatalf("back.y (%f) must be greater than front.y (%f)", back.Y, front.Y)
	}
}

func TestLayoutCalibrationOffsetsBackOnly(t *testing.T) {
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutSideBySide,
		ActualSize: true, CalibrationDX: 1.5, CalibrationDY: -0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	frontNoOffset := ComputeLayoutMust(t, LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutFrontOnly, ActualSize: true,
	})
	front := layout.Placements[0]
	if math.Abs(front.X-frontNoOffset.Placements[0].X) > 0.001 {
		t.Fatalf("front calibration shifted x: %f vs %f", front.X, frontNoOffset.Placements[0].X)
	}
	back := layout.Placements[1]
	backNoOffset := ComputeLayoutMust(t, LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutSideBySide, ActualSize: true,
	})
	if math.Abs(back.X-backNoOffset.Placements[1].X-1.5) > 0.001 {
		t.Fatalf("back x not offset by 1.5: %f vs %f", back.X, backNoOffset.Placements[1].X)
	}
	if math.Abs(back.Y-backNoOffset.Placements[1].Y-(-0.5)) > 0.001 {
		t.Fatalf("back y not offset by -0.5: %f vs %f", back.Y, backNoOffset.Placements[1].Y)
	}
}

func TestLayoutDuplexBackLongEdgeFlipsHorizontally(t *testing.T) {
	frontLayout := ComputeLayoutMust(t, LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutDuplexFront, ActualSize: true,
	})
	backLayout := ComputeLayoutMust(t, LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutDuplexBack, FlipEdge: FlipLongEdge, ActualSize: true,
	})
	front := frontLayout.Placements[0]
	back := backLayout.Placements[0]
	// Long-edge flip mirrors X around the sheet centre.
	expectedX := frontLayout.SheetWidthMm - front.Width - front.X
	if math.Abs(back.X-expectedX) > 0.001 {
		t.Fatalf("back X = %f, want %f", back.X, expectedX)
	}
	if math.Abs(back.Y-front.Y) > 0.001 {
		t.Fatalf("back Y should equal front Y on long-edge flip, got %f vs %f", back.Y, front.Y)
	}
}

func TestLayoutDuplexBackShortEdgeFlipsBothAxes(t *testing.T) {
	frontLayout := ComputeLayoutMust(t, LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutDuplexFront, ActualSize: true,
	})
	backLayout := ComputeLayoutMust(t, LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutDuplexBack, FlipEdge: FlipShortEdge, ActualSize: true,
	})
	front := frontLayout.Placements[0]
	back := backLayout.Placements[0]
	if math.Abs(back.X-(frontLayout.SheetWidthMm-front.Width-front.X)) > 0.001 {
		t.Fatalf("short-edge X mismatch: %f vs %f", back.X, frontLayout.SheetWidthMm-front.Width-front.X)
	}
	if math.Abs(back.Y-(frontLayout.SheetHeightMm-front.Height-front.Y)) > 0.001 {
		t.Fatalf("short-edge Y mismatch: %f vs %f", back.Y, frontLayout.SheetHeightMm-front.Height-front.Y)
	}
}

func TestLayoutCustomCardSizeRespectsBounds(t *testing.T) {
	if _, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCustom, CardWidthMm: 5, CardHeightMm: 50,
		LayoutKind: LayoutFrontOnly, ActualSize: true,
	}); err == nil {
		t.Fatal("expected error for too-small custom card")
	}
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCustom, CardWidthMm: 100, CardHeightMm: 70,
		LayoutKind: LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(layout.CardWidthMm-100) > 1e-9 {
		t.Fatalf("custom width = %f", layout.CardWidthMm)
	}
}

func TestLayoutFitToGridScalesCard(t *testing.T) {
	layout, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutFrontOnly, ActualSize: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	// With ActualSize=false the card should still fit but the dimensions
	// remain 85.6x53.98 because they already fit the printable area. This
	// regression guard ensures the fit-to-grid path is wired.
	if math.Abs(layout.Placements[0].Width-85.6) > 0.001 {
		t.Fatalf("fit dims = %f", layout.Placements[0].Width)
	}
}

func TestLayoutRejectsUnknownLayoutKind(t *testing.T) {
	if _, err := ComputeLayout(LayoutInput{
		Sheet: SheetA4, Card: CardCR80, LayoutKind: LayoutKind("invalid"),
	}); err == nil {
		t.Fatal("expected error for unknown layout kind")
	}
}

func ComputeLayoutMust(t *testing.T, in LayoutInput) Layout {
	t.Helper()
	layout, err := ComputeLayout(in)
	if err != nil {
		t.Fatal(err)
	}
	return layout
}
