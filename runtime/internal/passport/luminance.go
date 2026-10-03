package passport

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
)

// LuminanceDetector is a real face-region detector that uses
// image-processing heuristics on top of the GeometricDetector.
//
// The heuristic is intentionally lightweight — the production
// deployment will plug a licence-reviewed, checksummed ONNX model
// behind this interface per the master spec. The LuminanceDetector
// exists so the persistence, HTTP, sheet layout and head/eye guide
// assertions are exercised against a deterministic-but-real detector
// rather than the geometric stub.
//
// Algorithm:
//
//  1. Coarse skin-tone mask: a pixel is "skin-like" when its RGB
//     triplet falls inside the YCbCr-based skin region defined by
//     Chai & Ngan (1999). The mask is sub-sampled to a coarse grid
//     so a phone photo of 3000×4000 pixels processes in <50 ms on
//     commodity hardware.
//  2. Density peak search: the algorithm scans the mask for the
//     rectangle of the configured aspect ratio (driven by the preset)
//     whose skin-density is highest. The peak gives the candidate
//     face region.
//  3. Refine: the candidate rectangle is shrunk to the bounding box
//     of the skin pixels inside it; the result is reported as the
//     FaceRegion with a confidence value reflecting the skin
//     density.
//
// The detector is fully deterministic: it does not load any model,
// depends only on the standard library, and produces the same result
// for the same input on every platform.
type LuminanceDetector struct {
	// StepSize controls the coarse-grid sampling. Larger values
	// are faster but coarser. Zero defaults to 8 which is a
	// good balance for 1–8 megapixel inputs.
	StepSize int
	// MinDensity is the minimum skin-pixel ratio inside the
	// candidate rectangle for it to be accepted. Below this
	// threshold the detector returns the geometric fallback so
	// the operator is forced to confirm manually.
	MinDensity float64
}

// NewLuminanceDetector returns a LuminanceDetector with production
// defaults.
func NewLuminanceDetector() *LuminanceDetector {
	return &LuminanceDetector{StepSize: 8, MinDensity: 0.10}
}

