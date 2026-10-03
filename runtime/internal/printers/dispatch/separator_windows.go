//go:build windows

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"strings"
	"unicode/utf16"
	"unsafe"
)

// Resolve the exact driver label at submission time. Bin IDs are driver-specific
// and must never be guessed from names such as "Tray 2".
func trayID(name *uint16, label string) (uint16, error) {
	cap := spool.NewProc("DeviceCapabilitiesW")
	n, _, _ := cap.Call(uintptr(unsafe.Pointer(name)), 0, 6, 0, 0)
	if int32(n) <= 0 || n > 1024 {
		return 0, errors.New("printer trays unavailable")
	}
	ids := make([]uint16, n)
	names := make([]uint16, n*24)
	got, _, _ := cap.Call(uintptr(unsafe.Pointer(name)), 0, 6, uintptr(unsafe.Pointer(&ids[0])), 0)
	labels, _, _ := cap.Call(uintptr(unsafe.Pointer(name)), 0, 12, uintptr(unsafe.Pointer(&names[0])), 0)
	if got != n || labels != n {
		return 0, errors.New("printer trays changed; refresh printer setup")
	}
	for i, id := range ids {
		if strings.TrimSpace(windows.UTF16ToString(names[i*24:(i+1)*24])) == strings.TrimSpace(label) {
			return id, nil
		}
	}
	return 0, fmt.Errorf("invoice tray %q is no longer available", label)
}

// The same renderer draws printer pages and the offscreen preview used in tests.
func drawInvoice(ctx context.Context, dc uintptr, text string, logo []byte, width, height, dpi int, sheets *int) error {
	return drawInvoicePages(ctx, dc, text, logo, width, height, dpi, sheets, func() error {
		if v, _, e := gdi.NewProc("StartPage").Call(dc); int32(v) <= 0 {
			return e
		}
		return nil
	}, func() error {
		if v, _, e := gdi.NewProc("EndPage").Call(dc); int32(v) <= 0 {
			return e
		}
		return nil
	})
}

