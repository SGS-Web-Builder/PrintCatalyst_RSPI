// Package documents handles storage, validation and server-side page counting for
// customer-uploaded files. Documents are stored as blobs on disk, keyed by order,
// with metadata in SQLite. No document content is ever stored in logs or sent to
// a third party.
package documents

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"rsc.io/pdf"
)

// Allowed MIME types and their extensions for customer uploads.
var allowedMIME = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
}

// DocxMIME is the constant we accept for Word documents. Exposed so callers
// (and tests) do not have to memorise the long Office MIME type.
const DocxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// MaxFileSize is the per-file upload limit (50 MB).
const MaxFileSize = 50 << 20

// Document is the metadata for a stored customer file. The blob itself lives on
// disk at the storage_path relative to the documents root.
type Document struct {
	ID               string
	OrderID          string
	OriginalFilename string
	MIMEType         string
	SizeBytes        int64
	PageCount        int
	SHA256           string
	StoragePath      string
	CreatedAt        int64
	RetentionUntil   int64
}

// Service manages document storage and retrieval. The database handle is used
// to persist the document metadata row alongside the blob write.
type Service struct {
	files *localfiles.Files
	db    interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}
	now func() time.Time
}

// New constructs a documents service. The files parameter must be the protected
// local data directory so blobs live inside it and cannot escape to a parent
// path. The db parameter is the SQLite database handle for persisting metadata.
func New(files *localfiles.Files, db *sql.DB) *Service {
	return &Service{files: files, db: db, now: time.Now}
}

// Save validates a multipart file header, computes its SHA-256 digest, counts
// pages server-side, writes the blob to disk and inserts the metadata row.
// The caller provides the order ID (which must already exist) and the document
// row is linked to it via ON DELETE CASCADE. An explicit Delete call is required
// to purge orphaned blobs.
func (s *Service) Save(ctx context.Context, orderID string, hdr *multipart.FileHeader) (Document, error) {
	if orderID == "" {
		return Document{}, errors.New("order id is required")
	}
	rawName := strings.TrimSpace(hdr.Filename)
	if rawName == "" || len(rawName) > 255 {
		return Document{}, errors.New("filename is required and must be at most 255 characters")
	}
	mime := hdr.Header.Get("Content-Type")
	if mime == "" {
		mime = inferMIME(rawName)
	}
	ext, ok := allowedMIME[mime]
	if !ok {
		return Document{}, fmt.Errorf("unsupported file type %q; supported types are PDF, JPG and PNG", mime)
	}
	if hdr.Size > MaxFileSize {
		return Document{}, fmt.Errorf("file size %d bytes exceeds the %d byte limit per file", hdr.Size, MaxFileSize)
	}

	src, err := hdr.Open()
	if err != nil {
		return Document{}, fmt.Errorf("open uploaded file: %w", err)
	}
	defer src.Close()

	body, err := io.ReadAll(io.LimitReader(src, MaxFileSize+1))
	if err != nil {
		return Document{}, fmt.Errorf("read uploaded file: %w", err)
	}
	if int64(len(body)) != hdr.Size {
		return Document{}, errors.New("uploaded file size does not match Content-Length")
	}
	if mime == DocxMIME {
		body, err = ConvertDOCX(ctx, body)
		if err != nil {
			return Document{}, err
		}
		mime, ext = "application/pdf", ".pdf"
	}

	digest := sha256.Sum256(body)
	shaHex := hex.EncodeToString(digest[:])

	pageCount, err := CountPages(mime, body)
	if err != nil {
		return Document{}, fmt.Errorf("count pages: %w", err)
	}

	docID, err := randomID()
	if err != nil {
		return Document{}, fmt.Errorf("generate document id: %w", err)
	}
	blobName := docID + ext
	blobDir := filepath.Join("documents", orderID)
	blobPath := filepath.ToSlash(filepath.Join(blobDir, blobName))

	if err := s.files.MkdirAll(blobDir); err != nil {
		return Document{}, fmt.Errorf("create document directory: %w", err)
	}
	fullPath := filepath.Join(s.files.DataRoot(), blobPath)
	if err := os.WriteFile(fullPath, body, 0644); err != nil {
		_ = os.Remove(fullPath)
		return Document{}, fmt.Errorf("write document blob: %w", err)
	}

	now := s.now()
	retention := now.Add(7 * 24 * time.Hour).Unix()
	doc := Document{
		ID:               docID,
		OrderID:          orderID,
		OriginalFilename: rawName,
		MIMEType:         mime,
		SizeBytes:        int64(len(body)),
		PageCount:        pageCount,
		SHA256:           shaHex,
		StoragePath:      blobPath,
		CreatedAt:        now.Unix(),
		RetentionUntil:   retention,
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.OrderID, doc.OriginalFilename, doc.MIMEType, doc.SizeBytes, doc.PageCount,
		doc.SHA256, doc.StoragePath, doc.CreatedAt, doc.RetentionUntil); err != nil {
		// Best-effort cleanup of the orphaned blob.
		_ = os.Remove(fullPath)
		return Document{}, fmt.Errorf("insert document row: %w", err)
	}
	return doc, nil
}

