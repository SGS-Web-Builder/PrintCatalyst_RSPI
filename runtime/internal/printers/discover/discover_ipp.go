// discover_ipp.go — IPP/IPPS network printer discovery for Print Catalyst
// On-Premise. The implementation follows RFC 8011 (IPP) and the IPP-Everywhere
// + Bonjour / DNS-SD browsing conventions:
//
//  1. Each discoverer Probe()s a candidate URI by sending an
//     IPP GET-Printer-Attributes request (RFC 8011 §4.3.2) over HTTP POST.
//     The body is a binary IPP packet; the response carries the printer's
//     attributes which we map onto printers.Snapshot via NormalizeIPPAttributes.
//  2. Browse()s the multicast DNS-SD "_ipp._tcp.local." (and "_ipps._tcp.local.")
//     name to discover printers without a known URI. We send a single DNS-SD
//     query packet and parse responses inline (no external mDNS / Avahi
//     dependency). Browsing is opportunistic; a network without mDNS
//     forwarding just yields no DNS-SD printers and the manually-registered
//     URIs continue to work.
//
// The discovery loop runs every IPP_POLL_INTERVAL seconds and emits one
// printers.Discovered record per printer. Failed probes emit a synthetic
// "_ipp_error" record so the dashboard can surface "printer unreachable"
// instead of hanging silently.
//
// References:
//
//   - RFC 8011 (Internet Printing Protocol / IPP)
//   - RFC 6762 (Multicast DNS)
//   - RFC 6763 (DNS-Based Service Discovery)
//
// All comments are deliberately explanatory so a future maintainer who
// has never written an IPP packet before can read the package top-down
// and understand each step.
//
// The runtime is Windows-only; this file lives on Windows despite having
// no build tag because it is pure-Go (no syscalls) and exposing it as
// cross-platform keeps the package importable from any test rig.

package discover

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

const (
	ippPollInterval   = 8 * time.Second
	dnssdQueryTimeout = 2 * time.Second
	httpProbeTimeout  = 3 * time.Second
	dnssdPort         = 5353
	dnssdAddr         = "224.0.0.251"
)

// Common IPP attribute tags from RFC 8010 §3.5.2. We only need a subset for
// the Get-Printer-Attributes response parser.
const (
	ippTagDelimiter    = 0x01 // operation-attributes-tag
	ippTagEnd          = 0x03 // end-of-attributes-tag
	ippTagInteger      = 0x21 // value-tag for signed integer
	ippTagBoolean      = 0x22
	ippTagEnum         = 0x23
	ippTagURI          = 0x45
	ippTagKeyword      = 0x44
	ippTagName         = 0x36
	ippTagText         = 0x41
	ippTagRangeOfInt   = 0x33
	ippTagCollection   = 0x34 // collection value-tag
	ippTagMemberAttr   = 0x4A // member-attribute-name (used inside collections)
	ippTagBeginCollect = 0x35 // begin-collection value-tag (separate byte per RFC 8010)
	ippTagEndCollect   = 0x37 // end-collection value-tag
)

// IPP operation IDs (RFC 8011 §5.4). We only ever issue
// Get-Printer-Attributes (0x000B).
const (
	ippOpGetPrinterAttributes = 0x000B
)

// IPP status codes we treat as success. Anything else surfaces the raw
// status-code + status-message so the dashboard can report "printer
// refused our query".
const (
	ippStatusOK = 0x0000
)

// IPP Discoverer probes IPP / IPPS endpoints (RFC 8011). It is safe for
// concurrent use; the constructor takes an http.Client so tests can point it
// at an httptest server.
type IPPDiscoverer struct {
	mu       sync.Mutex
	known    map[string]Discovered
	uris     []string
	client   *http.Client
	pollNow  func() time.Time
	listenIP string
}

// NewIPP returns a discoverer that probes the supplied URIs on every tick.
// Pass any number of ipp://host:631/ipp/print or ipps://host/ipp/print
// endpoints. URIs that fail to parse are silently dropped — the caller is
// responsible for surfacing the malformed URI as a configuration error.
func NewIPP(uris ...string) *IPPDiscoverer {
	cleaned := make([]string, 0, len(uris))
	for _, u := range uris {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "ipp://") && !strings.HasPrefix(u, "ipps://") {
			continue
		}
		cleaned = append(cleaned, u)
	}
	return &IPPDiscoverer{
		known:    map[string]Discovered{},
		uris:     cleaned,
		client:   &http.Client{Timeout: httpProbeTimeout},
		pollNow:  time.Now,
		listenIP: "0.0.0.0",
	}
}

