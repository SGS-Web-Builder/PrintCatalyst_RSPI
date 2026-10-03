package dispatch

import (
	"bytes"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"image"
)

// Preview and physical printing must interpret camera orientation identically.
func decodePrintImage(body []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, fmt.Errorf("image exceeds the 40 megapixel print limit")
	}
	img, _, _, _, err := idcards.Decode(body)
	return img, err
}
