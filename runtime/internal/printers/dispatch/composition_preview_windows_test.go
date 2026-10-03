//go:build windows

package dispatch

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
)

func TestComposedImagesSheetPreview(t *testing.T) {
	exe, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "pdfium.dll")); err != nil {
		t.Skip("run compiled test beside bundled PDFium")
	}
	sources := [][]byte{}
	for _, c := range []color.RGBA{{255, 0, 0, 255}, {0, 0, 255, 255}} {
		img := image.NewRGBA(image.Rect(0, 0, 80, 60))
		for y := 0; y < 60; y++ {
			for x := 0; x < 80; x++ {
				img.Set(x, y, c)
			}
		}
		var out bytes.Buffer
		png.Encode(&out, img)
		sources = append(sources, out.Bytes())
	}
	for _, n := range []int{1, 2} {
		pdf, err := documents.ComposeImages(sources, n, "A4", "landscape", 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, orientation := range []string{"portrait", "landscape"} {
			for _, mode := range []string{"colour", "monochrome"} {
				data, err := RenderSheetPreview(context.Background(), pdf, "application/pdf", DocumentRef{Pages: []int{1}, Copies: 1, PageStart: 1, PageEnd: 1, PagesPerSheet: 1, Orientation: orientation, ColourMode: mode, Sides: "one-sided"}, (2+n-1)/n, 1, 210, 297)
				if err != nil {
					t.Fatal(err)
				}
				img, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if (img.Bounds().Dx() > img.Bounds().Dy()) != (orientation == "landscape") {
					t.Fatal("preview does not match selected orientation")
				}
				if n == 2 && mode == "colour" {
					var rx, ry, bx, by, rc, bc int64
					for y := 0; y < img.Bounds().Dy(); y++ {
						for x := 0; x < img.Bounds().Dx(); x++ {
							r, g, b, _ := img.At(x, y).RGBA()
							if r > 40000 && g < 15000 && b < 15000 {
								rx += int64(x)
								ry += int64(y)
								rc++
							}
							if b > 40000 && r < 15000 && g < 15000 {
								bx += int64(x)
								by += int64(y)
								bc++
							}
						}
					}
					if rc == 0 || bc == 0 {
						t.Fatal("both merged images must remain visible")
					}
					dx, dy := rx/rc-bx/bc, ry/rc-by/bc
					if dx < 0 {
						dx = -dx
					}
					if dy < 0 {
						dy = -dy
					}
					if orientation == "landscape" && dx <= dy {
						t.Fatal("landscape must arrange images across the page")
					}
					if orientation == "portrait" && dy <= dx {
						t.Fatal("portrait must arrange images down the page")
					}
				}
				ink := 0
				for y := 0; y < img.Bounds().Dy(); y++ {
					for x := 0; x < img.Bounds().Dx(); x++ {
						r, g, b, _ := img.At(x, y).RGBA()
						if r < 50000 || g < 50000 || b < 50000 {
							ink++
						}
						if mode == "monochrome" && (r != g || g != b) {
							t.Fatal("monochrome preview contains colour")
						}
					}
				}
				if ink < 100 {
					t.Fatal("merged preview is blank")
				}
			}
		}
	}
}