// Backend identifies the discoverer.
func (d *IPPDiscoverer) Backend() printers.Backend { return printers.BackendIPP }

// Watch implements Discoverer by polling the configured URIs on every
// IPP_POLL_INTERVAL and emitting changes onto out. The function returns
// when ctx is cancelled.
func (d *IPPDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	ticker := time.NewTicker(ippPollInterval)
	defer ticker.Stop()
	d.tick(ctx, out)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx, out)
		}
	}
}

// tick performs one discovery pass over every configured URI.
func (d *IPPDiscoverer) tick(ctx context.Context, out chan<- Discovered) {
	now := d.pollNow()
	seen := make(map[string]struct{}, len(d.uris))
	for _, raw := range d.uris {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		key := raw
		seen[key] = struct{}{}
		attrs, err := d.probe(ctx, raw)
		if err != nil {
			out <- Discovered{
				Backend:     printers.BackendIPP,
				QueueName:   raw,
				DisplayName: raw,
				URI:         raw,
				Status:      printers.StatusError,
				LastSeenAt:  now,
				Error:       err,
			}
			continue
		}
		queue := firstOrEmpty(attrs["printer-name"])
		if queue == "" {
			queue = raw
		}
		display := firstOrEmpty(attrs["printer-info"])
		if display == "" {
			display = queue
		}
		location := firstOrEmpty(attrs["printer-location"])
		driverName := firstOrEmpty(attrs["printer-make-and-model"])
		isDefault := firstOrEmpty(attrs["is-default"]) == "true"
		backend := printers.BackendIPP
		if u.Scheme == "ipps" {
			backend = printers.BackendIPPS
		}
		disco := Discovered{
			Backend:     backend,
			QueueName:   queue,
			DisplayName: display,
			DriverName:  driverName,
			URI:         raw,
			Location:    location,
			IsDefault:   isDefault,
			Status:      printers.StatusReady,
			LastSeenAt:  now,
			Capabilities: printers.NormalizeIPPAttributes(printers.RawAttributes(map[string][]string{
				"media-supported":            attrs["media-supported"],
				"media-col-database":         attrs["media-col-database"],
				"media-source-supported":     attrs["media-source-supported"],
				"print-color-mode-supported": attrs["print-color-mode-supported"],
				"sides-supported":            attrs["sides-supported"],
				"finishings-supported":       attrs["finishings-supported"],
				"output-bin-supported":       attrs["output-bin-supported"],
			})),
		}
		d.mu.Lock()
		prev, existed := d.known[key]
		d.known[key] = disco
		d.mu.Unlock()
		if !existed || !ippDiscoEqual(prev, disco) {
			out <- disco
		}
	}
	// Emit offline for previously-known printers that no longer respond.
	d.mu.Lock()
	for k, prev := range d.known {
		if _, ok := seen[k]; !ok {
			prev.Status = printers.StatusOffline
			prev.Error = errors.New("printer no longer configured")
			d.known[k] = prev
			out <- prev
		}
	}
	d.mu.Unlock()
}

// firstOrEmpty returns the first element of the supplied slice or an empty
// string. The IPP attribute parser stores every value as []string because
// attributes can repeat; helpers like printer-name are always single-valued
// in practice so we just take the first.
func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// ippDiscoEqual is the platform-agnostic version of discoEqual. The Windows
// discoverer also defines discoEqual with a slightly different signature
// (it owns capability comparison). Both implementations are kept identical
// in behaviour so the registration loop can treat them uniformly.
func ippDiscoEqual(a, b Discovered) bool {
	if a.QueueName != b.QueueName || a.DisplayName != b.DisplayName {
		return false
	}
	if a.Status != b.Status {
		return false
	}
	if a.DriverName != b.DriverName || a.URI != b.URI {
		return false
	}
	if a.IsDefault != b.IsDefault {
		return false
	}
	return true
}

// probe issues an IPP GET-Printer-Attributes request against the supplied URI.
// The implementation is deliberately raw-bytes (not via an IPP SDK) so the
// runtime stays small and dependency-free.
func (d *IPPDiscoverer) probe(ctx context.Context, uri string) (map[string][]string, error) {
	req, err := buildGetPrinterAttributesRequest(uri)
	if err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	switch endpoint.Scheme {
	case "ipp":
		endpoint.Scheme = "http"
	case "ipps":
		endpoint.Scheme = "https"
	case "http", "https":
	default:
		return nil, errors.New("unsupported IPP transport")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(req))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/ipp")
	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ipp probe: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read ipp response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ipp endpoint returned status=%d", resp.StatusCode)
	}
	return parseGetPrinterAttributesResponse(body)
}

