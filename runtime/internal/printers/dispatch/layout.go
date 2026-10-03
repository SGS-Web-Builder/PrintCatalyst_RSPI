package dispatch

import (
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"math"
)

func validatePrintOptions(r DocumentRef, pages int) error {
	if _, err := pageselection.Resolve(r.Pages, r.PageStart, r.PageEnd, pages); err != nil {
		return err
	}
	if r.Copies < 1 || r.Copies > 100 || r.PageStart < 1 || r.PageEnd < r.PageStart || r.PageEnd > pages {
		return fmt.Errorf("invalid copies or page range")
	}
	if r.PagesPerSheet != 1 && r.PagesPerSheet != 2 && r.PagesPerSheet != 4 {
		return fmt.Errorf("pages per sheet must be 1, 2 or 4")
	}
	if r.ColourMode != "monochrome" && r.ColourMode != "colour" {
		return fmt.Errorf("invalid colour mode")
	}
	if r.Sides != "one-sided" && r.Sides != "two-sided-long-edge" && r.Sides != "two-sided-short-edge" {
		return fmt.Errorf("invalid sides mode")
	}
	if r.Orientation != "" && r.Orientation != "auto" && r.Orientation != "portrait" && r.Orientation != "landscape" {
		return fmt.Errorf("invalid orientation")
	}
	return nil
}
func pageGrid(n int) (int, int) {
	if n == 2 {
		return 1, 2
	}
	if n == 4 {
		return 2, 2
	}
	return 1, 1
}
func fitPage(sw, sh float64, x, y, w, h int) (int, int, int, int) {
	if sw <= 0 || sh <= 0 || math.IsNaN(sw) || math.IsNaN(sh) || math.IsInf(sw, 0) || math.IsInf(sh, 0) {
		return x, y, w, h
	}
	scale := math.Min(float64(w)/sw, float64(h)/sh)
	dw, dh := max(1, int(sw*scale)), max(1, int(sh*scale))
	return x + (w-dw)/2, y + (h-dh)/2, dw, dh
}

func printPages(r DocumentRef, total int) []int {
	pages, _ := pageselection.Resolve(r.Pages, r.PageStart, r.PageEnd, total)
	return pages
}

// Back-to-back documents turn like a book in the final sheet orientation.
func orientationDuplex(sides, orientation string) string {
	if sides == "one-sided" {
		return sides
	}
	if orientation == "landscape" {
		return "two-sided-short-edge"
	}
	return "two-sided-long-edge"
}
