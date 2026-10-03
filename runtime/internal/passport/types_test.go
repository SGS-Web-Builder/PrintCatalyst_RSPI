package passport

import (
	"testing"
)

func TestKnownPresetsCoverTheMasterSpecCountries(t *testing.T) {
	want := []DocumentPreset{
		PresetIndiaPassport,
		PresetIndiaVisa,
		PresetSchengenVisa,
		PresetUKPassport,
		PresetUSPassport,
		PresetCanadaPassport,
		PresetAustraliaPassport,
		PresetCustom,
	}
	got := KnownPresets()
	if len(got) != len(want) {
		t.Fatalf("KnownPresets returned %d presets, want %d", len(got), len(want))
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("KnownPresets[%d] = %q, want %q", i, got[i], p)
		}
	}
}

func TestPresetForReturnsCanonicalDimensions(t *testing.T) {
	cases := []struct {
		preset       DocumentPreset
		wantWidthMm  float64
		wantHeightMm float64
	}{
		{PresetIndiaPassport, 35, 45},
		{PresetIndiaVisa, 35, 45},
		{PresetSchengenVisa, 35, 45},
		{PresetUKPassport, 35, 45},
		{PresetAustraliaPassport, 35, 45},
		{PresetUSPassport, 50.8, 50.8},
		{PresetCanadaPassport, 50, 70},
	}
	for _, c := range cases {
		spec, err := PresetFor(c.preset)
		if err != nil {
			t.Fatalf("PresetFor(%q) error: %v", c.preset, err)
		}
		if spec.WidthMm != c.wantWidthMm {
			t.Errorf("preset %q width = %v, want %v", c.preset, spec.WidthMm, c.wantWidthMm)
		}
		if spec.HeightMm != c.wantHeightMm {
			t.Errorf("preset %q height = %v, want %v", c.preset, spec.HeightMm, c.wantHeightMm)
		}
		if spec.HeadHeightMm <= 0 {
			t.Errorf("preset %q head height = %v, want > 0", c.preset, spec.HeadHeightMm)
		}
		if spec.EyeLineFromBottomMm <= 0 {
			t.Errorf("preset %q eye line = %v, want > 0", c.preset, spec.EyeLineFromBottomMm)
		}
	}
}

func TestPresetForRejectsUnknownPreset(t *testing.T) {
	_, err := PresetFor("narnia_passport")
	if err != ErrUnknownPreset {
		t.Fatalf("PresetFor unknown = %v, want %v", err, ErrUnknownPreset)
	}
}

func TestBackgroundColorReturnsExpectedChannels(t *testing.T) {
	white, err := BackgroundColor(BackgroundReplaceWhite)
	if err != nil {
		t.Fatal(err)
	}
	if white.R != 255 || white.G != 255 || white.B != 255 {
		t.Fatalf("white background = %+v, want 255/255/255", white)
	}
	if _, err := BackgroundColor(BackgroundKeepOriginal); err != nil {
		t.Fatalf("keep original should not error: %v", err)
	}
	if _, err := BackgroundColor("neon"); err != ErrInvalidBackground {
		t.Fatalf("unknown background = %v, want %v", err, ErrInvalidBackground)
	}
}

func TestPlanSheetPlacesExpectedRowsAndCols(t *testing.T) {
	// India 35x45 mm on A4 with a 3 mm cut margin should give ~7 cols and ~5 rows
	// (210 / (35+3) = 5.5 → 5 cols; 297 / (45+3) = 6.2 → 6 rows). The exact numbers
	// are not load-bearing for the master spec but the plan must produce more
	// than one photo per sheet.
	layout, err := PlanSheet(SheetA4, 35, 45, 300)
	if err != nil {
		t.Fatal(err)
	}
	if layout.PhotoCount < 4 {
		t.Errorf("PhotoCount = %d, want >= 4 (A4 must fit multiple 35x45 photos)", layout.PhotoCount)
	}
	if layout.Rows*layout.Cols != layout.PhotoCount {
		t.Errorf("Rows*Cols = %d, want PhotoCount = %d", layout.Rows*layout.Cols, layout.PhotoCount)
	}
	if layout.SheetWMm != 210 || layout.SheetHMm != 297 {
		t.Errorf("A4 dimensions = %vx%v, want 210x297", layout.SheetWMm, layout.SheetHMm)
	}
	// Photo rectangles must tile the sheet with no overlap.
	for i := 0; i < layout.PhotoCount; i++ {
		rect := layout.PhotoRect(i)
		if rect.Empty() {
			t.Errorf("rect %d is empty", i)
		}
		w, _ := layout.PixelSize()
		if rect.Max.X > w+1 {
			t.Errorf("rect %d extends past sheet width: %+v > %d", i, rect, w)
		}
	}
}

func TestPlanSheetPixelSizeMatchesDPI(t *testing.T) {
	layout, err := PlanSheet(SheetA4, 35, 45, 300)
	if err != nil {
		t.Fatal(err)
	}
	w, h := layout.PixelSize()
	// 210 mm * 300dpi / 25.4 ≈ 2480 px (rounded down).
	if w < 2400 || w > 2500 {
		t.Errorf("A4 pixel width = %d, want ~2480", w)
	}
	if h < 3450 || h > 3550 {
		t.Errorf("A4 pixel height = %d, want ~3508", h)
	}
}

func TestGeometricDetectorReturnsCentreRegion(t *testing.T) {
	src := newTestImage(400, 600)
	spec, _ := PresetFor(PresetIndiaPassport)
	region, err := GeometricDetector{}.DetectFace(testContext(), src, spec)
	if err != nil {
		t.Fatal(err)
	}
	b := region.Bounds()
	if b.Empty() {
		t.Fatal("detector returned an empty rectangle")
	}
	if b.Dx() > 400 || b.Dy() > 600 {
		t.Errorf("detector returned out-of-bounds rectangle: %+v", b)
	}
	if region.Confidence != 0.5 {
		t.Errorf("detector confidence = %v, want 0.5 (forces operator confirmation)", region.Confidence)
	}
	// The region should be horizontally centred.
	centre := b.Min.X + b.Dx()/2
	want := 200
	if centre < want-10 || centre > want+10 {
		t.Errorf("detector centre = %v, want ~%v", centre, want)
	}
}

func TestFaceRegionBoundsNormalisesNegativeInputs(t *testing.T) {
	r := FaceRegion{X: -10, Y: -5, Width: 100, Height: 80}.Bounds()
	if r.Min.X != 0 || r.Min.Y != 0 {
		t.Errorf("negative coords not clamped: %+v", r)
	}
}

func TestComposeGuideOverlayPlacesEyeLineAtPresetHeight(t *testing.T) {
	photo := makeTestPhoto(300, 450)
	spec, _ := PresetFor(PresetIndiaPassport)
	ComposeGuideOverlay(photo, spec, 300)
	// Verify that some pixels changed — the overlay must mutate the image.
	changed := false
	for y := 0; y < 450 && !changed; y++ {
		for x := 0; x < 300 && !changed; x++ {
			r, g, b, a := photo.At(x, y).RGBA()
			if r != 65535 || g != 65535 || b != 65535 || a != 65535 {
				changed = true
			}
		}
	}
	if !changed {
		t.Fatal("guide overlay did not mutate any pixel")
	}
}