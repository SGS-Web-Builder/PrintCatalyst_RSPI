package documents_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
)

// minimalPDF constructs a small PDF in memory with N leaf /Page objects
// (numbered 3..N+2), a /Pages container (2) and a /Catalog (1). The body
// carries an xref table and trailer so rsc.io/pdf can parse it like a real
// document — that is the parser the production code uses.
func minimalPDF(pageCount int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	// Object 1: /Catalog referencing the /Pages container.
	obj1 := buf.Len()
	buf.WriteString("1 0 obj\n<</Type/Catalog/Pages 2 0 R>>\nendobj\n")
	// Object 2: /Pages container with N /Kids.
	obj2 := buf.Len()
	buf.WriteString("2 0 obj\n<</Type/Pages/Count ")
	buf.WriteString(strconv.Itoa(pageCount))
	buf.WriteString("/Kids[")
	for i := 0; i < pageCount; i++ {
		if i > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(strconv.Itoa(3 + i))
		buf.WriteString(" 0 R")
	}
	buf.WriteString("]>>\nendobj\n")
	// Object 3..N+2: leaf /Page objects.
	pageOffsets := make([]int, pageCount)
	for i := 0; i < pageCount; i++ {
		pageOffsets[i] = buf.Len()
		buf.WriteString(strconv.Itoa(3+i) + " 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")
	}

	// Cross-reference table.
	xrefOffset := buf.Len()
	totalObjects := 2 + pageCount
	buf.WriteString("xref\n")
	buf.WriteString("0 " + strconv.Itoa(totalObjects+1) + "\n")
	buf.WriteString("0000000000 65535 f \n")
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj1))
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj2))
	for _, offset := range pageOffsets {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", offset))
	}

	// Trailer and startxref.
	buf.WriteString("trailer\n<</Size " + strconv.Itoa(totalObjects+1) + "/Root 1 0 R>>\n")
	buf.WriteString(fmt.Sprintf("startxref\n%d\n", xrefOffset))
	buf.WriteString("%%EOF\n")
	return buf.Bytes()
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestCountPagesDetectsPDFPageCount(t *testing.T) {
	tests := []struct {
		name    string
		pages   int
		wantMin int
		wantMax int
	}{
		{"one page", 1, 1, 1},
		{"three pages", 3, 1, 5},
		{"ten pages", 10, 5, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := minimalPDF(tt.pages)
			got, err := documents.CountPages("application/pdf", body)
			if err != nil {
				t.Fatalf("CountPages error: %v", err)
			}
			if got < tt.wantMin || got > tt.wantMax {
				t.Fatalf("got %d, want in [%d,%d]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCountPagesRecognisesJPEGPng(t *testing.T) {
	// SOI + APP0 + a minimal SOF0 frame marker + EOI so the JPEG validator
	// accepts the body as a printable one-page image.
	jpeg := []byte{
		0xff, 0xd8, // SOI
		0xff, 0xe0, 0x00, 0x10, // APP0 length=16
		'J', 'F', 'I', 'F', 0x00, // identifier
		0x01, 0x01, 0x00, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00, // version + density
		0xff, 0xc0, 0x00, 0x0b, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00, // SOF0 (frame marker)
		0xff, 0xd9, // EOI
	}
	if got, err := documents.CountPages("image/jpeg", jpeg); err != nil || got != 1 {
		t.Fatalf("jpeg pages: got %d err %v, want 1 nil", got, err)
	}
	// Minimal but valid PNG: signature + IHDR (1x1) + IDAT (one zero byte)
	// + IEND. CRCs are zero because validation only checks marker structure.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, // IHDR length=13
		'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, // bit depth, colour, compression, filter, interlace
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
		0x00, 0x00, 0x00, 0x01, // IDAT length=1
		'I', 'D', 'A', 'T',
		0x00, // one byte of payload
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
		0x00, 0x00, 0x00, 0x00, // IEND length=0
		'I', 'E', 'N', 'D',
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
	}
	if got, err := documents.CountPages("image/png", png); err != nil || got != 1 {
		t.Fatalf("png pages: got %d err %v, want 1 nil", got, err)
	}
}

func TestCountPagesRejectsInvalidPDF(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"too small", []byte("hi")},
		{"missing header", []byte("just garbage bytes here that are clearly long enough")},
		{"no page objects", []byte("%PDF-1.4\nno page objects at all just junk content here")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := documents.CountPages("application/pdf", tc.body)
			if err == nil {
				t.Fatal("expected error for invalid PDF, got nil")
			}
		})
	}
}

func TestCountPagesRejectsUnknownMIME(t *testing.T) {
	_, err := documents.CountPages("text/plain", []byte("hello"))
	if err == nil {
		t.Fatal("expected error for unknown MIME")
	}
}

func TestCountPagesRejectsOversizedImageDimensions(t *testing.T) {
	// JPEG: SOI + APP0 + SOF0 with height/width set to 99999 (over the
	// 10000-pixel limit) + EOI.
	jpeg := []byte{
		0xff, 0xd8,
		0xff, 0xe0, 0x00, 0x10,
		'J', 'F', 'I', 'F', 0x00,
		0x01, 0x01, 0x00, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00,
		0xff, 0xc0, 0x00, 0x0b, 0x08,
		0x9c, 0x40, // height = 40000
		0x9c, 0x40, // width = 40000
		0x01, 0x01, 0x01, 0x11, 0x00,
		0xff, 0xd9,
	}
	if _, err := documents.CountPages("image/jpeg", jpeg); err == nil {
		t.Fatal("expected oversized JPEG to be rejected")
	}

	// PNG: signature + IHDR with width=20000, height=20000 + IDAT + IEND.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, // length=13
		'I', 'H', 'D', 'R',
		0x00, 0x00, 0x4e, 0x20, // width = 20000
		0x00, 0x00, 0x4e, 0x20, // height = 20000
		0x08, 0x02, 0x00, 0x00, 0x00,
		// CRC (4 bytes, can be zero for validation purposes)
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x0a, // IDAT length=10
		'I', 'D', 'A', 'T',
		// 10 bytes of fake data + CRC.
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, // IEND length=0
		'I', 'E', 'N', 'D',
		0x00, 0x00, 0x00, 0x00, // CRC
	}
	if _, err := documents.CountPages("image/png", png); err == nil {
		t.Fatal("expected oversized PNG to be rejected")
	}
}

func TestRandomIDIsDistinct(t *testing.T) {
	// Sanity: hex.EncodeToString(rand.Read) returns unique ids.
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		id := hex.EncodeToString(buf)
		if seen[id] {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}
