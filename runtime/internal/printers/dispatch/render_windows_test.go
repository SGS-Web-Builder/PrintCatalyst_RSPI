//go:build windows

package dispatch

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"
)

// Run a compiled test executable beside the bundled DLL. This renders into an
// offscreen DIB and never opens or submits to a physical printer.
func TestBundledPDFiumRendersTestPage(t *testing.T) {
	exe, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "pdfium.dll")); err != nil {
		t.Skip("run the compiled test beside the bundled renderer")
	}
	pdfMu.Lock()
	defer pdfMu.Unlock()
	if err := loadPDFium(); err != nil {
		t.Fatal(err)
	}
	body := TestPrintContent
	var pin runtime.Pinner
	pin.Pin(&body[0])
	defer pin.Unpin()
	doc := pdfCall("FPDF_LoadMemDocument64", uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)), 0)
	if doc == 0 {
		t.Fatal("test PDF failed to open")
	}
	defer pdfCall("FPDF_CloseDocument", doc)
	if n := pdfCall("FPDF_GetPageCount", doc); n != 1 {
		t.Fatalf("page count=%d", n)
	}
	page := pdfCall("FPDF_LoadPage", doc, 0)
	if page == 0 {
		t.Fatal("page load failed")
	}
	defer pdfCall("FPDF_ClosePage", page)
	dc, _, _ := gdi.NewProc("CreateCompatibleDC").Call(0)
	if dc == 0 {
		t.Fatal("memory DC unavailable")
	}
	defer gdi.NewProc("DeleteDC").Call(dc)
	info := bitmapInfo{Size: 40, Width: 595, Height: -842, Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bmp, _, _ := gdi.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		t.Fatal("DIB allocation failed")
	}
	defer gdi.NewProc("DeleteObject").Call(bmp)
	old, _, _ := gdi.NewProc("SelectObject").Call(dc, bmp)
	defer gdi.NewProc("SelectObject").Call(dc, old)
	pixels := unsafe.Slice((*byte)(bits), 595*842*4)
	for i := range pixels {
		pixels[i] = 255
	}
	pdfCall("FPDF_RenderPage", dc, page, 0, 0, 595, 842, 0, 0x800|1)
	dark := 0
	for i := 0; i < len(pixels); i += 4 {
		if pixels[i] < 128 {
			dark++
		}
	}
	if dark < 50 {
		t.Fatalf("rendered blank page (%d dark pixels)", dark)
	}
}

func TestBundledPDFiumPreviewPNG(t *testing.T) {
	exe, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "pdfium.dll")); err != nil {
		t.Skip("run compiled test beside renderer")
	}
	body, err := RenderPreview(context.Background(), TestPrintContent, 1)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() > 900 || img.Bounds().Dy() > 900 {
		t.Fatal("unbounded preview")
	}
	coloured := false
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 60000 || g < 60000 || b < 60000 {
				coloured = true
			}
		}
	}
	if !coloured {
		t.Fatal("preview is blank")
	}
	if _, err := RenderPreview(context.Background(), TestPrintContent, 2); err == nil {
		t.Fatal("accepted nonexistent page")
	}
}
