package passport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

// ComposedPhoto is one rendered passport photo at the destination's pixel
// dimensions. The composer returns it to the sheet layer so the per-photo
// image can be cached and reused when previewing alternative backgrounds.
type ComposedPhoto struct {
	PixelWidth  int
	PixelHeight int
	Image       *image.RGBA
}

// Compose renders a single passport photo at the destination's pixel
// dimensions. The face crop is scaled to the photo rectangle while
// preserving the destination aspect ratio; any portion of the source image
// outside the face region is replaced with the chosen background colour so
// the rendered photo matches the country preset.
//
// The background colour is applied as a solid fill before the face crop is
// drawn; this is a deliberate simplification for the phase 6 slice. A
// future slice will add a refinement brush that lets the merchant retouch
// the edge mask (the master spec requires "background removal/refinement"
// but does not prescribe an algorithm).
//
// Scaling is performed with nearest-neighbour sampling so the printed
// pixels are an integer multiple of the source pixels: the function never
// smooths over a misaligned face, and the merchant sees exactly the
// pixels the printer will reproduce.
func ComposePhoto(src image.Image, face FaceRegion, preset PresetSpec, background BackgroundKind, dpi int) (ComposedPhoto, error) {
	if src == nil {
		return ComposedPhoto{}, ErrNoDocument
	}
	if dpi <= 0 {
		dpi = 300
	}
	bgColor, err := BackgroundColor(background)
	if err != nil {
		return ComposedPhoto{}, err
	}
	pxPerMm := float64(dpi) / 25.4
	pixelW := int(preset.WidthMm * pxPerMm)
	pixelH := int(preset.HeightMm * pxPerMm)
	if pixelW < 16 {
		pixelW = 16
	}
	if pixelH < 16 {
		pixelH = 16
	}
	dst := image.NewRGBA(image.Rect(0, 0, pixelW, pixelH))
	if background != BackgroundKeepOriginal {
		draw.Draw(dst, dst.Bounds(), &image.Uniform{C: bgColor}, image.Point{}, draw.Src)
	} else {
		// Keep-original: paint the source face crop stretched to fill the
		// destination. This is rarely useful for passport photos but the
		// merchant can still opt in.
		srcBounds := src.Bounds()
		if !srcBounds.Empty() {
			scaleNearest(src, srcBounds, dst, dst.Bounds())
		}
	}
	// Extract the face crop from the source image.
	faceRect := intersectRect(face.Bounds(), src.Bounds())
	if !faceRect.Empty() {
		crop := cropImage(src, faceRect)
		if crop != nil {
			// Place the face crop so the eye line aligns with the
			// destination's eye-line position. The crop is scaled to
			// honour the destination aspect ratio.
			eyeLineFromBottomPx := int(preset.EyeLineFromBottomMm * pxPerMm)
			headHeightPx := int(preset.HeadHeightMm * pxPerMm)
			if headHeightPx < 16 {
				headHeightPx = pixelH
			}
			cropW := int(float64(headHeightPx) * (preset.WidthMm / preset.HeightMm))
			if cropW > pixelW {
				cropW = pixelW
			}
			cropH := headHeightPx
			if cropH > pixelH {
				cropH = pixelH
			}
			cropX := (pixelW - cropW) / 2
			cropY := pixelH - eyeLineFromBottomPx - cropH/2
			if cropY < 0 {
				cropY = 0
			}
			if cropY+cropH > pixelH {
				cropY = pixelH - cropH
			}
			dstRect := image.Rect(cropX, cropY, cropX+cropW, cropY+cropH)
			scaleNearest(crop, crop.Bounds(), dst, dstRect)
		}
	}
	return ComposedPhoto{
		PixelWidth:  pixelW,
		PixelHeight: pixelH,
		Image:       dst,
	}, nil
}

// ComposeSheet renders a full printable sheet: the photo is composed once
// and tiled across every slot in the sheet layout. The output PNG is
// written to disk and the resulting storage path + SHA-256 are returned so
// the caller can persist the output row.
func ComposeSheet(ctx context.Context, composed ComposedPhoto, layout SheetLayout, files *localfiles.Files, now func() time.Time, sessionID string) (Output, error) {
	if composed.Image == nil {
		return Output{}, ErrCompose
	}
	if files == nil {
		return Output{}, fmt.Errorf("%w: missing file store", ErrCompose)
	}
	if now == nil {
		now = time.Now
	}
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	w, h := layout.PixelSize()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.NRGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, draw.Src)
	for i := 0; i < layout.PhotoCount; i++ {
		rect := layout.PhotoRect(i)
		if rect.Empty() {
			continue
		}
		draw.Draw(dst, rect, composed.Image, composed.Image.Bounds().Min, draw.Src)
	}
	relDir := filepath.Join("passports", sessionID)
	if err := files.MkdirAll(relDir); err != nil {
		return Output{}, err
	}
	filename := fmt.Sprintf("sheet-%d.png", now().UnixNano())
	rel := filepath.Join(relDir, filename)
	full := filepath.Join(files.DataRoot(), rel)
	tmp := full + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return Output{}, err
	}
	if err := png.Encode(f, dst); err != nil {
		f.Close()
		os.Remove(tmp)
		return Output{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Output{}, err
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return Output{}, err
	}
	// Hash the rendered PNG for audit.
	hash, err := sha256File(full)
	if err != nil {
		return Output{}, err
	}
	return Output{
		ID:            newID(now),
		SessionID:     sessionID,
		Side:          SideSingle,
		SheetWidthMm:  layout.SheetWMm,
		SheetHeightMm: layout.SheetHMm,
		PixelWidth:    w,
		PixelHeight:   h,
		StoragePath:   rel,
		SHA256:        hash,
		PhotoCount:    layout.PhotoCount,
		CreatedAt:     now().Unix(),
	}, nil
}

