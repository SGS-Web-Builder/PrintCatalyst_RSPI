//go:build windows

package dispatch

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestInvoiceDesignedPreview(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{{"A4", 794, 1123}, {"A6", 397, 559}} {
		t.Run(size.name, func(t *testing.T) {
			dc, _, _ := gdi.NewProc("CreateCompatibleDC").Call(0)
			if dc == 0 {
				t.Fatal("DC failed")
			}
			defer gdi.NewProc("DeleteDC").Call(dc)
			info := bitmapInfo{Size: 40, Width: int32(size.w), Height: -int32(size.h), Planes: 1, BitCount: 32}
			var bits unsafe.Pointer
			bmp, _, _ := gdi.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
			if bmp == 0 {
				t.Fatal("bitmap failed")
			}
			defer gdi.NewProc("DeleteObject").Call(bmp)
			old, _, _ := gdi.NewProc("SelectObject").Call(dc, bmp)
			defer gdi.NewProc("SelectObject").Call(dc, old)
			pixels := unsafe.Slice((*byte)(bits), size.w*size.h*4)
			pages := 0
			begin := func() error {
				for i := range pixels {
					pixels[i] = 255
				}
				return nil
			}
			end := func() error {
				gdi.NewProc("GdiFlush").Call()
				pages++
				img := image.NewNRGBA(image.Rect(0, 0, size.w, size.h))
				dark := 0
				for i := 0; i < len(pixels); i += 4 {
					img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pixels[i+2], pixels[i+1], pixels[i], 255
					if pixels[i] < 200 {
						dark++
					}
				}
				if dark < 100 {
					return fmt.Errorf("blank invoice page")
				}
				if out := os.Getenv("PC_INVOICE_PREVIEW_DIR"); out != "" {
					if e := os.MkdirAll(out, 0755); e != nil {
						return e
					}
					f, e := os.Create(filepath.Join(out, fmt.Sprintf("invoice-%s-%d.png", size.name, pages)))
					if e != nil {
						return e
					}
					e = png.Encode(f, img)
					f.Close()
					return e
				}
				return nil
			}
			text := "Print Catalyst\nPRINT DONE\nOrder: PC-2048\nPlaced: 30 Sep 2026 16:45\nCustomer: Ravi Sharma\nPhone: 98765 43210\nPrinter: Shop printer\nPayment: Recorded\n\nORDER DOCUMENTS & SETTINGS\nDocument: Project report.pdf\nPaper: A4\nColour: Black & white\nSides: Back-to-back\nCopies: 2\nPages: 1-8\nOrientation: Portrait\nPages per side: 1\nLine value: INR 24.00\n\nTOTAL ORDER VALUE: INR 24.00\nEnd of this order on this printer."
			sheets := 0
			if err := drawInvoicePages(context.Background(), dc, text, nil, size.w, size.h, 96, &sheets, begin, end); err != nil {
				t.Fatal(err)
			}
			if sheets != pages || pages < 1 {
				t.Fatal("incorrect page count")
			}
		})
	}
}
