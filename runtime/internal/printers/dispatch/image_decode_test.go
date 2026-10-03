package dispatch

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestCameraOrientationMatchesSheetPreview(t *testing.T) {
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 30, 10)), nil); err != nil {
		t.Fatal(err)
	}
	// TIFF orientation 6 rotates the landscape sensor pixels to portrait.
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 1, 18, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0}
	app := append([]byte("Exif\x00\x00"), tiff...)
	body := []byte{255, 216, 255, 225}
	body = binary.BigEndian.AppendUint16(body, uint16(len(app)+2))
	body = append(body, app...)
	body = append(body, raw.Bytes()[2:]...)
	img, err := decodePrintImage(body)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 10 || img.Bounds().Dy() != 30 {
		t.Fatal("camera rotation ignored", img.Bounds())
	}
	ref := DocumentRef{Copies: 1, PageStart: 1, PageEnd: 1, PagesPerSheet: 1, ColourMode: "colour", Sides: "one-sided", Orientation: "auto"}
	preview, err := RenderSheetPreview(context.Background(), body, "image/jpeg", ref, 1, 1, 210, 297)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(preview))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width >= cfg.Height {
		t.Fatal("auto preview used unrotated sensor orientation")
	}
}