func drawInvoicePages(ctx context.Context, dc uintptr, text string, logo []byte, width, height, dpi int, sheets *int, startPage, endPage func() error) error {
	if dpi <= 0 {
		return errors.New("printer resolution unavailable")
	}
	unit := func(pt int) int { return max(1, pt*dpi/72) }
	fontName, _ := utf16Ptr("Segoe UI")
	makeFont := func(points, weight int) uintptr {
		h := int32(-unit(points))
		f, _, _ := gdi.NewProc("CreateFontW").Call(uintptr(h), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 0, 0, uintptr(unsafe.Pointer(fontName)))
		return f
	}
	normal, bold, title := makeFont(9, 400), makeFont(9, 600), makeFont(15, 700)
	if normal == 0 || bold == 0 || title == 0 {
		return errors.New("cannot create invoice fonts")
	}
	defer gdi.NewProc("DeleteObject").Call(normal)
	defer gdi.NewProc("DeleteObject").Call(bold)
	defer gdi.NewProc("DeleteObject").Call(title)
	old, _, _ := gdi.NewProc("SelectObject").Call(dc, normal)
	defer gdi.NewProc("SelectObject").Call(dc, old)
	gdi.NewProc("SetTextColor").Call(dc, 0x002b241b)
	gdi.NewProc("SetBkMode").Call(dc, 1)
	margin := unit(14)
	boxW := min(width-2*margin, unit(470))
	left := (width - boxW) / 2
	right := left + boxW
	pad := unit(7)
	lineH := unit(14)
	if boxW < unit(110) || height < unit(160) {
		return errors.New("invoice paper too small for readable layout")
	}
	pen, _, _ := gdi.NewProc("CreatePen").Call(0, uintptr(unit(1)), 0x00dddddd)
	defer gdi.NewProc("DeleteObject").Call(pen)
	oldPen, _, _ := gdi.NewProc("SelectObject").Call(dc, pen)
	defer gdi.NewProc("SelectObject").Call(dc, oldPen)
	line := func(x1, y1, x2, y2 int) {
		gdi.NewProc("MoveToEx").Call(dc, uintptr(x1), uintptr(y1), 0)
		gdi.NewProc("LineTo").Call(dc, uintptr(x2), uintptr(y2))
	}
	measure := func(s string) (int, error) {
		u := utf16.Encode([]rune(s))
		if len(u) == 0 {
			return 0, nil
		}
		var size struct{ X, Y int32 }
		v, _, e := gdi.NewProc("GetTextExtentPoint32W").Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&size)))
		if v == 0 {
			return 0, e
		}
		return int(size.X), nil
	}
	write := func(s string, x, y int, font uintptr, center bool) error {
		gdi.NewProc("SelectObject").Call(dc, font)
		if center {
			w, e := measure(s)
			if e != nil {
				return e
			}
			x += (boxW - w) / 2
		}
		u := utf16.Encode([]rune(s))
		if len(u) == 0 {
			return nil
		}
		v, _, e := gdi.NewProc("TextOutW").Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)))
		if v == 0 {
			return e
		}
		return nil
	}
	paragraphs := strings.Split(strings.TrimSpace(text), "\n")
	merchant := paragraphs[0]
	heading := "ORDER PRINT RECEIPT"
	if len(paragraphs) > 1 && paragraphs[1] == "PRINT DONE" {
		heading = "Print Done"
	}
	y, page := 0, 0
	finish := func() error {
		line(left, y, right, y)
		if err := write(fmt.Sprintf("Print order receipt  |  Page %d", page), left, height-margin-lineH, normal, true); err != nil {
			return err
		}
		return endPage()
	}
	begin := func() error {
		if err := startPage(); err != nil {
			return err
		}
		page++
		y = margin
		if page == 1 && len(logo) > 0 {
			img, e := decodePrintImage(logo)
			if e != nil {
				return e
			}
			if e = drawImage(dc, img, left, y, boxW, unit(38), true); e != nil {
				return e
			}
			y += unit(46)
		}
		gdi.NewProc("SelectObject").Call(dc, title)
		names, e := wrapInvoice(merchant, boxW-2*pad, measure)
		if e != nil {
			return e
		}
		for _, v := range names {
			if e = write(v, left, y, title, true); e != nil {
				return e
			}
			y += unit(20)
		}
		y += unit(8)
		// A vector check stays crisp and does not depend on emoji fonts.
		checkPen, _, _ := gdi.NewProc("CreatePen").Call(0, uintptr(unit(2)), 0x003c3428)
		previousPen, _, _ := gdi.NewProc("SelectObject").Call(dc, checkPen)
		cx := left + boxW/2
		line(cx-unit(6), y+unit(5), cx-unit(1), y+unit(10))
		line(cx-unit(1), y+unit(10), cx+unit(8), y)
		gdi.NewProc("SelectObject").Call(dc, previousPen)
		gdi.NewProc("DeleteObject").Call(checkPen)
		y += unit(18)
		if e = write(heading, left, y, bold, true); e != nil {
			return e
		}
		y += unit(24)
		line(left, y, right, y)
		if y+2*pad+3*lineH > height-margin {
			return errors.New("invoice header is too tall for selected paper")
		}
		return nil
	}
	if err := begin(); err != nil {
		return err
	}
	for i, paragraph := range paragraphs {
		if i == 0 || paragraph == "ORDER INVOICE / SEPARATOR" || paragraph == "PRINT DONE" || paragraph == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		label, value := "", paragraph
		if parts := strings.SplitN(paragraph, ": ", 2); len(parts) == 2 {
			label, value = parts[0], parts[1]
		}
		labelW := boxW / 3
		valueX := left + pad
		available := boxW - 2*pad
		if label != "" {
			valueX = left + labelW + pad
			available = boxW - labelW - 2*pad
		}
		gdi.NewProc("SelectObject").Call(dc, normal)
		values, e := wrapInvoice(value, available, measure)
		if e != nil {
			return e
		}
		labels := []string{}
		if label != "" {
			gdi.NewProc("SelectObject").Call(dc, bold)
			labels, e = wrapInvoice(label, labelW-2*pad, measure)
			if e != nil {
				return e
			}
		}
		count := max(len(values), len(labels))
		for n := 0; n < count; {
			room := (height - margin - 2*lineH - y - 2*pad) / lineH
			if room < 1 {
				if e = finish(); e != nil {
					return e
				}
				if e = begin(); e != nil {
					return e
				}
				continue
			}
			take := min(room, count-n)
			rowH := take*lineH + 2*pad
			line(left, y, left, y+rowH)
			line(right, y, right, y+rowH)
			if label != "" {
				line(left+labelW, y, left+labelW, y+rowH)
			}
			for j := 0; j < take; j++ {
				if n+j < len(labels) {
					if e = write(labels[n+j], left+pad, y+pad+j*lineH, bold, false); e != nil {
						return e
					}
				}
				if n+j < len(values) {
					f := normal
					if paragraph == "ORDER DOCUMENTS & SETTINGS" || strings.HasPrefix(paragraph, "TOTAL ORDER VALUE") {
						f = bold
					}
					if e = write(values[n+j], valueX, y+pad+j*lineH, f, false); e != nil {
						return e
					}
				}
			}
			y += rowH
			line(left, y, right, y)
			n += take
		}
	}
	if err := finish(); err != nil {
		return err
	}
	if sheets != nil {
		*sheets = page
	}
	return nil
}