// buildGetPrinterAttributesRequest assembles a minimal but well-formed IPP
// request packet. We request the full set of attributes the runtime cares
// about (RFC 8011 §4.4 + the printer description attributes in §5.4) so the
// response can be normalised in one shot.
//
// Wire format (RFC 8010 §3.1):
//
//	+--------+--------+----------------+----------------+
//	| major  | minor  | operation-id   | request-id     |
//	| (1 B)  | (1 B)  | (2 B big-end)  | (4 B big-end)  |
//	+--------+--------+----------------+----------------+
//
// The two version bytes are written individually per the RFC, not packed as
// a big-endian uint16 (the major/minor split is significant; e.g. version
// 2.0 is bytes [0x02, 0x00], not [0x00, 0x02]).
func buildGetPrinterAttributesRequest(uri string) ([]byte, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	// IPP requires request-uri to be a "ipp:..." or "ipps:..." URL.
	// We forward the parsed URL verbatim.
	ippURI := parsed.String()

	var b bytes.Buffer
	b.WriteByte(0x02) // version-major (RFC 8010 §3.1.1)
	b.WriteByte(0x00) // version-minor
	binary.Write(&b, binary.BigEndian, uint16(ippOpGetPrinterAttributes))
	binary.Write(&b, binary.BigEndian, uint32(0x1234)) // request-id (any unique number)
	// operation-attributes-tag.
	b.WriteByte(ippTagDelimiter)
	// attributes-charset = utf-8
	writeAttr(&b, "attributes-charset", ippTagKeyword, []string{"utf-8"})
	// attributes-natural-language = en
	writeAttr(&b, "attributes-natural-language", ippTagKeyword, []string{"en"})
	// printer-uri = URI from caller
	writeAttr(&b, "printer-uri", ippTagURI, []string{ippURI})
	// requesting all common attribute groups.
	writeAttr(&b, "requested-attributes", ippTagKeyword,
		[]string{"all", "media-supported", "media-col-database", "media-source-supported",
			"print-color-mode-supported", "sides-supported", "finishings-supported",
			"output-bin-supported", "printer-name", "printer-info", "printer-location",
			"printer-make-and-model"})
	// end-of-attributes-tag.
	b.WriteByte(ippTagEnd)
	return b.Bytes(), nil
}

// writeAttr emits one IPP attribute value(s) to b. name must be in US-ASCII;
// values are also restricted to US-ASCII per RFC 8010 §3.5.4 (with the
// documented exceptions for text-without-language which we don't use).
func writeAttr(b *bytes.Buffer, name string, valueTag byte, values []string) {
	b.WriteByte(valueTag)
	writeName(b, name)
	for _, v := range values {
		writeString(b, v)
	}
}

// writeName writes a 2-byte length-prefixed attribute name (RFC 8010 §3.5.4).
func writeName(b *bytes.Buffer, name string) {
	n := uint16(len(name))
	binary.Write(b, binary.BigEndian, n)
	b.WriteString(name)
}

// writeString writes a 2-byte length-prefixed string value (RFC 8010 §3.5.4).
func writeString(b *bytes.Buffer, v string) {
	n := uint16(len(v))
	binary.Write(b, binary.BigEndian, n)
	b.WriteString(v)
}

