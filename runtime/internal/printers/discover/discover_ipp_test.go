//go:build !windows

package discover

import (
	"testing"
)

func TestIPPDiscovererRoundTrip(t *testing.T) {
	d := NewIPP("ipp://127.0.0.1:631/printers/test")
	if d.Backend() != "ipp" {
		t.Fatalf("backend = %s", d.Backend())
	}
}

// TestIPPRequestWireFormat checks that buildGetPrinterAttributesRequest
// emits a packet with the documented layout (RFC 8010 §3.1):
//
//	+--------+--------+----------------+----------------+
//	| major  | minor  | operation-id   | request-id     |
//	+--------+--------+----------------+----------------+
//
// followed by an operation-attributes-tag (0x01) and ending with
// end-of-attributes-tag (0x03).
func TestIPPRequestWireFormat(t *testing.T) {
	pkt, err := buildGetPrinterAttributesRequest("ipp://127.0.0.1:631/printers/test")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) < 8 {
		t.Fatalf("packet too small: %d bytes", len(pkt))
	}
	if pkt[0] != 0x02 {
		t.Errorf("version-major byte = %#02x, want 0x02", pkt[0])
	}
	if pkt[1] != 0x00 {
		t.Errorf("version-minor byte = %#02x, want 0x00", pkt[1])
	}
	op := uint16(pkt[2])<<8 | uint16(pkt[3])
	if op != ippOpGetPrinterAttributes {
		t.Errorf("op = %#04x, want %#04x", op, ippOpGetPrinterAttributes)
	}
	// First byte after the 8-byte header is the operation-attributes-tag.
	if pkt[8] != ippTagDelimiter {
		t.Errorf("tag at offset 8 = %#02x, want operation-attributes-tag %#02x", pkt[8], ippTagDelimiter)
	}
	if pkt[len(pkt)-1] != ippTagEnd {
		t.Errorf("last byte = %#02x, want end-of-attributes tag %#02x", pkt[len(pkt)-1], ippTagEnd)
	}
}

// TestIPPResponseParser checks that a minimal but well-formed Get-Printer-Attributes
// response is decoded into the attribute map the rest of the runtime expects.
//
// The packet layout follows RFC 8010 §3.5: 8-byte header, then a single
// attribute group (no operation-attributes-tag at this layer — the parser
// treats it as a delimiter that starts a new group).
func TestIPPResponseParser(t *testing.T) {
	pkt := []byte{
		// version 2.0
		0x02, 0x00,
		// status = OK
		0x00, 0x00,
		// request-id
		0x00, 0x00, 0x12, 0x34,
		// attributes-charset = utf-8
		0x44, // keyword tag
		0x00, 0x12, 'a', 't', 't', 'r', 'i', 'b', 'u', 't', 'e', 's', '-', 'c', 'h', 'a', 'r', 's', 'e', 't',
		0x00, 0x05, 'u', 't', 'f', '-', '8',
		// attributes-natural-language = en
		0x44,
		0x00, 0x1b, 'a', 't', 't', 'r', 'i', 'b', 'u', 't', 'e', 's', '-', 'n', 'a', 't', 'u', 'r', 'a', 'l', '-', 'l', 'a', 'n', 'g', 'u', 'a', 'g', 'e',
		0x00, 0x02, 'e', 'n',
		// end-of-attributes-tag
		0x03,
	}
	attrs, err := parseGetPrinterAttributesResponse(pkt)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if got := attrs["attributes-charset"]; len(got) != 1 || got[0] != "utf-8" {
		t.Errorf("attributes-charset = %v", got)
	}
	if got := attrs["attributes-natural-language"]; len(got) != 1 || got[0] != "en" {
		t.Errorf("attributes-natural-language = %v", got)
	}
}

// TestIPPResponseParserWithDelimiter ensures the parser tolerates the
// operation-attributes-tag delimiter byte at the start of the body.
func TestIPPResponseParserWithDelimiter(t *testing.T) {
	pkt := []byte{
		0x02, 0x00,
		0x00, 0x00,
		0x00, 0x00, 0x12, 0x34,
		// operation-attributes-tag (delimiter)
		0x01,
		// printer-name = "TestPrinter"
		0x44,
		0x00, 0x0c, 'p', 'r', 'i', 'n', 't', 'e', 'r', '-', 'n', 'a', 'm', 'e',
		0x00, 0x0b, 'T', 'e', 's', 't', 'P', 'r', 'i', 'n', 't', 'e', 'r',
		0x03,
	}
	attrs, err := parseGetPrinterAttributesResponse(pkt)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if got := attrs["printer-name"]; len(got) != 1 || got[0] != "TestPrinter" {
		t.Errorf("printer-name = %v", got)
	}
}

// TestIPPResponseRejectsBadStatus ensures a non-zero status returns an error.
func TestIPPResponseRejectsBadStatus(t *testing.T) {
	pkt := []byte{
		0x02, 0x00,             // version
		0x00, 0x06,             // status = 0x0006 (server-error-busy)
		0x00, 0x00, 0x12, 0x34, // request-id
	}
	_, err := parseGetPrinterAttributesResponse(pkt)
	if err == nil {
		t.Fatal("expected an error for non-zero status")
	}
}

// TestDNSNameReader covers the label-length-decoder used by parseDNSSDResponse.
func TestDNSNameReader(t *testing.T) {
	buf := []byte{
		4, 't', 'e', 's', 't',
		9, '_', 'i', 'p', 'p', '.', '_', 't', 'c', 'p',
		0x00,
	}
	got, off := readDNSName(buf, 0)
	want := "test._ipp._tcp"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if off != len(buf) {
		t.Errorf("consumed %d bytes, want %d", off, len(buf))
	}
}

// TestIPPDiscovererFiltersBadURIs ensures malformed entries are dropped
// instead of producing a garbage discoverer.
func TestIPPDiscovererFiltersBadURIs(t *testing.T) {
	d := NewIPP("", "  ", "ftp://nope", "ipp://valid/printers/test", "ipps://also-valid")
	if got := len(d.uris); got != 2 {
		t.Fatalf("kept %d uris, want 2 (got: %v)", got, d.uris)
	}
}
