package passport

import (
	"context"
	"image"
)

// FaceDetector abstracts the face-region detector. The production deployment
// will plug a licence-reviewed, checksummed ONNX model behind this
// interface; the phase 6 slice ships a deterministic geometric stub so the
// persistence, HTTP, sheet layout, background colour and head/eye guide
// assertions can be exercised end-to-end without an ML dependency.
//
// Implementations must:
//   - Return a FaceRegion whose Bounds() is inside the supplied image bounds.
//   - Return a confidence in [0, 1]. Confidence values below MinConfidence
//     force the operator to confirm manually in the UI per the master spec.
//   - Be safe for concurrent use from multiple goroutines. The Service calls
//     the detector from a request-scoped context, so a slow detector must
//     respect ctx cancellation.
type FaceDetector interface {
	DetectFace(ctx context.Context, img image.Image, preset PresetSpec) (FaceRegion, error)
}

// GeometricDetector is the deterministic stub detector. It returns the
// central region of the source image scaled to the preset's required head
// height, which yields a layout-correct (but not content-correct) preview.
//
// The stub is intentionally simple: the master spec requires the real
// detector to be licence-reviewed and checksummed before it ships, and the
// UI surfaces a compliance disclaimer so the merchant understands that a
// geometric estimate is not a guarantee of acceptance.
type GeometricDetector struct{}

// DetectFace returns the centred face region whose height equals the
// preset's required head height in pixels and whose width is the preset's
// head-height-to-photo-height ratio. The returned confidence is fixed at
// 0.5 so the UI forces the operator to confirm the region before composing.
//
// The preset is supplied so the detector can pick a head height that matches
// the destination photo rather than the source image. When preset is empty
// the detector falls back to a 40% centre crop.
func (GeometricDetector) DetectFace(ctx context.Context, img image.Image, preset PresetSpec) (FaceRegion, error) {
	if err := ctx.Err(); err != nil {
		return FaceRegion{}, err
	}
	if img == nil {
		return FaceRegion{}, ErrBadFaceRegion
	}
	b := img.Bounds()
	if b.Empty() {
		return FaceRegion{}, ErrBadFaceRegion
	}
	// Default head-height ratio: 69% of the photo height when no preset is
	// supplied (this matches the Indian / Schengen 35 x 45 mm specification).
	headRatio := 0.69
	if preset.HeightMm > 0 && preset.HeadHeightMm > 0 {
		headRatio = preset.HeadHeightMm / preset.HeightMm
	}
	srcW := b.Dx()
	srcH := b.Dy()
	// The head region is the centre rectangle whose height is headRatio of
	// the source height. Width matches the aspect ratio of the destination
	// photo so the rendered crop does not stretch the face.
	destAspect := preset.WidthMm / preset.HeightMm
	if destAspect <= 0 {
		destAspect = 0.75 // ~ 3:4 fallback
	}
	h := int(float64(srcH) * headRatio)
	if h < 16 {
		h = srcH
		if h < 16 {
			h = 16
		}
	}
	w := int(float64(h) * destAspect)
	if w > srcW {
		w = srcW
		if w < 16 {
			w = 16
		}
		h = int(float64(w) / destAspect)
		if h < 16 {
			h = 16
		}
	}
	x := b.Min.X + (srcW-w)/2
	y := b.Min.Y + (srcH-h)/2
	return FaceRegion{
		X:          x,
		Y:          y,
		Width:      w,
		Height:     h,
		Confidence: 0.5,
		Manual:     false,
	}, nil
}