package dispatch

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestSheetPreviewReflectsPrintSettings(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 80, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 80; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 230, G: 30, B: 60, A: 255})
		}
	}
	var body bytes.Buffer
	if err := png.Encode(&body, source); err != nil {
		t.Fatal(err)
	}
	ref := DocumentRef{Copies: 1, PageStart: 1, PageEnd: 1, PagesPerSheet: 1, Orientation: "portrait", ColourMode: "colour", Sides: "one-sided"}
	render := func() image.Image {
		t.Helper()
		raw, err := RenderSheetPreview(context.Background(), body.Bytes(), "image/png", ref, 1, 1, 210, 297)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	portrait := render()
	if portrait.Bounds().Dx() >= portrait.Bounds().Dy() {
		t.Fatal("portrait sheet was not portrait")
	}
	r, g, _, _ := portrait.At(portrait.Bounds().Dx()/2, portrait.Bounds().Dy()/2).RGBA()
	if r == g {
		t.Fatal("colour was lost")
	}
	r, g, b, _ := portrait.At(0, 0).RGBA()
	if r != 65535 || g != r || b != r {
		t.Fatal("fit-to-page should preserve white space")
	}
	ref.Orientation = "landscape"
	landscape := render()
	if landscape.Bounds().Dx() <= landscape.Bounds().Dy() {
		t.Fatal("landscape sheet was not landscape")
	}
	ref.Orientation = "auto"
	automatic := render()
	if automatic.Bounds() != landscape.Bounds() {
		t.Fatal("auto did not use source aspect ratio")
	}
	ref.ColourMode = "monochrome"
	mono := render()
	for y := 0; y < mono.Bounds().Dy(); y++ {
		for x := 0; x < mono.Bounds().Dx(); x++ {
			r, g, b, _ := mono.At(x, y).RGBA()
			if r != g || r != b {
				t.Fatal("monochrome preview contains colour")
			}
		}
	}
	ref.PagesPerSheet = 4
	multi := render()
	r, g, b, _ = multi.At(multi.Bounds().Dx()*3/4, multi.Bounds().Dy()*3/4).RGBA()
	if r != 65535 || g != r || b != r {
		t.Fatal("unfilled N-up slot should be blank")
	}
	if _, err := RenderSheetPreview(context.Background(), body.Bytes(), "image/png", ref, 1, 2, 210, 297); err == nil {
		t.Fatal("accepted nonexistent side")
	}
	ref.Orientation = "invalid"
	if _, err := RenderSheetPreview(context.Background(), body.Bytes(), "image/png", ref, 1, 1, 210, 297); err == nil {
		t.Fatal("accepted invalid orientation")
	}
}
