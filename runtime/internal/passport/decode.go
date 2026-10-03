package passport

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
)

// ErrUnsupportedImageFormat is returned when the source blob is neither a
// JPEG nor a PNG. The studio accepts the same baseline formats as the
// document upload flow so the merchant does not have to maintain a parallel
// conversion path.
var ErrUnsupportedImageFormat = errors.New("passport: unsupported image format")

// Decode reads a JPEG or PNG byte slice and returns the decoded image. The
// function does not apply EXIF orientation correction: passport photos are
// almost always uploaded already oriented, and a future slice that needs
// EXIF can reuse the idcards decoder.
func Decode(body []byte) (image.Image, error) {
	if len(body) < 4 {
		return nil, ErrUnsupportedImageFormat
	}
	// JPEG starts with FF D8.
	if body[0] == 0xFF && body[1] == 0xD8 {
		img, err := jpeg.Decode(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		return img, nil
	}
	// PNG starts with the 8-byte signature.
	if bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		img, err := png.Decode(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		return img, nil
	}
	return nil, ErrUnsupportedImageFormat
}