package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"math"
)

// RenderSheetPreview uses the same grid and fit-to-page geometry as Windows
// printing. Device-specific unprintable margins are determined at print time.
func RenderSheetPreview(ctx context.Context, body []byte, mime string, ref DocumentRef, pages, side int, paperW, paperH float64) ([]byte, error) {
	if err := validatePrintOptions(ref, pages); err != nil {
		return nil, err
	}
	selectedPages := printPages(ref, pages)
	count := (len(selectedPages) + ref.PagesPerSheet - 1) / ref.PagesPerSheet
	if side < 1 || side > count || paperW <= 0 || paperH <= 0 || math.IsNaN(paperW+paperH) || math.IsInf(paperW+paperH, 0) {
		return nil, fmt.Errorf("invalid preview sheet")
	}
	if mime == "application/pdf" {
		var err error
		body, err = documents.ReflowImages(body, ref.PaperSize, ref.Orientation)
		if err != nil {
			return nil, err
		}
	}
	load := func(page int) (image.Image, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := body
		if mime == "application/pdf" {
			var err error
			data, err = RenderPreview(ctx, body, page)
			if err != nil {
				return nil, err
			}
		}
		img, err := decodePrintImage(data)
		return img, err
	}
	first, err := load(selectedPages[0])
	if err != nil {
		return nil, err
	}
	landscape := ref.Orientation == "landscape" || ((ref.Orientation == "auto" || ref.Orientation == "") && first.Bounds().Dx() > first.Bounds().Dy())
	w, h := math.Min(paperW, paperH), math.Max(paperW, paperH)
	if landscape {
		w, h = h, w
	}
	scale := 900 / math.Max(w, h)
	width, height := max(1, int(w*scale)), max(1, int(h*scale))
	sheet := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := range sheet.Pix {
		sheet.Pix[i] = 255
	}
	cols, rows := pageGrid(ref.PagesPerSheet)
	start := (side - 1) * ref.PagesPerSheet
	for slot := 0; slot < ref.PagesPerSheet && start+slot < len(selectedPages); slot++ {
		img := first
		if start+slot != 0 {
			img, err = load(selectedPages[start+slot])
			if err != nil {
				return nil, err
			}
		}
		bounds := img.Bounds()
		x, y, dw, dh := fitPage(float64(bounds.Dx()), float64(bounds.Dy()), slot%cols*width/cols, slot/cols*height/rows, width/cols, height/rows)
		for py := 0; py < dh; py++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for px := 0; px < dw; px++ {
				c := color.NRGBAModel.Convert(img.At(bounds.Min.X+px*bounds.Dx()/dw, bounds.Min.Y+py*bounds.Dy()/dh)).(color.NRGBA)
				// Composite transparency over the physical white sheet before grayscale.
				r := uint32(c.R)*uint32(c.A)/255 + 255 - uint32(c.A)
				g := uint32(c.G)*uint32(c.A)/255 + 255 - uint32(c.A)
				b := uint32(c.B)*uint32(c.A)/255 + 255 - uint32(c.A)
				if ref.ColourMode == "monochrome" {
					v := (299*r + 587*g + 114*b) / 1000
					r, g, b = v, v, v
				}
				sheet.SetNRGBA(x+px, y+py, color.NRGBA{uint8(r), uint8(g), uint8(b), 255})
			}
		}
	}
	var out bytes.Buffer
	err = png.Encode(&out, sheet)
	return out.Bytes(), err
}
