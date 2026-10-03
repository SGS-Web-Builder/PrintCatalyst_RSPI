//go:build windows

// Windows printing uses a driver device context, never RAW document bytes.
// PDFium renders PDF pages; Go decodes images. DEVMODE configures the driver.
package dispatch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"golang.org/x/sys/windows"
)

var spool = windows.NewLazySystemDLL("winspool.drv")
var gdi = windows.NewLazySystemDLL("gdi32.dll")
var pdfMu sync.Mutex
var pdfOnce sync.Once
var pdfErr error
var pdfProcs = map[string]uintptr{}

func pdfCall(name string, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(pdfProcs[name], args...)
	return r
}
func loadPDFium() error {
	pdfOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			pdfErr = err
			return
		}
		lib, err := windows.LoadLibraryEx(filepath.Join(filepath.Dir(exe), "pdfium.dll"), 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
		if err != nil {
			pdfErr = fmt.Errorf("PDF renderer missing: repair the Print Catalyst installation: %w", err)
			return
		}
		for _, n := range []string{"FPDF_InitLibrary", "FPDF_LoadMemDocument64", "FPDF_CloseDocument", "FPDF_GetPageCount", "FPDF_GetPageSizeByIndexF", "FPDF_LoadPage", "FPDF_ClosePage", "FPDF_RenderPage", "FPDFBitmap_Create", "FPDFBitmap_Destroy", "FPDFBitmap_FillRect", "FPDFBitmap_GetBuffer", "FPDFBitmap_GetStride", "FPDF_RenderPageBitmap"} {
			p, e := windows.GetProcAddress(lib, n)
			if e != nil {
				pdfErr = e
				return
			}
			pdfProcs[n] = p
		}
		pdfCall("FPDF_InitLibrary")
	})
	return pdfErr
}

type WinspoolBackend struct{}

func NewWinspoolBackend() *WinspoolBackend { return &WinspoolBackend{} }
func (b *WinspoolBackend) Name() string    { return "winspool-gdi" }

func WinspoolAvailable() bool            { return true }
func utf16Ptr(s string) (*uint16, error) { return windows.UTF16PtrFromString(s) }
func freeUTF16(*uint16)                  {}

type docInfo struct {
	Size                   int32
	Name, Output, Datatype *uint16
	Flags                  uint32
}
type bitmapInfo struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPels, YPels           int32
	Used, Important        uint32
}

