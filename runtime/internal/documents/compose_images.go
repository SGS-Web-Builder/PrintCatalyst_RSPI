package documents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"math"
	"strings"
)

// ComposeImages embeds photographs in a PDF with fit-within-cell placement.
// One image per page is a merge; larger grids are photo collages. No crop occurs.
func ComposeImages(sources [][]byte, perPage int, paper, orientation string, split int) ([]byte, error) {
	grids := map[int][2]int{1: {1, 1}, 2: {1, 2}, 4: {2, 2}, 6: {2, 3}, 9: {3, 3}, 12: {3, 4}, 16: {4, 4}}
	grid, ok := grids[perPage]
	if !ok || len(sources) < 2 || len(sources) > 10 {
		return nil, fmt.Errorf("select 2–10 images and a supported layout")
	}
	sizes := map[string][2]float64{"A4": {595.28, 841.89}, "A3": {841.89, 1190.55}, "A5": {419.53, 595.28}, "LETTER": {612, 792}, "LEGAL": {612, 1008}}
	size, ok := sizes[strings.ToUpper(paper)]
	if !ok {
		return nil, fmt.Errorf("unsupported collage paper size")
	}
	if orientation != "portrait" && orientation != "landscape" {
		return nil, fmt.Errorf("choose portrait or landscape")
	}
	if split < 20 || split > 80 {
		return nil, fmt.Errorf("two-photo split must be 20–80 percent")
	}
	if orientation == "landscape" {
		size[0], size[1] = size[1], size[0]
		grid[0], grid[1] = grid[1], grid[0]
	}
	objects := [][]byte{nil, nil}
	add := func(body []byte) int { objects = append(objects, body); return len(objects) }
	stream := func(header string, body []byte) []byte {
		return append(append([]byte(fmt.Sprintf("<< %s /Length %d >>\nstream\n", header, len(body))), body...), []byte("\nendstream")...)
	}
	var kids bytes.Buffer
	for start := 0; start < len(sources); start += perPage {
		var commands, resources bytes.Buffer
		for index := start; index < len(sources) && index < start+perPage; index++ {
			cfg, _, err := image.DecodeConfig(bytes.NewReader(sources[index]))
			if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 25000000 {
				return nil, fmt.Errorf("image is invalid or larger than 25 megapixels")
			}
			img, width, height, _, err := idcards.Decode(sources[index])
			if err != nil {
				return nil, fmt.Errorf("could not decode image")
			}
			cfg.Width, cfg.Height = width, height
			// Composite transparency onto paper white before JPEG encoding.
			rgb := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
			draw.Draw(rgb, rgb.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
			draw.Draw(rgb, rgb.Bounds(), img, img.Bounds().Min, draw.Over)
			var encoded bytes.Buffer
			if err = jpeg.Encode(&encoded, rgb, &jpeg.Options{Quality: 95}); err != nil {
				return nil, err
			}
			id := add(stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", cfg.Width, cfg.Height), encoded.Bytes()))
			slot := index - start
			fmt.Fprintf(&resources, "/Im%d %d 0 R ", slot, id)
			const margin = 18.0
			const gap = 8.0
			cw := (size[0] - 2*margin - float64(grid[0]-1)*gap) / float64(grid[0])
			ch := (size[1] - 2*margin - float64(grid[1]-1)*gap) / float64(grid[1])
			x := margin + float64(slot%grid[0])*(cw+gap)
			top := margin + float64(slot/grid[0])*(ch+gap)
			if perPage == 2 {
				fraction := float64(split) / 100
				if orientation == "portrait" {
					available := size[1] - 2*margin - gap
					ch = available * fraction
					if slot == 1 {
						top = margin + ch + gap
						ch = available * (1 - fraction)
					}
				} else {
					available := size[0] - 2*margin - gap
					cw = available * fraction
					if slot == 1 {
						x = margin + cw + gap
						cw = available * (1 - fraction)
					}
				}
			}
			scale := math.Min(cw/float64(cfg.Width), ch/float64(cfg.Height))
			w, h := float64(cfg.Width)*scale, float64(cfg.Height)*scale
			fmt.Fprintf(&commands, "q %.4f 0 0 %.4f %.4f %.4f cm /Im%d Do Q\n", w, h, x+(cw-w)/2, size[1]-top-ch+(ch-h)/2, slot)
		}
		content := add(stream("", commands.Bytes()))
		page := add([]byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.4f %.4f] /Resources << /XObject << %s >> >> /Contents %d 0 R >>", size[0], size[1], resources.String(), content)))
		fmt.Fprintf(&kids, "%d 0 R ", page)
	}
	objects[0] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	objects[1] = []byte(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", (len(sources)+perPage-1)/perPage, kids.String()))
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	recipe := imageRecipe{Paper: paper, Orientation: orientation, PerPage: perPage, Split: split}
	for i, obj := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		if bytes.Contains(obj, []byte("/Subtype /Image")) {
			start := bytes.Index(obj, []byte("stream\n")) + len("stream\n")
			end := bytes.LastIndex(obj, []byte("\nendstream"))
			recipe.Images = append(recipe.Images, imageRange{Offset: out.Len() + start, Length: end - start})
		}
		out.Write(obj)
		out.WriteString("\nendobj\n")
	}
	metadata, _ := json.Marshal(recipe)
	out.WriteString("%PrintCatalystImages ")
	out.Write(metadata)
	out.WriteByte('\n')
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	if out.Len() > MaxFileSize {
		return nil, fmt.Errorf("combined document exceeds 50 MB; select fewer or smaller images")
	}
	return out.Bytes(), nil
}