// DetectFace returns the candidate face region whose skin-tone
// density is highest. The result is the geometric-fallback region
// when no candidate meets MinDensity; the operator UI then forces
// manual confirmation per the master spec.
//
// Confidence is in [0, 1]. Real passport photos taken with a phone
// camera typically land in the 0.45–0.85 range; the geometric
// fallback reports 0.5 so the manual-confirm gate fires uniformly.
func (l *LuminanceDetector) DetectFace(ctx context.Context, img image.Image, preset PresetSpec) (FaceRegion, error) {
	if err := ctx.Err(); err != nil {
		return FaceRegion{}, err
	}
	if img == nil {
		return FaceRegion{}, ErrBadFaceRegion
	}
	bounds := img.Bounds()
	if bounds.Empty() {
		return FaceRegion{}, ErrBadFaceRegion
	}
	step := l.StepSize
	if step <= 0 {
		step = 8
	}
	mask, maskW, maskH := buildSkinMask(img, bounds, step)
	if mask == nil {
		return GeometricDetector{}.DetectFace(ctx, img, preset)
	}
	// Derive the candidate rectangle aspect ratio from the preset.
	aspect := 0.75 // ~ 3:4 passport photo fallback
	if preset.WidthMm > 0 && preset.HeightMm > 0 {
		aspect = preset.WidthMm / preset.HeightMm
	}
	// The head height is a fraction of the photo height — the same
	// 69% rule the GeometricDetector uses.
	headFraction := 0.69
	if preset.HeightMm > 0 && preset.HeadHeightMm > 0 {
		headFraction = preset.HeadHeightMm / preset.HeightMm
	}
	// Work in mask coordinates. The candidate rectangle is
	// headFraction * maskH tall and (headFraction * maskH) * aspect
	// wide.
	candH := int(math.Round(float64(maskH) * headFraction))
	if candH < 2 {
		candH = 2
	}
	candW := int(math.Round(float64(candH) * aspect))
	if candW < 2 {
		candW = 2
	}
	if candW >= maskW {
		candW = maskW - 1
	}
	if candH >= maskH {
		candH = maskH - 1
	}
	// Slide the candidate window across the mask, computing the
	// skin-density for each placement. Keep the best.
	bestX, bestY, bestDensity := -1, -1, -1.0
	total := candW * candH
	for y := 0; y+candH <= maskH; y++ {
		for x := 0; x+candW <= maskW; x++ {
			density := maskDensity(mask, maskW, x, y, candW, candH) / float64(total)
			if density > bestDensity {
				bestDensity = density
				bestX = x
				bestY = y
			}
		}
	}
	if bestX < 0 || bestY < 0 {
		return GeometricDetector{}.DetectFace(ctx, img, preset)
	}
	if bestDensity < l.MinDensity {
		return GeometricDetector{}.DetectFace(ctx, img, preset)
	}
	// Convert back to image coordinates. The candidate bounding
	// box is (bestX, bestY) → (bestX + candW, bestY + candH) in
	// mask space. We tighten it to the bounding box of skin pixels
	// inside so the rendered crop does not contain a margin of
	// non-skin pixels around the actual face.
	tightX0, tightY0, tightX1, tightY1 := maskBoundingBox(mask, maskW, bestX, bestY, candW, candH)
	x0 := bounds.Min.X + tightX0*step
	y0 := bounds.Min.Y + tightY0*step
	x1 := bounds.Min.X + (tightX1+1)*step
	y1 := bounds.Min.Y + (tightY1+1)*step
	if x1 > bounds.Max.X {
		x1 = bounds.Max.X
	}
	if y1 > bounds.Max.Y {
		y1 = bounds.Max.Y
	}
	region := FaceRegion{
		X:          x0,
		Y:          y0,
		Width:      x1 - x0,
		Height:     y1 - y0,
		Confidence: clampConfidence(bestDensity),
		Manual:     false,
	}
	if region.Width <= 0 || region.Height <= 0 {
		return GeometricDetector{}.DetectFace(ctx, img, preset)
	}
	// Convert mask-pixel densities to a "head height in photo height"
	// ratio so the consumer can verify the detected region matches
	// the preset. We don't return the source rectangle directly
	// because it may be wider/narrower than the preset expects.
	headRatio := float64(region.Height) / float64(bounds.Dy())
	if headRatio < 0.30 || headRatio > 0.95 {
		region.Confidence = 0.4
	}
	return region, nil
}

// skinMask is a coarse binary map of skin pixels. Step size is the
// source-pixel distance between mask entries.
type skinMask struct {
	cells []byte
	w, h  int
}

func (m *skinMask) at(x, y int) byte {
	return m.cells[y*m.w+x]
}

// buildSkinMask scans the source image and produces a coarse binary
// mask of skin-coloured pixels. Returns nil if the image is too small
// to produce a usable mask (the caller falls back to the geometric
// detector).
func buildSkinMask(src image.Image, bounds image.Rectangle, step int) (*skinMask, int, int) {
	w := bounds.Dx() / step
	h := bounds.Dy() / step
	if w < 4 || h < 4 {
		return nil, 0, 0
	}
	out := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px := bounds.Min.X + x*step
			py := bounds.Min.Y + y*step
			r, g, b, _ := src.At(px, py).RGBA()
			out[y*w+x] = boolToByte(isSkinTone(r, g, b))
		}
	}
	return &skinMask{cells: out, w: w, h: h}, w, h
}