func (b *WinspoolBackend) Submit(ctx context.Context, queue string, content []byte, ref DocumentRef) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if queue == "" || len(content) == 0 {
		return "", errors.New("printer and document are required")
	}
	if ref.MIMEType == documents.DocxMIME {
		var err error
		content, err = documents.ConvertDOCX(ctx, content)
		if err != nil {
			return "", err
		}
	}
	// Serialise PDFium and test-print calls. Its public API is not thread safe.
	pdfMu.Lock()
	defer pdfMu.Unlock()
	var pdf uintptr
	var img image.Image
	var err error
	count := 1
	isPDF := bytes.HasPrefix(content, []byte("%PDF-"))
	var pin runtime.Pinner
	if isPDF {
		if err = loadPDFium(); err != nil {
			return "", err
		}
		pin.Pin(&content[0])
		defer pin.Unpin()
		pdf = pdfCall("FPDF_LoadMemDocument64", uintptr(unsafe.Pointer(&content[0])), uintptr(len(content)), 0)
		if pdf == 0 {
			return "", errors.New("cannot open PDF (corrupt or password protected)")
		}
		defer pdfCall("FPDF_CloseDocument", pdf)
		count = int(pdfCall("FPDF_GetPageCount", pdf))
	} else if !ref.Invoice {
		cfg, _, e := image.DecodeConfig(bytes.NewReader(content))
		if e != nil {
			return "", fmt.Errorf("unsupported print document; upload PDF, JPG or PNG: %w", e)
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			return "", errors.New("image exceeds 40 megapixels")
		}
		img, err = decodePrintImage(content)
		if err != nil {
			return "", err
		}
	}
	if ref.PageStart == 0 {
		ref.PageStart = 1
	}
	if ref.PageEnd == 0 {
		ref.PageEnd = count
	}
	if ref.Copies == 0 {
		ref.Copies = 1
	}
	if ref.PagesPerSheet == 0 {
		ref.PagesPerSheet = 1
	}
	if err = validatePrintOptions(ref, count); err != nil {
		return "", err
	}
	selectedPages := printPages(ref, count)
	if ref.Orientation == "" || ref.Orientation == "auto" {
		w, h := 1.0, 1.0
		if pdf != 0 {
			var size [2]float32
			if pdfCall("FPDF_GetPageSizeByIndexF", pdf, uintptr(selectedPages[0]-1), uintptr(unsafe.Pointer(&size[0]))) == 0 {
				return "", errors.New("cannot read PDF page size")
			}
			w, h = float64(size[0]), float64(size[1])
		} else {
			w, h = float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
		}
		ref.Orientation = "portrait"
		if w > h {
			ref.Orientation = "landscape"
		}
	}
	ref.Sides = orientationDuplex(ref.Sides, ref.Orientation)
	name, err := utf16Ptr(queue)
	if err != nil {
		return "", err
	}
	var ph windows.Handle
	ok, _, e := spool.NewProc("OpenPrinterW").Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&ph)), 0)
	if ok == 0 {
		return "", fmt.Errorf("open printer: %w", e)
	}
	defer spool.NewProc("ClosePrinter").Call(uintptr(ph))
	dm, err := printerMode(ph, name, ref)
	if err != nil {
		return "", err
	}
	driver, _ := utf16Ptr("WINSPOOL")
	dc, _, e := gdi.NewProc("CreateDCW").Call(uintptr(unsafe.Pointer(driver)), uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&dm[0])))
	runtime.KeepAlive(dm)
	if dc == 0 {
		return "", fmt.Errorf("create printer context: %w", e)
	}
	defer gdi.NewProc("DeleteDC").Call(dc)
	caps := func(c uintptr) int { v, _, _ := gdi.NewProc("GetDeviceCaps").Call(dc, c); return int(v) }
	width, height := caps(8), caps(10)
	if width <= 0 || height <= 0 {
		return "", errors.New("printer has no printable area")
	}
	title, _ := utf16Ptr("Print Catalyst " + ref.OrderID)
	info := docInfo{Name: title}
	info.Size = int32(unsafe.Sizeof(info))
	job, _, e := gdi.NewProc("StartDocW").Call(dc, uintptr(unsafe.Pointer(&info)))
	if int32(job) <= 0 {
		return "", fmt.Errorf("start print job: %w", e)
	}
	ended := false
	defer func() {
		if !ended {
			gdi.NewProc("AbortDoc").Call(dc)
		}
	}()
	// Keep the completion flag available until the monitor records it durably.
	var retained uintptr
	if ref.OrderID != "" && ref.OrderID != "test-print" && ref.OrderID != "paper-test" {
		retained, _, _ = spool.NewProc("SetJobW").Call(uintptr(ph), job, 0, 0, 8) // JOB_CONTROL_RETAIN
	}
	if ref.Invoice {
		if err = drawInvoice(ctx, dc, string(content), ref.InvoiceLogo, width, height, caps(90), ref.InvoiceSheets); err != nil {
			return "", err
		}
	} else {
		cols, rows := pageGrid(ref.PagesPerSheet)
		for copyIndex := 0; copyIndex < ref.Copies; copyIndex++ {
			sides := 0
			for first := 0; first < len(selectedPages); first += ref.PagesPerSheet {
				if err = ctx.Err(); err != nil {
					return "", err
				}
				if v, _, e := gdi.NewProc("StartPage").Call(dc); int32(v) <= 0 {
					return "", fmt.Errorf("start page: %w", e)
				}
				for slot := 0; slot < ref.PagesPerSheet && first+slot < len(selectedPages); slot++ {
					x, y := (slot%cols)*width/cols, (slot/cols)*height/rows
					cw, ch := width/cols, height/rows
					if pdf != 0 {
						var size [2]float32
						if pdfCall("FPDF_GetPageSizeByIndexF", pdf, uintptr(selectedPages[first+slot]-1), uintptr(unsafe.Pointer(&size[0]))) == 0 {
							return "", errors.New("cannot read PDF page size")
						}
						px, py, pw, ph := fitPage(float64(size[0]), float64(size[1]), x, y, cw, ch)
						page := pdfCall("FPDF_LoadPage", pdf, uintptr(selectedPages[first+slot]-1))
						if page == 0 {
							return "", errors.New("cannot load PDF page")
						}
						flags := uintptr(0x800 | 1)
						if ref.ColourMode == "monochrome" {
							flags |= 8
						}
						pdfCall("FPDF_RenderPage", dc, page, uintptr(px), uintptr(py), uintptr(pw), uintptr(ph), 0, flags)
						pdfCall("FPDF_ClosePage", page)
					} else if err = drawImage(dc, img, x, y, cw, ch, ref.ColourMode == "monochrome"); err != nil {
						return "", err
					}
				}
				if v, _, e := gdi.NewProc("EndPage").Call(dc); int32(v) <= 0 {
					return "", fmt.Errorf("end page: %w", e)
				}
				sides++
			}
			// Start every collated copy on a fresh sheet when duplex has an odd side count.
			if ref.Sides != "one-sided" && sides%2 == 1 && copyIndex+1 < ref.Copies {
				if v, _, e := gdi.NewProc("StartPage").Call(dc); int32(v) <= 0 {
					return "", e
				}
				if v, _, e := gdi.NewProc("EndPage").Call(dc); int32(v) <= 0 {
					return "", e
				}
			}
		}
	}
	if v, _, e := gdi.NewProc("EndDoc").Call(dc); int32(v) <= 0 {
		return "", fmt.Errorf("finish print job: %w", e)
	}
	ended = true
	runtime.KeepAlive(content)
	if retained != 0 {
		return fmt.Sprintf("winspool-retained-job-%d", job), nil
	}
	return fmt.Sprintf("winspool-job-%d", job), nil
}