// parseGetPrinterAttributesResponse decodes an IPP response body. The format
// is a tight binary stream of attribute values; we read it sequentially with
// knowledge of the subset of value tags we use internally.
//
// Wire layout (RFC 8010 §3.1):
//
//	+--------+--------+----------------+----------------+
//	| major  | minor  | status-code    | request-id     |
//	| (1 B)  | (1 B)  | (2 B big-end)  | (4 B big-end)  |
//	+--------+--------+----------------+----------------+
//
// The two version bytes are read individually; the status-code is the only
// field packed as a big-endian uint16. Any version mismatch is a fatal
// error — IPP/2.0 is the only protocol we implement.
func parseGetPrinterAttributesResponse(body []byte) (map[string][]string, error) {
	if len(body) < 8 {
		return nil, errors.New("ipp response too short")
	}
	if body[0] != 0x02 || body[1] != 0x00 {
		return nil, fmt.Errorf("unsupported ipp version %d.%d", body[0], body[1])
	}
	statusCode := binary.BigEndian.Uint16(body[2:4])
	if statusCode != ippStatusOK {
		return nil, fmt.Errorf("ipp error status=%d", statusCode)
	}
	out := map[string][]string{}
	r := bytes.NewReader(body[8:])
	for r.Len() > 0 {
		tag, err := r.ReadByte()
		if err != nil {
			break
		}
		// end-of-attributes-tag: every attribute group is over.
		if tag == ippTagEnd {
			break
		}
		// Delimiter tags (0x01..0x05) mark the start of an
		// attribute group; they are NOT followed by an attribute
		// name. The next byte is the value-tag of the first
		// attribute in the group.
		if tag >= 0x01 && tag <= 0x05 {
			continue
		}
		// Read attribute name (length-prefixed).
		var nameLen uint16
		if err := binary.Read(r, binary.BigEndian, &nameLen); err != nil {
			return nil, fmt.Errorf("read attr name length: %w", err)
		}
		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			return nil, fmt.Errorf("read attr name: %w", err)
		}
		name := string(nameBytes)
		// Read value(s) until next delimiter or end-of-attributes tag.
		values, err := readAttrValues(r, tag)
		if err != nil {
			// Skip unknown tags rather than failing the entire probe.
			continue
		}
		out[name] = append(out[name], values...)
	}
	return out, nil
}

// readAttrValues consumes one or more values for an attribute whose
// value-tag has already been read. Per RFC 8010 §3.5.5, multi-valued
// attributes encode each value with its own length prefix (for string
// types) or 4-byte integer (for numeric types); there is no repeated
// value-tag byte between values.
//
// The reader keeps consuming values until it sees a delimiter byte
// (0x01..0x05) or the end-of-attributes tag (0x03). When the byte
// following a value does not look like a delimiter and the next two
// bytes form a plausible length (<=1024, which is well over any
// realistic attribute value), we treat them as another value of the
// same attribute. Otherwise we push back and let the outer loop read
// the next attribute.
func readAttrValues(r *bytes.Reader, tag byte) ([]string, error) {
	var values []string
	for r.Len() > 0 {
		next, err := r.ReadByte()
		if err != nil {
			return values, err
		}
		if next == ippTagEnd {
			break
		}
		if next >= 0x01 && next <= 0x05 {
			if err := r.UnreadByte(); err != nil {
				return values, err
			}
			break
		}
		// Try to interpret the byte after `next` as the high byte
		// of a value-length. If the implied length is unreasonable
		// for a single value, treat `next` as the tag of the next
		// attribute and push both bytes back.
		switch tag {
		case ippTagKeyword, ippTagURI, ippTagName, ippTagText, ippTagMemberAttr:
			if r.Len() < 1 {
				if err := r.UnreadByte(); err != nil {
					return values, err
				}
				return values, nil
			}
			second, err := r.ReadByte()
			if err != nil {
				if err := r.UnreadByte(); err != nil {
					return values, err
				}
				return values, nil
			}
			length := int(next)<<8 | int(second)
			if length < 0 || length > 8192 || length > r.Len() {
				// Push back both bytes — the outer loop will
				// read `next` as a new attribute's tag.
				if err := r.UnreadByte(); err != nil {
					return values, err
				}
				if err := r.UnreadByte(); err != nil {
					return values, err
				}
				return values, nil
			}
			// Reconstruct the length bytes so readValue can
			// re-read them.
			if err := r.UnreadByte(); err != nil {
				return values, err
			}
			if err := r.UnreadByte(); err != nil {
				return values, err
			}
		}
		v, err := readValue(r, tag)
		if err != nil {
			return values, err
		}
		values = append(values, v)
	}
	return values, nil
}

