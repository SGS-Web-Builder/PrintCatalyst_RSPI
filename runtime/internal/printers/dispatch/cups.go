package dispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// CUPSBackend sends only prepared PDFs to the local CUPS scheduler. It does
// not follow redirects, use environment proxies, or retry Print-Job requests.
type CUPSBackend struct {
	client   *http.Client
	sequence atomic.Uint32
}

func NewCUPSBackend() *CUPSBackend {
	return &CUPSBackend{client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (b *CUPSBackend) Name() string { return "cups-ipp" }

var cupsQueue = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,126}$`)
var cupsJob = regexp.MustCompile(`^cups:([1-9][0-9]{0,9}):(pc-[0-9a-f]{16}-[0-9a-f]{16})$`)

func shortHash(s string) string          { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:8]) }
func jobTitle(order, line string) string { return "pc-" + shortHash(order) + "-" + shortHash(line) }

type ippValue struct {
	tag   byte
	value []byte
}
type ippAttribute struct {
	name  string
	tag   byte
	value []byte
}

func ippText(name string, tag byte, value string) ippAttribute {
	return ippAttribute{name, tag, []byte(value)}
}
func ippNumber(name string, value int) ippAttribute {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(value))
	return ippAttribute{name, 0x21, b}
}
func putAttribute(b *bytes.Buffer, a ippAttribute) {
	b.WriteByte(a.tag)
	_ = binary.Write(b, binary.BigEndian, uint16(len(a.name)))
	b.WriteString(a.name)
	_ = binary.Write(b, binary.BigEndian, uint16(len(a.value)))
	b.Write(a.value)
}
func (b *CUPSBackend) request(ctx context.Context, op uint16, path string, operation, job []ippAttribute, pdf []byte) (map[string][]ippValue, error) {
	id := b.sequence.Add(1)
	var packet bytes.Buffer
	packet.Write([]byte{2, 0})
	_ = binary.Write(&packet, binary.BigEndian, op)
	_ = binary.Write(&packet, binary.BigEndian, id)
	packet.WriteByte(1)
	putAttribute(&packet, ippText("attributes-charset", 0x47, "utf-8"))
	putAttribute(&packet, ippText("attributes-natural-language", 0x48, "en"))
	for _, a := range operation {
		putAttribute(&packet, a)
	}
	if len(job) > 0 {
		packet.WriteByte(2)
		for _, a := range job {
			putAttribute(&packet, a)
		}
	}
	packet.WriteByte(3)
	// MultiReader deliberately supplies no GetBody replay function.
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:631"+path, io.MultiReader(bytes.NewReader(packet.Bytes()), bytes.NewReader(pdf)))
	if err != nil {
		return nil, err
	}
	r.ContentLength = int64(packet.Len() + len(pdf))
	r.Header.Set("Content-Type", "application/ipp")
	response, err := b.client.Do(r)
	if err != nil {
		return nil, errors.New("CUPS request outcome unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("CUPS HTTP request rejected")
	}
	content, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || content != "application/ipp" {
		return nil, errors.New("invalid CUPS response type")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("invalid CUPS response size")
	}
	return parseCUPS(data, id)
}
func parseCUPS(data []byte, id uint32) (map[string][]ippValue, error) {
	bad := func() (map[string][]ippValue, error) {
		return nil, errors.New("invalid or unsuccessful CUPS IPP response")
	}
	if len(data) < 9 || (data[0] != 1 && data[0] != 2) || binary.BigEndian.Uint32(data[4:8]) != id {
		return bad()
	}
	// Attribute substitutions are unsafe: fidelity must be honoured exactly.
	if binary.BigEndian.Uint16(data[2:4]) != 0 {
		return bad()
	}
	values := map[string][]ippValue{}
	offset := 8
	name := ""
	for offset < len(data) {
		tag := data[offset]
		offset++
		if tag == 3 {
			if offset != len(data) {
				return bad()
			}
			return values, nil
		}
		if tag < 0x10 {
			name = ""
			continue
		}
		if offset+2 > len(data) {
			return bad()
		}
		n := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2
		if offset+n+2 > len(data) {
			return bad()
		}
		if n > 0 {
			name = string(data[offset : offset+n])
		}
		offset += n
		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2
		if name == "" || offset+length > len(data) {
			return bad()
		}
		values[name] = append(values[name], ippValue{tag, append([]byte(nil), data[offset:offset+length]...)})
		offset += length
	}
	return bad()
}
func oneText(values map[string][]ippValue, name string, tag byte) (string, bool) {
	v := values[name]
	if len(v) != 1 || v[0].tag != tag {
		return "", false
	}
	return string(v[0].value), true
}
func oneNumber(values map[string][]ippValue, name string, tag byte) (uint32, bool) {
	v := values[name]
	if len(v) != 1 || v[0].tag != tag || len(v[0].value) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(v[0].value), true
}
func (b *CUPSBackend) Submit(ctx context.Context, queue string, pdf []byte, ref DocumentRef) (string, error) {
	if !cupsQueue.MatchString(queue) || !ref.Prepared || ref.MIMEType != "application/pdf" || !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) > 50<<20 || ref.OrderID == "" || ref.LineID == "" || ref.Copies < 1 || ref.Copies > 999 || ref.PagesPerSheet != 1 || len(ref.Pages) != 0 || ref.PageStart != 0 || ref.PageEnd != 0 {
		return "", errors.New("CUPS requires a complete prepared PDF and frozen options")
	}
	if ref.ColourMode != "monochrome" && ref.ColourMode != "colour" {
		return "", errors.New("invalid CUPS colour")
	}
	if ref.Sides != "one-sided" && ref.Sides != "two-sided-long-edge" && ref.Sides != "two-sided-short-edge" {
		return "", errors.New("invalid CUPS sides")
	}
	media := map[string]string{"A3": "iso_a3_297x420mm", "A4": "iso_a4_210x297mm", "A5": "iso_a5_148x210mm", "A6": "iso_a6_105x148mm", "LETTER": "na_letter_8.5x11in", "LEGAL": "na_legal_8.5x14in"}[strings.ToUpper(ref.PaperSize)]
	if media == "" {
		media = ref.PaperSize
	}
	if media == "" || len(media) > 255 || strings.ContainsAny(media, "\x00\r\n") || len(ref.Tray) > 255 || strings.ContainsAny(ref.Tray, "\x00\r\n") {
		return "", errors.New("invalid CUPS media")
	}
	path := "/printers/" + queue
	title := jobTitle(ref.OrderID, ref.LineID)
	colour := ref.ColourMode
	if colour == "colour" {
		colour = "color"
	}
	op := []ippAttribute{ippText("printer-uri", 0x45, "ipp://localhost:631"+path), ippText("requesting-user-name", 0x42, "printcatalyst-kiosk"), ippText("job-name", 0x42, title), ippText("document-format", 0x49, "application/pdf"), {name: "ipp-attribute-fidelity", tag: 0x22, value: []byte{1}}}
	job := []ippAttribute{ippNumber("copies", ref.Copies), ippText("sides", 0x44, ref.Sides), ippText("media", 0x44, media), ippText("print-color-mode", 0x44, colour), ippText("print-scaling", 0x44, "none"), ippNumber("number-up", 1)}
	if ref.Tray != "" {
		// Tray is a member of the media-col collection, not a top-level
		// job attribute. Include the same frozen paper within that collection.
		job = append(job,
			ippAttribute{name: "media-col", tag: 0x34},
			ippText("", 0x4a, "media-size-name"), ippText("", 0x44, media),
			ippText("", 0x4a, "media-source"), ippText("", 0x44, ref.Tray),
			ippAttribute{tag: 0x37},
		)
	}
	result, err := b.request(ctx, 2, path, op, job, pdf)
	if err != nil {
		return "", err
	}
	id, ok := oneNumber(result, "job-id", 0x21)
	if !ok || id == 0 || id > 2147483647 {
		return "", errors.New("CUPS did not return a valid job identity; review required")
	}
	return fmt.Sprintf("cups:%d:%s", id, title), nil
}
func (b *CUPSBackend) Queues(ctx context.Context) ([]string, error) {
	result, err := b.request(ctx, 0x4002, "/", []ippAttribute{ippText("requested-attributes", 0x44, "printer-name")}, nil, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range result["printer-name"] {
		if v.tag == 0x42 && cupsQueue.MatchString(string(v.value)) {
			out = append(out, string(v.value))
		}
	}
	return out, nil
}
func (b *CUPSBackend) JobProgress(ctx context.Context, queue, id, order string) (string, string, error) {
	match := cupsJob.FindStringSubmatch(id)
	if match == nil || !cupsQueue.MatchString(queue) || !strings.HasPrefix(match[2], "pc-"+shortHash(order)+"-") {
		return "review", "Job identity requires review", nil
	}
	number, err := strconv.ParseInt(match[1], 10, 32)
	if err != nil {
		return "review", "Job identity requires review", nil
	}
	op := []ippAttribute{ippText("printer-uri", 0x45, "ipp://localhost:631/printers/"+queue), ippNumber("job-id", int(number)), ippText("requested-attributes", 0x44, "job-state"), ippText("", 0x44, "job-name"), ippText("", 0x44, "job-printer-uri"), ippText("", 0x44, "job-state-reasons")}
	values, err := b.request(ctx, 9, "/jobs/", op, nil, nil)
	if err != nil {
		return "blocked", "Cannot confirm CUPS job state; do not resubmit", err
	}
	title, ok := oneText(values, "job-name", 0x42)
	if !ok || title != match[2] {
		return "review", "CUPS job identity changed", nil
	}
	uri, ok := oneText(values, "job-printer-uri", 0x45)
	u, e := url.Parse(uri)
	if !ok || e != nil || u.Path != "/printers/"+queue {
		return "review", "CUPS job printer changed", nil
	}
	state, ok := oneNumber(values, "job-state", 0x23)
	if !ok {
		return "review", "CUPS state missing", nil
	}
	for _, v := range values["job-state-reasons"] {
		reason := string(v.value)
		if strings.Contains(reason, "completed-with-errors") || strings.Contains(reason, "aborted") {
			return "review", "CUPS reports output errors", nil
		}
	}
	switch state {
	case 3:
		return "pending", "Waiting in CUPS", nil
	case 4, 6:
		return "blocked", "CUPS job held or stopped", nil
	case 5:
		return "processing", "CUPS is processing the job", nil
	case 7, 8:
		return "review", "CUPS job cancelled or aborted; check output", nil
	case 9:
		return "completed", "CUPS reports job completed", nil
	}
	return "review", "Unknown CUPS job state", nil
}
