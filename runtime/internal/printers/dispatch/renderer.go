package dispatch

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
)

//go:embed renderer/render.py
var rendererProgram string

var rendererSlots = make(chan struct{}, 1)

// PDFRenderer runs one isolated, bounded subprocess per document. Only the Go
// wrapper controls the executable/fonts; neither is accepted from HTTP input.
type PDFRenderer struct {
	python string
	fonts  []string
}

func NewPDFRenderer() *PDFRenderer {
	return &PDFRenderer{python: "/usr/local/lib/printcatalyst-kiosk/renderer-venv/bin/python3", fonts: []string{"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf", "/usr/share/fonts/truetype/noto/NotoSansDevanagari-Regular.ttf"}}
}

type renderHeader struct {
	Paper       string   `json:"paper"`
	Colour      string   `json:"colour"`
	Orientation string   `json:"orientation"`
	MIME        string   `json:"mime"`
	Pages       []int    `json:"pages"`
	SourcePages int      `json:"source_pages"`
	Nup         int      `json:"nup"`
	Invoice     bool     `json:"invoice"`
	Logo        []byte   `json:"logo,omitempty"`
	Fonts       []string `json:"fonts"`
}
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("renderer output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func (r *PDFRenderer) RenderPrepared(ctx context.Context, body []byte, ref DocumentRef) ([]byte, prepared.Settings, error) {
	select {
	case rendererSlots <- struct{}{}:
		defer func() { <-rendererSlots }()
	case <-ctx.Done():
		return nil, prepared.Settings{}, ctx.Err()
	}
	fail := func() ([]byte, prepared.Settings, error) {
		return nil, prepared.Settings{}, errors.New("local document rendering failed; check renderer dependencies and document settings")
	}
	if len(body) == 0 || len(body) > prepared.MaxPDFBytes || len(ref.InvoiceLogo) > 512<<10 {
		return fail()
	}
	if err := validatePrintOptions(ref, ref.PageCount); err != nil {
		return nil, prepared.Settings{}, err
	}
	if ref.Invoice && (ref.Copies != 1 || ref.Sides != "one-sided" || ref.PagesPerSheet != 1 || ref.MIMEType != "text/plain") {
		return fail()
	}
	if !ref.Invoice && ref.MIMEType == documents.DocxMIME {
		var err error
		body, err = documents.ConvertDOCX(ctx, body)
		if err != nil {
			return nil, prepared.Settings{}, err
		}
		ref.MIMEType = "application/pdf"
	}
	pages, err := pageselection.Resolve(ref.Pages, ref.PageStart, ref.PageEnd, ref.PageCount)
	if err != nil {
		return nil, prepared.Settings{}, err
	}
	orientation := ref.Orientation
	if orientation == "" {
		orientation = "auto"
	}
	header, err := json.Marshal(renderHeader{Paper: ref.PaperSize, Colour: ref.ColourMode, Orientation: orientation, MIME: ref.MIMEType, Pages: pages, SourcePages: ref.PageCount, Nup: ref.PagesPerSheet, Invoice: ref.Invoice, Logo: ref.InvoiceLogo, Fonts: r.fonts})
	if err != nil {
		return fail()
	}
	if len(header) >= 1<<20 {
		return fail()
	}
	header = append(header, '\n')
	renderCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	command := exec.CommandContext(renderCtx, r.python, "-I", "-c", rendererProgram)
	command.Stdin = io.MultiReader(bytes.NewReader(header), bytes.NewReader(body))
	output := &cappedBuffer{limit: prepared.MaxPDFBytes + 4096}
	command.Stdout = output
	// Child stderr is intentionally discarded; the service exposes a generic error.
	command.Stderr = io.Discard
	if err = command.Run(); err != nil {
		return fail()
	}
	split := bytes.IndexByte(output.Bytes(), '\n')
	if split < 0 || split > 4096 {
		return fail()
	}
	var metadata struct {
		Pages     int  `json:"pages"`
		Landscape bool `json:"landscape"`
		DPI       int  `json:"dpi"`
	}
	if json.Unmarshal(output.Bytes()[:split], &metadata) != nil || metadata.Pages < 1 || metadata.Pages > 1000 || metadata.DPI != 300 {
		return fail()
	}
	pdf := output.Bytes()[split+1:]
	if len(pdf) > prepared.MaxPDFBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return fail()
	}
	if !ref.Invoice && metadata.Pages != (len(pages)+ref.PagesPerSheet-1)/ref.PagesPerSheet {
		return fail()
	}
	if ref.Invoice && metadata.Landscape {
		return fail()
	}
	if !ref.Invoice && ((orientation == "portrait" && metadata.Landscape) || (orientation == "landscape" && !metadata.Landscape)) {
		return fail()
	}
	finalOrientation := "portrait"
	if metadata.Landscape {
		finalOrientation = "landscape"
	}
	colour := ref.ColourMode
	if colour == "colour" {
		colour = "color"
	}
	settings := prepared.Settings{Paper: ref.PaperSize, Tray: ref.Tray, Colour: colour, Sides: orientationDuplex(ref.Sides, finalOrientation), Copies: ref.Copies, Pages: metadata.Pages}
	return pdf, settings, nil
}
