package idcards

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
)

// Decode reads a JPEG or PNG byte slice and returns the image, the decoded
// dimensions after EXIF orientation is applied, and the original EXIF
// orientation value (1-8) so callers can surface it in UI.
//
// PNG cannot carry an EXIF orientation; the function returns OrientationNormal
// for PNG inputs. JPEG is the only path that consults the EXIF segment.
//
// Decode re-encodes the image with the EXIF orientation baked into the
// pixels (the returned image is the post-rotation pixels). This means
// downstream code never has to remember the orientation to interpret the
// pixel grid correctly.
func Decode(body []byte) (image.Image, int, int, Orientation, error) {
	if len(body) < 4 {
		return nil, 0, 0, OrientationNormal, errors.New("image body is empty")
	}
	// JPEG starts with FF D8.
	if body[0] == 0xFF && body[1] == 0xD8 {
		return decodeJPEG(body)
	}
	// PNG starts with the 8-byte signature.
	if bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return decodePNG(body)
	}
	return nil, 0, 0, OrientationNormal, ErrUnsupportedImageFormat
}

func decodeJPEG(body []byte) (image.Image, int, int, Orientation, error) {
	orientation := exifOrientation(body)
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, orientation, err
	}
	rotated := applyOrientationToImage(img, orientation)
	rotatedBounds := rotated.Bounds()
	return rotated, rotatedBounds.Dx(), rotatedBounds.Dy(), orientation, nil
}

func decodePNG(body []byte) (image.Image, int, int, Orientation, error) {
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, OrientationNormal, err
	}
	bounds := img.Bounds()
	return img, bounds.Dx(), bounds.Dy(), OrientationNormal, nil
}

// applyOrientationToImage returns a new image with the EXIF orientation
// applied. Orientations 2-8 are produced by composing flip and rotate
// operations. For each orientation we choose the smallest operation count:
//
//   1 = identity
//   2 = horizontal flip
//   3 = 180° rotation
//   4 = vertical flip
//   5 = transpose (mirror across y=x)
//   6 = 90° clockwise rotation
//   7 = transverse (mirror across y=-x)
//   8 = 270° clockwise rotation
//
// The implementation uses stdlib only (image/draw is not imported here to
// avoid pulling the full draw package into this hot path).
func applyOrientationToImage(src image.Image, o Orientation) image.Image {
	if o == OrientationNormal {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	flipped := o == OrientationMirrorHorizontal || o == OrientationMirrorVertical
	swapped := o == OrientationMirrorTranspose || o == OrientationRotate90 ||
		o == OrientationMirrorTransverse || o == OrientationRotate270
	dstW, dstH := w, h
	if swapped {
		dstW, dstH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := x + b.Min.X
			sy := y + b.Min.Y
			r, g, bb, a := src.At(sx, sy).RGBA()
			var dx, dy int
			switch o {
			case OrientationMirrorHorizontal:
				dx, dy = w-1-x, y
			case OrientationMirrorVertical:
				dx, dy = x, h-1-y
			case OrientationRotate180:
				dx, dy = w-1-x, h-1-y
			case OrientationMirrorTranspose:
				dx, dy = y, x
			case OrientationRotate90:
				dx, dy = h-1-y, x
			case OrientationMirrorTransverse:
				dx, dy = y, h-1-x
			case OrientationRotate270:
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			_ = flipped // already encoded above; suppress unused warning in builds
			c := color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bb >> 8), A: uint8(a >> 8)}
			dst.Set(dx, dy, c)
		}
	}
	return dst
}

// EncodePNG writes the image as a PNG with no filtering optimisation. The
// caller is responsible for picking the destination size; we never resize
// here because the ID Card Studio only re-encodes the perspective-corrected
// crop at its true pixel dimensions.
func EncodePNG(w io.Writer, img image.Image) error {
	return png.Encode(w, img)
}

// tinyUint32 is exposed for tests that build minimal JPEG fixtures.
func tinyUint32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// tinyUint16 is exposed for tests that build minimal JPEG fixtures.
func tinyUint16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}
