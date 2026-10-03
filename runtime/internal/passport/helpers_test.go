package passport

import (
	"context"
	"image"
	"image/color"
	"testing"
	"time"
)

// testContext returns a non-cancelled context for tests that do not care
// about cancellation.
func testContext() context.Context { return context.Background() }

// newTestImage returns a uniform RGBA image with the given dimensions. Tests
// use this as the source for the geometric detector; the detector's output
// depends only on the rectangle, so the pixel values are irrelevant.
func newTestImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.NRGBA{R: 200, G: 200, B: 200, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, bg)
		}
	}
	return img
}

// makeTestPhoto returns a fresh white RGBA image at the given pixel size.
// Tests use this as the destination for the guide overlay so they can
// assert that the overlay mutates pixels.
func makeTestPhoto(w, h int) *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, w, h))
}

// fixedClock returns a function that always reports the supplied time.
// Tests inject it via Service.WithClock so the created_at columns are
// stable across runs.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// _ keeps the testing import in case the package grows more tests later.
var _ = testing.Short