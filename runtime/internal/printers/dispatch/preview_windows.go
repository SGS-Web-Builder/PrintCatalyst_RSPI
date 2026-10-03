//go:build windows

package dispatch

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"math"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RenderPreview renders one PDF page locally as a bounded PNG. No printer is
// opened and no document leaves the shop PC. Mobile browsers can display it
// without a built-in PDF viewer.
func RenderPreview(ctx context.Context, content []byte, pageNumber int) ([]byte, error) {
	if len(content) == 0 || pageNumber < 1 {
		return nil, errors.New("invalid preview page")
	}
	pdfMu.Lock()
	defer pdfMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := loadPDFium(); err != nil {
		return nil, err
	}
	var pin runtime.Pinner
	pin.Pin(&content[0])
	defer pin.Unpin()
	doc := pdfCall("FPDF_LoadMemDocument64", uintptr(unsafe.Pointer(&content[0])), uintptr(len(content)), 0)
	if doc == 0 {
		return nil, errors.New("cannot open PDF preview")
	}
	defer pdfCall("FPDF_CloseDocument", doc)
	if pageNumber > int(pdfCall("FPDF_GetPageCount", doc)) {
		return nil, errors.New("preview page is out of range")
	}
	var size [2]float32
	if pdfCall("FPDF_GetPageSizeByIndexF", doc, uintptr(pageNumber-1), uintptr(unsafe.Pointer(&size[0]))) == 0 {
		return nil, errors.New("cannot read page size")
	}
	w, h := float64(size[0]), float64(size[1])
	if w <= 0 || h <= 0 || math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) {
		return nil, errors.New("invalid PDF dimensions")
	}
	scale := 900 / math.Max(w, h)
	width, height := max(1, int(w*scale)), max(1, int(h*scale))
	page := pdfCall("FPDF_LoadPage", doc, uintptr(pageNumber-1))
	if page == 0 {
		return nil, errors.New("cannot load page")
	}
	defer pdfCall("FPDF_ClosePage", page)
	bitmap := pdfCall("FPDFBitmap_Create", uintptr(width), uintptr(height), 1)
	if bitmap == 0 {
		return nil, errors.New("cannot allocate preview")
	}
	defer pdfCall("FPDFBitmap_Destroy", bitmap)
	pdfCall("FPDFBitmap_FillRect", bitmap, 0, 0, uintptr(width), uintptr(height), 0xffffffff)
	pdfCall("FPDF_RenderPageBitmap", bitmap, page, 0, 0, uintptr(width), uintptr(height), 0, 0x800|1)
	buffer := pdfCall("FPDFBitmap_GetBuffer", bitmap)
	stride := int(pdfCall("FPDFBitmap_GetStride", bitmap))
	if buffer == 0 || stride < width*4 || stride > width*4+4096 {
		return nil, errors.New("invalid preview bitmap")
	}
	// Copy from PDFium-owned memory while its bitmap is still alive. Keep the
	// native address as uintptr rather than manufacturing a Go pointer to it.
	raw := make([]byte, stride*height)
	windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlMoveMemory").Call(uintptr(unsafe.Pointer(&raw[0])), buffer, uintptr(len(raw)))
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src := y*stride + x*4
			dst := y*img.Stride + x*4
			img.Pix[dst] = raw[src+2]
			img.Pix[dst+1] = raw[src+1]
			img.Pix[dst+2] = raw[src]
			img.Pix[dst+3] = raw[src+3]
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
