package idcards

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Orientation represents the EXIF orientation tag value (1-8). The numeric
// values match the EXIF 2.3 specification verbatim so callers can compare
// against raw metadata dumps without an indirection table.
type Orientation int

const (
	OrientationNormal          Orientation = 1
	OrientationMirrorHorizontal Orientation = 2
	OrientationRotate180       Orientation = 3
	OrientationMirrorVertical  Orientation = 4
	OrientationMirrorTranspose Orientation = 5
	OrientationRotate90        Orientation = 6
	OrientationMirrorTransverse Orientation = 7
	OrientationRotate270       Orientation = 8
)

// IsValid reports whether the orientation is in the 1-8 range defined by the
// EXIF 2.3 spec. Anything else (zero, >8) is treated as "no rotation
// applied" by ApplyOrientation below.
func (o Orientation) IsValid() bool {
	return o >= 1 && o <= 8
}

// ApplyOrientation produces the dimensions of an image after applying the
// EXIF orientation transform. EXIF orientations 5-8 swap width and height
// because the captured image is rotated 90/270 degrees.
func ApplyOrientation(width, height int, o Orientation) (int, int) {
	switch o {
	case OrientationRotate90, OrientationRotate270,
		OrientationMirrorTranspose, OrientationMirrorTransverse:
		return height, width
	default:
		return width, height
	}
}

// exifOrientation reads the EXIF Orientation tag from a JPEG byte slice.
// It returns OrientationNormal when the slice is not a JPEG, has no EXIF
// segment, or the orientation tag is missing/malformed. A valid orientation
// in the 1-8 range is returned when found.
//
// The implementation intentionally parses only the fields it needs:
// JPEG SOI, the APP1/Exif marker, the TIFF header, IFD0, and the orientation
// tag (0x0112, type SHORT, count 1). This keeps the parser under 100 lines
// while handling every real-world EXIF variant we have encountered.
//
// References:
//   - EXIF 2.3 specification, section 4.6.4 (Orientation)
//   - TIFF 6.0 specification, sections 2 (TIFF header) and 3 (IFD entries)
func exifOrientation(jpeg []byte) Orientation {
	if len(jpeg) < 4 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		return OrientationNormal
	}
	// Walk APPn markers until we hit APP1 (EXIF) or another non-APP marker.
	offset := 2
	for offset+4 <= len(jpeg) && jpeg[offset] == 0xFF {
		marker := jpeg[offset+1]
		// APP markers are 0xFFE0..0xFFEF. APP1 is 0xFFE1.
		if marker < 0xE0 || marker > 0xEF {
			// Not an APP marker; EXIF is always in APP1 if present.
			return OrientationNormal
		}
		segLen := int(binary.BigEndian.Uint16(jpeg[offset+2 : offset+4]))
		if segLen < 2 || offset+2+segLen > len(jpeg) {
			return OrientationNormal
		}
		segBody := jpeg[offset+4 : offset+2+segLen]
		if marker == 0xE1 && len(segBody) >= 6 && bytes.HasPrefix(segBody, []byte("Exif\x00\x00")) {
			tiffStart := 6
			return parseTIFFOrientation(segBody[tiffStart:])
		}
		// Step past this segment.
		offset += 2 + segLen
	}
	return OrientationNormal
}

func parseTIFFOrientation(tiff []byte) Orientation {
	if len(tiff) < 8 {
		return OrientationNormal
	}
	var byteOrder binary.ByteOrder
	switch {
	case bytes.HasPrefix(tiff, []byte("II")):
		byteOrder = binary.LittleEndian
	case bytes.HasPrefix(tiff, []byte("MM")):
		byteOrder = binary.BigEndian
	default:
		return OrientationNormal
	}
	ifdOffset := int(byteOrder.Uint32(tiff[4:8]))
	return readOrientationFromIFD(tiff, byteOrder, ifdOffset)
}

func readOrientationFromIFD(tiff []byte, byteOrder binary.ByteOrder, ifdOffset int) Orientation {
	if ifdOffset < 0 || ifdOffset+2 > len(tiff) {
		return OrientationNormal
	}
	entryCount := int(byteOrder.Uint16(tiff[ifdOffset : ifdOffset+2]))
	if ifdOffset+2+entryCount*12 > len(tiff) {
		// Some files have stray bytes; do not overread.
		entryCount = (len(tiff) - ifdOffset - 2) / 12
	}
	for i := 0; i < entryCount; i++ {
		base := ifdOffset + 2 + i*12
		tag := byteOrder.Uint16(tiff[base : base+2])
		if tag != 0x0112 {
			continue
		}
		// Type field is 2 bytes; we only care about SHORT (type 3) per EXIF.
		if byteOrder.Uint16(tiff[base+2:base+4]) != 3 {
			continue
		}
		count := byteOrder.Uint32(tiff[base+4 : base+8])
		if count != 1 {
			continue
		}
		// Value fits in the 4-byte value/offset field for SHORT.
		value := byteOrder.Uint16(tiff[base+8 : base+10])
		o := Orientation(value)
		if !o.IsValid() {
			return OrientationNormal
		}
		return o
	}
	return OrientationNormal
}

// ErrUnsupportedImageFormat is returned by Decode when the input is neither a
// JPEG nor a PNG. Both formats are supported by the ID Card Studio because
// the customer's upload route accepts them.
var ErrUnsupportedImageFormat = errors.New("image is not a JPEG or PNG")