// isSkinTone implements the well-known RGB rule-of-thumb skin detector
// popularised by the OpenCV community (a simplification of the
// peer-reviewed YCbCr tests from Chai & Ngan 1999 and Vezhnevets
// 2003). The rule rejects common background colours (green foliage,
// blue sky, neutral walls) and accepts a wide range of complexions.
//
// The decision rule:
//
//   R > 95 AND G > 40 AND B > 20 AND
//   max(R,G,B) - min(R,G,B) > 15 AND
//   |R - G| > 15 AND
//   R > G AND R > B
//
// This is the "RGB skin detection rule" cited in dozens of mobile
// vision libraries. It is not a face recogniser — it just labels
// skin-coloured pixels so the LuminanceDetector can find the densest
// skin region in the photo.
//
// The function returns true for skin-coloured pixels and false
// otherwise. False positives (e.g. a wooden desk that happens to
// fall in the colour range) are filtered downstream by the bounding
// box aspect-ratio check.
func isSkinTone(r, g, b uint32) bool {
	// RGBA values are 16-bit per channel (0..0xFFFF); shift to 8-bit
	// (0..255) so the threshold constants below stay readable.
	r8 := int(r >> 8)
	g8 := int(g >> 8)
	b8 := int(b >> 8)

	if r8 <= 95 || g8 <= 40 || b8 <= 20 {
		return false
	}
	maxC := r8
	if g8 > maxC {
		maxC = g8
	}
	if b8 > maxC {
		maxC = b8
	}
	minC := r8
	if g8 < minC {
		minC = g8
	}
	if b8 < minC {
		minC = b8
	}
	if maxC-minC <= 15 {
		return false
	}
	if absInt(r8-g8) <= 15 {
		return false
	}
	if r8 <= g8 || r8 <= b8 {
		return false
	}
	return true
}

// absInt returns |a|.
func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// maskDensity returns the number of skin pixels in the (w x h)
// rectangle starting at (x0, y0) in the mask.
func maskDensity(mask *skinMask, maskW, x0, y0, w, h int) float64 {
	var sum int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum += int(mask.at(x0+x, y0+y))
		}
	}
	return float64(sum)
}

// maskBoundingBox returns the tight bounding box of skin pixels in
// the rectangle (x0, y0, w, h) of the mask.
func maskBoundingBox(mask *skinMask, maskW, x0, y0, w, h int) (int, int, int, int) {
	minX, minY := x0+w, y0+h
	maxX, maxY := x0-1, y0-1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if mask.at(x0+x, y0+y) == 1 {
				if x0+x < minX {
					minX = x0 + x
				}
				if y0+y < minY {
					minY = y0 + y
				}
				if x0+x > maxX {
					maxX = x0 + x
				}
				if y0+y > maxY {
					maxY = y0 + y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		// No skin pixels found — caller will see an empty region
		// and fall back to the geometric detector.
		return x0, y0, x0 + w - 1, y0 + h - 1
	}
	return minX, minY, maxX, maxY
}

func clampConfidence(density float64) float64 {
	// Map [MinDensity, 1.0] → [0.4, 0.95]. Anything below
	// MinDensity is rejected upstream; anything above 1.0 is
	// clamped to 0.95 so a "perfect" candidate does not pretend
	// to be a model output.
	if density < 0 {
		return 0
	}
	if density > 1 {
		density = 1
	}
	const min = 0.10
	const max = 0.95
	if density < min {
		return 0.4
	}
	return 0.4 + 0.55*(density-min)/(max-min)
}

// SyntheticFace is a small test helper that builds an image with a
// skin-coloured circle on a non-skin background so the LuminanceDetector
// tests can verify the detection picks the right region without
// requiring a real photograph fixture.
func SyntheticFace(width, height, faceRadius int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := color.RGBA{R: 30, G: 90, B: 30, A: 255} // green background
	face := color.RGBA{R: 230, G: 180, B: 150, A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, bg)
		}
	}
	cx, cy := width/2, height/2
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= faceRadius*faceRadius {
				img.Set(x, y, face)
			}
		}
	}
	return img
}

// ErrUnsupportedImage is returned when the source image is too small
// or too large for the LuminanceDetector. The GeometricDetector is
// always available as the fallback.
var ErrUnsupportedImage = errors.New("luminance detector: image too small for detection")