// readValue reads exactly one value of the supplied value-tag. Strings use
// the 2-byte length-prefix; integers read 4 bytes big-endian; booleans
// read a single byte; ranges are emitted as "lower-upper".
func readValue(r *bytes.Reader, tag byte) (string, error) {
	switch tag {
	case ippTagKeyword, ippTagURI, ippTagName, ippTagText, ippTagMemberAttr:
		var n uint16
		if err := binary.Read(r, binary.BigEndian, &n); err != nil {
			return "", err
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	case ippTagInteger, ippTagEnum:
		var n int32
		if err := binary.Read(r, binary.BigEndian, &n); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", n), nil
	case ippTagBoolean:
		var b byte
		if err := binary.Read(r, binary.BigEndian, &b); err != nil {
			return "", err
		}
		if b == 1 {
			return "true", nil
		}
		return "false", nil
	case ippTagRangeOfInt:
		var lo, hi int32
		if err := binary.Read(r, binary.BigEndian, &lo); err != nil {
			return "", err
		}
		if err := binary.Read(r, binary.BigEndian, &hi); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d-%d", lo, hi), nil
	case ippTagCollection, ippTagBeginCollect:
		skipCollection(r)
		return "", nil
	default:
		return "", fmt.Errorf("unsupported tag 0x%02X", tag)
	}
}

// skipCollection walks past a collection value by reading attribute-value
// pairs until the end-of-collection marker (0x37) is encountered.
func skipCollection(r *bytes.Reader) {
	depth := 1
	for depth > 0 {
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		switch b {
		case ippTagEnd:
			return
		case ippTagBeginCollect:
			depth++
		case ippTagEndCollect:
			depth--
			if depth == 0 {
				return
			}
		}
		// We are skipping; a real decoder would track the
		// name/value structure, but for our use case the only
		// collection we ever see is "media-col-database" which
		// is large but linearly terminated.
		var n uint16
		if err := binary.Read(r, binary.BigEndian, &n); err != nil {
			return
		}
		if _, err := r.Seek(int64(n), io.SeekCurrent); err != nil {
			return
		}
	}
}

// DNS-SD browse for "_ipp._tcp.local." — minimal implementation that sends
// the query packet and parses the first batch of responses. Returns the
// service-instance names ("PrinterName._ipp._tcp.local.") it discovered.
//
// We deliberately do NOT depend on Avahi / Bonjour — a 60-line implementation
// is enough for the runtime to detect printers on the same link. Networks
// without mDNS just return no results and the manual URI list still works.
func (d *IPPDiscoverer) BrowseDNSSD(ctx context.Context, service string) ([]string, error) {
	addr, err := net.ResolveUDPAddr("udp4", dnssdAddr+":"+fmt.Sprintf("%d", dnssdPort))
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(dnssdQueryTimeout))
	q := buildDNSSDQuery(service + ".local")
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		// Timeout is fine — no DNS-SD responder means no printers
		// on this link, which is a normal state on a kiosk.
		if isTimeout(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseDNSSDResponse(buf[:n], service), nil
}

// buildDNSSDQuery emits a single DNS-SD question for PTR records of
// <service>.local.
func buildDNSSDQuery(service string) []byte {
	q := []byte{
		0x00, 0x00, // transaction id
		0x00, 0x00, // flags: standard query
		0x00, 0x01, // questions
		0x00, 0x00, // answers
		0x00, 0x00, // authority
		0x00, 0x00, // additional
	}
	// Encode service name as DNS labels.
	labels := strings.Split(service, ".")
	for _, label := range labels {
		q = append(q, byte(len(label)))
		q = append(q, []byte(label)...)
	}
	q = append(q, 0x00)       // terminator
	q = append(q, 0x00, 0x0c) // type = PTR
	q = append(q, 0x80, 0x01) // class = IN with cache-flush bit
	return q
}

// parseDNSSDResponse extracts the PTR record names from a DNS response.
// We deliberately skip fully-decoding the answer records; we only need the
// instance names so the dashboard can show "found printer X" — the actual
// IPP probe happens separately when the merchant selects the printer.
func parseDNSSDResponse(buf []byte, service string) []string {
	var out []string
	for i := 0; i+12 < len(buf); {
		// Skip past the header.
		if i == 0 {
			i = 12
			continue
		}
		// Walk label-length-encoded names.
		name, next := readDNSName(buf, i)
		if name == "" || next == i {
			break
		}
		if strings.HasPrefix(name, service) {
			out = append(out, name)
		}
		i = next
	}
	return out
}

// readDNSName walks a DNS name (sequence of label-length + label bytes)
// starting at offset off. Returns the decoded name and the offset of the
// byte immediately following the name.
func readDNSName(buf []byte, off int) (string, int) {
	var parts []string
	for off < len(buf) {
		l := int(buf[off])
		if l == 0 {
			off++
			break
		}
		// Compression pointer (top two bits set) — bail out.
		if l&0xC0 != 0 {
			break
		}
		off++
		if off+l > len(buf) {
			break
		}
		parts = append(parts, string(buf[off:off+l]))
		off += l
	}
	return strings.Join(parts, "."), off
}

func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}