// ComposeGuideOverlay draws the head-rectangle and eye-line guides on top
// of the rendered photo. The function mutates the destination image in
// place; the master spec asks for "head/eye guides" so the merchant can
// preview the layout. A darker shade is used for the eye line so it stands
// out against the light head-rectangle outline.
func ComposeGuideOverlay(dst *image.RGBA, preset PresetSpec, dpi int) {
	if dst == nil || preset.HeightMm <= 0 {
		return
	}
	pxPerMm := float64(dpi) / 25.4
	w := dst.Bounds().Dx()
	h := dst.Bounds().Dy()
	if w == 0 || h == 0 {
		return
	}
	headBottomY := h - int(preset.EyeLineFromBottomMm*pxPerMm)
	if headBottomY < 0 {
		headBottomY = 0
	}
	headTopY := headBottomY - int(preset.HeadHeightMm*pxPerMm)
	if headTopY < 0 {
		headTopY = 0
	}
	rectW := int(float64(w) * 0.8)
	if rectW < 4 {
		rectW = 4
	}
	rectX0 := (w - rectW) / 2
	rectX1 := rectX0 + rectW
	outline := color.NRGBA{R: 220, G: 60, B: 60, A: 255}
	drawRectOutline(dst, image.Rect(rectX0, headTopY, rectX1, headBottomY), outline)
	eyeY := h - int(preset.EyeLineFromBottomMm*pxPerMm)
	if eyeY < 0 {
		eyeY = 0
	}
	if eyeY >= h {
		eyeY = h - 1
	}
	eyeLine := color.NRGBA{R: 60, G: 80, B: 220, A: 255}
	for x := rectX0; x < rectX1; x++ {
		dst.Set(x, eyeY, eyeLine)
	}
}

// intersectRect returns the intersection of two rectangles, or an empty
// rectangle when they do not overlap.
func intersectRect(a, b image.Rectangle) image.Rectangle {
	if a.Empty() || b.Empty() {
		return image.Rectangle{}
	}
	return a.Intersect(b)
}

// cropImage returns the sub-image of src bounded by r. The result shares the
// underlying pixel buffer when the sub-image is contiguous, so we copy into
// a fresh RGBA to keep the rendered photo independent of the source.
func cropImage(src image.Image, r image.Rectangle) image.Image {
	if r.Empty() {
		return nil
	}
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), src, r.Min, draw.Src)
	return out
}

// scaleNearest copies src onto dst using nearest-neighbour sampling. Both
// rectangles are inclusive of Min and exclusive of Max.
func scaleNearest(src image.Image, srcRect image.Rectangle, dst *image.RGBA, dstRect image.Rectangle) {
	if srcRect.Empty() || dstRect.Empty() {
		return
	}
	srcW := srcRect.Dx()
	srcH := srcRect.Dy()
	dstW := dstRect.Dx()
	dstH := dstRect.Dy()
	for y := 0; y < dstH; y++ {
		sy := srcRect.Min.Y + (y*srcH)/dstH
		if sy >= srcRect.Max.Y {
			sy = srcRect.Max.Y - 1
		}
		for x := 0; x < dstW; x++ {
			sx := srcRect.Min.X + (x*srcW)/dstW
			if sx >= srcRect.Max.X {
				sx = srcRect.Max.X - 1
			}
			r, g, b, a := src.At(sx, sy).RGBA()
			dst.Set(dstRect.Min.X+x, dstRect.Min.Y+y, color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)})
		}
	}
}

func drawRectOutline(dst *image.RGBA, r image.Rectangle, c color.NRGBA) {
	if r.Empty() {
		return
	}
	for x := r.Min.X; x < r.Max.X; x++ {
		if r.Min.Y >= 0 && r.Min.Y < dst.Bounds().Dy() {
			dst.Set(x, r.Min.Y, c)
		}
		if r.Max.Y-1 >= 0 && r.Max.Y-1 < dst.Bounds().Dy() {
			dst.Set(x, r.Max.Y-1, c)
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if r.Min.X >= 0 && r.Min.X < dst.Bounds().Dx() {
			dst.Set(r.Min.X, y, c)
		}
		if r.Max.X-1 >= 0 && r.Max.X-1 < dst.Bounds().Dx() {
			dst.Set(r.Max.X-1, y, c)
		}
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// newID is a small helper that returns a stable hex identifier based on the
// supplied clock. Tests inject a fixed clock so the generated ids are
// deterministic; production code uses time.Now.
func newID(now func() time.Time) string {
	return strings.ReplaceAll(fmt.Sprintf("%x", now().UnixNano()), "0", "")
}