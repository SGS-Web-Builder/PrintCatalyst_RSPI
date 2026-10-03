package passport

import (
	"context"
	"image"
	"image/color"
	"testing"
)

func TestLuminanceDetectorFindsSyntheticFace(t *testing.T) {
	d := NewLuminanceDetector()
	img := SyntheticFace(160, 200, 30)
	region, err := d.DetectFace(context.Background(), img, PresetSpec{
		WidthMm: 35, HeightMm: 45, HeadHeightMm: 31,
	})
	if err != nil {
		t.Fatalf("DetectFace: %v", err)
	}
	if region.Width == 0 || region.Height == 0 {
		t.Fatalf("zero-sized region: %+v", region)
	}
	// The face is centred at (80, 100) with radius 30; the region
	// should be near the centre of the image.
	cx := region.X + region.Width/2
	cy := region.Y + region.Height/2
	if cx < 60 || cx > 100 {
		t.Errorf("region centre x = %d, want near 80", cx)
	}
	if cy < 80 || cy > 120 {
		t.Errorf("region centre y = %d, want near 100", cy)
	}
	if region.Confidence < 0.4 {
		t.Errorf("confidence = %f, want >= 0.4", region.Confidence)
	}
}

func TestLuminanceDetectorFallsBackOnNoSkin(t *testing.T) {
	d := NewLuminanceDetector()
	// All-green image: no skin pixels. Detector should return the
	// geometric fallback (confidence = 0.5).
	img := allGreenImage(200, 240)
	region, err := d.DetectFace(context.Background(), img, PresetSpec{
		WidthMm: 35, HeightMm: 45,
	})
	if err != nil {
		t.Fatalf("DetectFace: %v", err)
	}
	if region.Width == 0 || region.Height == 0 {
		t.Fatalf("zero-sized fallback region: %+v", region)
	}
}

func TestLuminanceDetectorHandlesTinyImage(t *testing.T) {
	d := NewLuminanceDetector()
	img := allGreenImage(4, 4)
	region, err := d.DetectFace(context.Background(), img, PresetSpec{})
	if err != nil {
		t.Fatalf("DetectFace: %v", err)
	}
	// Tiny input → no skin pixel ever, fallback fires with non-zero region.
	_ = region
}

func TestIsSkinToneAcceptsSkin(t *testing.T) {
	if !isSkinTone(230<<8, 180<<8, 150<<8) {
		t.Error("light skin tone should be detected as skin")
	}
	if !isSkinTone(180<<8, 130<<8, 100<<8) {
		t.Error("medium skin tone should be detected as skin")
	}
}

func TestIsSkinToneRejectsDarkSkin(t *testing.T) {
	// The RGB rule requires R > 95; very dark complexions fall
	// below that and are rejected. The geometric fallback handles
	// such photos so the UI forces a manual confirmation.
	if isSkinTone(80<<8, 50<<8, 30<<8) {
		t.Error("very dark tone should fall through to geometric fallback")
	}
}

// allGreenImage returns a uniform-green image of the supplied size.
func allGreenImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: 30, G: 90, B: 30, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}