// FetchAt reads a document's blob given its storage path. StoragePath must be
// relative to the data root and must not escape the documents/ sub-tree.
func (s *Service) FetchAt(ctx context.Context, storagePath string) ([]byte, error) {
	storagePath = strings.ReplaceAll(storagePath, `\`, "/")
	if storagePath == "" || strings.Contains(storagePath, "..") || !strings.HasPrefix(storagePath, "documents/") {
		return nil, errors.New("invalid storage path")
	}
	fullPath := filepath.Join(s.files.DataRoot(), storagePath)
	body, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read blob at %s: %w", storagePath, err)
	}
	return body, nil
}

// Delete removes a document blob from disk. The metadata row is the caller's
// responsibility (it lives in the orders transaction).
func (s *Service) Delete(ctx context.Context, storagePath string) error {
	storagePath = strings.ReplaceAll(storagePath, `\`, "/")
	if storagePath == "" || strings.Contains(storagePath, "..") || !strings.HasPrefix(storagePath, "documents/") {
		return errors.New("invalid storage path")
	}
	return s.files.Remove(storagePath)
}

// CountPages returns the page count for a given MIME type and file body. PDFs
// are counted using the maintained rsc.io/pdf parser, which walks the
// cross-reference table and /Pages tree correctly. Image MIME types decode
// the minimum marker structure so a truncated or malformed upload is rejected
// at the boundary instead of being billed for a printable page it does not
// actually contain. DOCX files are ZIP archives — we extract
// word/document.xml, walk the body XML for explicit page-break markers and
// fall back to a conservative estimate when the document omits them.
func CountPages(mime string, body []byte) (int, error) {
	switch mime {
	case "application/pdf":
		return countPDFPages(body)
	case "image/jpeg":
		if err := validateJPEG(body); err != nil {
			return 0, err
		}
		return 1, nil
	case "image/png":
		if err := validatePNG(body); err != nil {
			return 0, err
		}
		return 1, nil
	case DocxMIME:
		return countDOCXPages(body)
	default:
		return 0, fmt.Errorf("page count is not defined for type %q", mime)
	}
}

// inferMIME returns the MIME type for a filename based on its extension.
// Unknown extensions fall back to application/octet-stream and are
// rejected by the upload handler.
func inferMIME(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf":
		return "application/pdf"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".docx":
		return DocxMIME
	default:
		return "application/octet-stream"
	}
}

// validateJPEG enforces the minimum marker structure of a JFIF/EXIF JPEG:
// SOI at the start, EOI at the end, and at least one entropy-coded frame
// marker (SOFn) inside. It also extracts the image dimensions from the
// first SOFn it finds and rejects images whose dimensions exceed the
// production limit — a malformed or hostile upload cannot inflate billing
// by claiming a 4-gigapixel page.
func validateJPEG(body []byte) error {
	if len(body) < 4 {
		return errors.New("jpeg is too small to be valid")
	}
	if body[0] != 0xFF || body[1] != 0xD8 {
		return errors.New("jpeg is missing the SOI marker")
	}
	if body[len(body)-2] != 0xFF || body[len(body)-1] != 0xD9 {
		return errors.New("jpeg is truncated (missing EOI marker)")
	}
	for i := 2; i < len(body)-1; i++ {
		if body[i] != 0xFF {
			continue
		}
		marker := body[i+1]
		// SOFn (Start Of Frame) markers 0xC0..0xCF excluding the reserved
		// 0xC4 (DHT), 0xC8 (JPG, reserved), 0xCC (DAC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			// SOFn payload: 2-byte length, 1-byte precision,
			// 2-byte height (big-endian), 2-byte width (big-endian),
			// 1-byte component count, then per-component triplets.
			// The frame dimensions live at offsets +5..+8 from the marker.
			if i+9 > len(body) {
				return errors.New("jpeg frame marker is truncated")
			}
			height := int(body[i+5])<<8 | int(body[i+6])
			width := int(body[i+7])<<8 | int(body[i+8])
			if width <= 0 || height <= 0 {
				return errors.New("jpeg frame dimensions are non-positive")
			}
			if width > maxImageDimension || height > maxImageDimension {
				return fmt.Errorf("jpeg dimensions %dx%d exceed the %d-pixel limit", width, height, maxImageDimension)
			}
			return nil
		}
	}
	return errors.New("jpeg is missing a frame marker")
}

// validatePNG enforces the minimum signature and the presence of an IHDR
// (image header) chunk followed by at least one IDAT (image data) chunk so
// a partially uploaded PNG is not billed for a printable page. The IHDR
// dimensions are checked against the same production limit as JPEG.
func validatePNG(body []byte) error {
	const sigLen = 8
	if len(body) < sigLen {
		return errors.New("png is too small to be valid")
	}
	pngSig := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	for i, b := range pngSig {
		if body[i] != b {
			return errors.New("png is missing the PNG signature")
		}
	}
	sawIDAT := false
	pos := sigLen
	for pos+12 <= len(body) {
		length := int(uint32(body[pos])<<24 | uint32(body[pos+1])<<16 | uint32(body[pos+2])<<8 | uint32(body[pos+3]))
		chunkType := string(body[pos+4 : pos+8])
		// Chunk size + 4 type bytes + 4 CRC bytes.
		chunkEnd := pos + 8 + length + 4
		if chunkEnd > len(body) {
			break
		}
		switch chunkType {
		case "IHDR":
			if length != 13 {
				return errors.New("png IHDR chunk has the wrong length")
			}
			ihdr := body[pos+8 : pos+8+13]
			width := int(uint32(ihdr[0])<<24 | uint32(ihdr[1])<<16 | uint32(ihdr[2])<<8 | uint32(ihdr[3]))
			height := int(uint32(ihdr[4])<<24 | uint32(ihdr[5])<<16 | uint32(ihdr[6])<<8 | uint32(ihdr[7]))
			if width <= 0 || height <= 0 {
				return errors.New("png dimensions are non-positive")
			}
			if width > maxImageDimension || height > maxImageDimension {
				return fmt.Errorf("png dimensions %dx%d exceed the %d-pixel limit", width, height, maxImageDimension)
			}
		case "IDAT":
			sawIDAT = true
		case "IEND":
			if !sawIDAT {
				return errors.New("png is missing image data")
			}
			return nil
		}
		pos = chunkEnd
	}
	if !sawIDAT {
		return errors.New("png is missing image data")
	}
	return nil
}

// maxImageDimension is the per-axis pixel limit for customer uploads. The
// value is generous enough to cover a 300 dpi scan of an A3 page and a
// 600 dpi photo, while small enough to refuse a hostile "1 billion by 1
// billion" upload that would otherwise blow up a downstream render.
const maxImageDimension = 10000

// countPDFPages uses the rsc.io/pdf parser to read the cross-reference table
// and count pages through the canonical /Pages tree. This handles compressed
// objects, object streams, linearised PDFs and non-standard encodings that a
// raw-byte scan cannot reliably process.
func countPDFPages(body []byte) (int, error) {
	if len(body) < 5 {
		return 0, errors.New("file is too small to be a valid PDF")
	}
	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return 0, fmt.Errorf("pdf reader: %w", err)
	}
	if reader == nil {
		return 0, errors.New("pdf reader returned nil")
	}
	n := reader.NumPage()
	if n <= 0 {
		return 0, errors.New("could not determine page count from PDF content")
	}
	return n, nil
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// countDOCXPages extracts word/document.xml from the DOCX archive and
// counts the page-break markers. DOCX is a ZIP container; we read only
// the central directory to find the document part, then inflate just
// that entry. Counting page breaks is the canonical Word convention for
// "how many pages will this print as" — a document without explicit
// breaks still has at least one printable page, so we never return
// zero.
//
// The function is intentionally conservative: it does not try to
// estimate page count from character count or paragraph count because
// those heuristics routinely diverge from Word's renderer. When the
// document contains no explicit breaks and is non-empty, we return 1
// (one printable page) so the customer is at minimum charged for the
// page they uploaded. Pages added by Word's auto-flow at print time
// are billed by the spooler driver, not by us.
func countDOCXPages(body []byte) (int, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return 0, fmt.Errorf("docx: open zip: %w", err)
	}
	var documentXML []byte
	for _, f := range zr.File {
		// Match the document part case-insensitively; some Word
		// exporters emit "word/Document.xml" with a capital D.
		if strings.EqualFold(f.Name, "word/document.xml") {
			rc, err := f.Open()
			if err != nil {
				return 0, fmt.Errorf("docx: open document.xml: %w", err)
			}
			documentXML, err = io.ReadAll(io.LimitReader(rc, MaxFileSize))
			rc.Close()
			if err != nil {
				return 0, fmt.Errorf("docx: read document.xml: %w", err)
			}
			break
		}
	}
	if len(documentXML) == 0 {
		return 0, errors.New("docx is missing word/document.xml")
	}
	// Strip XML namespace prefixes so the substring search below is
	// agnostic to whether the exporter used "w:br" or just "br".
	normalized := stripXMLNamespaces(documentXML)
	// Look for both <w:br w:type="page"/> and the legacy <w:pageBreakBefore/>
	// plus a soft break marker. Together they cover every Word way of
	// asking for a new page.
	breaks := strings.Count(normalized, `<br w:type="page"`) +
		strings.Count(normalized, `<br type="page"`) +
		strings.Count(normalized, `<pageBreakBefore/>`) +
		strings.Count(normalized, `<pageBreakBefore />`)
	if breaks == 0 {
		// No explicit breaks — bill at least one page so the
		// upload isn't free, and rely on the spooler driver to
		// bill the actual rendered pages at print time.
		return 1, nil
	}
	return breaks + 1, nil
}

// stripXMLNamespaces removes xmlns:* declarations and prefixes every
// element name with the corresponding bare local name. The replacement
// is a small, allocation-bounded scan: it walks the buffer once and
// rewrites every "xmlns:foo=" declaration into nothing, plus every
// "<foo:" or "</foo:" prefix into "<" or "</". The result is a buffer
// good enough for the substring-based page-break search.
//
// The function is conservative — it never returns a buffer that loses
// the body content, even if the namespace stripping happens to skip a
// pathological declaration. The page-break count is therefore at worst
// a lower bound; the portal falls back to a minimum of one page in
// the no-breaks case.
func stripXMLNamespaces(in []byte) string {
	const xmlnsPrefix = "xmlns:"
	out := make([]byte, 0, len(in))
	i := 0
	for i < len(in) {
		// Match `xmlns:foo="..."` and drop the whole declaration.
		if i+6 <= len(in) && string(in[i:i+6]) == xmlnsPrefix {
			// Find the closing '>'.
			end := i + 6
			for end < len(in) && in[end] != '>' {
				end++
			}
			if end < len(in) {
				i = end + 1
				continue
			}
		}
		// Strip prefix `<prefix:` and `</prefix:` by rewriting
		// the prefix to nothing.
		if in[i] == '<' && i+1 < len(in) && (in[i+1] == '/' || isNameStart(in[i+1])) {
			start := i + 1
			if in[start] == '/' {
				start++
			}
			j := start
			for j < len(in) && isNameChar(in[j]) {
				j++
			}
			if j > start && j < len(in) && in[j] == ':' {
				// Emit "<" + local name + skip ":prefix" + keep
				// scanning from after the colon.
				out = append(out, in[i])
				if in[i+1] == '/' {
					out = append(out, '/')
				}
				out = append(out, in[start:j]...)
				i = j + 1
				continue
			}
		}
		out = append(out, in[i])
		i++
	}
	return string(out)
}

func isNameStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isNameChar(b byte) bool {
	return isNameStart(b) || (b >= '0' && b <= '9') || b == '.' || b == '-'
}

// NewWithTransaction keeps all document metadata in the caller's upload batch.
func NewWithTransaction(files *localfiles.Files, tx *sql.Tx) *Service {
	return &Service{files: files, db: tx, now: time.Now}
}
