package idcards

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

// buildJPEGWithOrientation synthesises a minimal JPEG with an EXIF segment
// declaring the given orientation value. The pixel data is intentionally
// trivial; the test only cares that the EXIF parser reads the orientation.
func buildJPEGWithOrientation(t *testing.T, o Orientation) []byte {
	t.Helper()
	body := []byte{0xFF, 0xD8} // SOI
	// APP1 length includes the length field itself (2 bytes) and the Exif\0\0
	// prefix (6 bytes) and the TIFF payload.
	tiffPayload := buildTIFFOrientation(o)
	app1Body := append([]byte("Exif\x00\x00"), tiffPayload...)
	segLen := uint16(len(app1Body) + 2)
	body = append(body, 0xFF, 0xE1)
	body = binary.BigEndian.AppendUint16(body, segLen)
	body = append(body, app1Body...)
	body = append(body, 0xFF, 0xD9) // EOI
	return body
}

func buildTIFFOrientation(o Orientation) []byte {
	// Big-endian TIFF: header (8 bytes) + IFD0 with 1 entry (12 bytes) +
	// next-IFD pointer (4 bytes). Big-endian is simpler to construct in the
	// test fixture because binary.BigEndian helpers are stdlib.
	buf := &bytes.Buffer{}
	buf.WriteString("MM")
	buf.Write(tinyUint16(42))
	buf.Write(tinyUint32(8)) // IFD0 offset
	// IFD0: 1 entry
	buf.Write(tinyUint16(1))
	// Entry: tag=0x0112, type=SHORT (3), count=1, value=uint16
	buf.Write(tinyUint16(0x0112))
	buf.Write(tinyUint16(3))
	buf.Write(tinyUint32(1))
	buf.Write(tinyUint16(uint16(o)))
	buf.Write(tinyUint16(0)) // padding to 4-byte boundary
	// Next IFD offset = 0 (no IFD1)
	buf.Write(tinyUint32(0))
	return buf.Bytes()
}

func TestExifOrientationReadsAllValidValues(t *testing.T) {
	for o := Orientation(1); o <= 8; o++ {
		body := buildJPEGWithOrientation(t, o)
		got := exifOrientation(body)
		if got != o {
			t.Fatalf("orientation = %d, want %d", got, o)
		}
	}
}

func TestExifOrientationDefaultsToNormalForNonJPEG(t *testing.T) {
	if got := exifOrientation([]byte("not a jpeg")); got != OrientationNormal {
		t.Fatalf("non-JPEG orientation = %d", got)
	}
	if got := exifOrientation(nil); got != OrientationNormal {
		t.Fatalf("nil orientation = %d", got)
	}
}

func TestExifOrientationDefaultsToNormalWhenAbsent(t *testing.T) {
	body := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	if got := exifOrientation(body); got != OrientationNormal {
		t.Fatalf("orientation = %d", got)
	}
}

func TestApplyOrientationSwapsForRotate90(t *testing.T) {
	w, h := ApplyOrientation(640, 480, OrientationRotate90)
	if w != 480 || h != 640 {
		t.Fatalf("rotate90 dims = %dx%d", w, h)
	}
	w, h = ApplyOrientation(640, 480, OrientationRotate180)
	if w != 640 || h != 480 {
		t.Fatalf("rotate180 dims = %dx%d", w, h)
	}
	w, h = ApplyOrientation(640, 480, OrientationMirrorHorizontal)
	if w != 640 || h != 480 {
		t.Fatalf("mirror dims = %dx%d", w, h)
	}
}

func TestDecodePNGReturnsOrientationNormal(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := EncodePNG(&buf, img); err != nil {
		t.Fatal(err)
	}
	gotImg, w, h, o, err := Decode(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if o != OrientationNormal {
		t.Fatalf("orientation = %d", o)
	}
	if w != 10 || h != 10 {
		t.Fatalf("dims = %dx%d", w, h)
	}
	if gotImg.Bounds().Dx() != 10 {
		t.Fatalf("image width = %d", gotImg.Bounds().Dx())
	}
}

func TestDecodeRejectsUnknownFormat(t *testing.T) {
	_, _, _, _, err := Decode([]byte("definitely not an image"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}