func printerMode(handle windows.Handle, name *uint16, ref DocumentRef) ([]byte, error) {
	proc := spool.NewProc("DocumentPropertiesW")
	size, _, _ := proc.Call(0, uintptr(handle), uintptr(unsafe.Pointer(name)), 0, 0, 0)
	if int32(size) < 220 || size > 1<<20 {
		return nil, errors.New("invalid printer DEVMODE size")
	}
	dm := make([]byte, size)
	if v, _, _ := proc.Call(0, uintptr(handle), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm[0])), 0, 2); int32(v) != 1 {
		return nil, errors.New("cannot read printer defaults")
	}
	if binary.LittleEndian.Uint16(dm[68:]) < 102 {
		return nil, errors.New("printer DEVMODE is too short")
	}
	paper, err := paperID(name, ref.PaperSize)
	if err != nil {
		return nil, err
	}
	fields := binary.LittleEndian.Uint32(dm[72:]) | 1 | 2 | 0x100 | 0x800 | 0x1000
	// Remove paper length/width and form name overrides; paper ID is authoritative.
	fields &^= 4 | 8 | 0x10000
	var source uint16
	if ref.Tray != "" {
		source, err = trayID(name, ref.Tray)
		if err != nil {
			return nil, err
		}
		fields |= 0x200 // DM_DEFAULTSOURCE
		binary.LittleEndian.PutUint16(dm[88:], source)
	}
	binary.LittleEndian.PutUint32(dm[72:], fields)
	orientation := uint16(1)
	if ref.Orientation == "landscape" {
		orientation = 2
	}
	colour := uint16(2)
	if ref.ColourMode == "monochrome" {
		colour = 1
	}
	duplex := uint16(1)
	if ref.Sides == "two-sided-long-edge" {
		duplex = 2
	}
	if ref.Sides == "two-sided-short-edge" {
		duplex = 3
	}
	for off, val := range map[int]uint16{76: orientation, 78: paper, 86: 1, 92: colour, 94: duplex} {
		binary.LittleEndian.PutUint16(dm[off:], val)
	}
	if v, _, _ := proc.Call(0, uintptr(handle), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm[0])), uintptr(unsafe.Pointer(&dm[0])), 2|8); int32(v) != 1 {
		return nil, errors.New("printer rejected selected settings")
	}
	for off, val := range map[int]uint16{76: orientation, 78: paper, 86: 1, 92: colour, 94: duplex} {
		if binary.LittleEndian.Uint16(dm[off:]) != val {
			return nil, fmt.Errorf("printer does not support requested setting at DEVMODE offset %d", off)
		}
	}
	if ref.Tray != "" && binary.LittleEndian.Uint16(dm[88:]) != source {
		return nil, errors.New("printer rejected the selected invoice tray")
	}
	return dm, nil
}
func paperID(name *uint16, paper string) (uint16, error) {
	cap := spool.NewProc("DeviceCapabilitiesW")
	n, _, _ := cap.Call(uintptr(unsafe.Pointer(name)), 0, 2, 0, 0)
	if int32(n) <= 0 || n > 1024 {
		return 0, errors.New("printer paper sizes unavailable")
	}
	ids := make([]uint16, n)
	names := make([]uint16, n*64)
	got, _, _ := cap.Call(uintptr(unsafe.Pointer(name)), 0, 2, uintptr(unsafe.Pointer(&ids[0])), 0)
	gotNames, _, _ := cap.Call(uintptr(unsafe.Pointer(name)), 0, 16, uintptr(unsafe.Pointer(&names[0])), 0)
	if got != n || gotNames != n {
		return 0, errors.New("printer paper capabilities changed; retry")
	}
	canonical := map[string]uint16{"A3": 8, "A4": 9, "A5": 11, "A6": 70, "LETTER": 1, "LEGAL": 5, "TABLOID": 3, "EXECUTIVE": 7}
	for i, id := range ids {
		label := windows.UTF16ToString(names[i*64 : (i+1)*64])
		if strings.EqualFold(strings.TrimSpace(label), strings.TrimSpace(paper)) || (canonical[strings.ToUpper(paper)] != 0 && canonical[strings.ToUpper(paper)] == id) {
			return id, nil
		}
	}
	return 0, fmt.Errorf("paper size %q is not supported by this printer", paper)
}
func drawImage(dc uintptr, img image.Image, x, y, w, h int, mono bool) error {
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	data := make([]byte, iw*ih*4)
	for py := 0; py < ih; py++ {
		for px := 0; px < iw; px++ {
			r, g, blue, a := img.At(b.Min.X+px, b.Min.Y+py).RGBA()
			// RGBA values are premultiplied; composite transparency over white.
			r = (r + 65535 - a) >> 8
			g = (g + 65535 - a) >> 8
			blue = (blue + 65535 - a) >> 8
			if mono {
				v := (299*r + 587*g + 114*blue) / 1000
				r, g, blue = v, v, v
			}
			off := (py*iw + px) * 4
			data[off], data[off+1], data[off+2] = byte(blue), byte(g), byte(r)
		}
	}
	info := bitmapInfo{Size: 40, Width: int32(iw), Height: -int32(ih), Planes: 1, BitCount: 32}
	px, py, pw, ph := fitPage(float64(iw), float64(ih), x, y, w, h)
	gdi.NewProc("SetStretchBltMode").Call(dc, 4)
	r, _, e := gdi.NewProc("StretchDIBits").Call(dc, uintptr(px), uintptr(py), uintptr(pw), uintptr(ph), 0, 0, uintptr(iw), uintptr(ih), uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&info)), 0, 0x00CC0020)
	if int32(r) <= 0 {
		return fmt.Errorf("render image: %w", e)
	}
	return nil
}
