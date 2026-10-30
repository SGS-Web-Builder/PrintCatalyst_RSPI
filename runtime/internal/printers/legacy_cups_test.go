package printers

import "testing"

func TestLegacyCUPSPaperAndPricingCapabilities(t *testing.T) {
	s := NormalizeIPPAttributes(RawAttributes{"media-supported": {"iso_a4_210x297mm", "na_letter_8.5x11in"}, "color-supported": {"false"}})
	if len(s.PaperSizes) != 2 || s.PaperSizes[0].WidthMM != 210 || s.PaperSizes[0].HeightMM != 297 || s.PaperSizes[1].WidthMM != 216 {
		t.Fatalf("dimensions: %+v", s.PaperSizes)
	}
	if len(s.ColourModes) != 1 || s.ColourModes[0] != "monochrome" || len(s.SidesModes) != 1 || s.SidesModes[0] != "one-sided" {
		t.Fatalf("legacy modes: %+v", s)
	}
}
func TestMediaDimensionsAndCollectionOnly(t *testing.T) {
	for _, label := range []string{"custom", "custom_0x297mm", "custom_999999x297mm"} {
		if w, h := mediaDimensions(label); w != 0 || h != 0 {
			t.Fatalf("guessed %s", label)
		}
	}
	s := NormalizeIPPAttributes(RawAttributes{"media-col-database": {"media-size iso_a4_210x297mm x-dimension 21000 y-dimension 29700"}})
	if len(s.PaperSizes) != 1 || s.PaperSizes[0].WidthMM != 210 {
		t.Fatalf("collection lost: %+v", s)
	}
	if len(s.ColourModes) != 0 {
		t.Fatal("invented unknown colour capabilities")
	}
}
