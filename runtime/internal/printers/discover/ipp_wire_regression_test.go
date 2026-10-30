package discover

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRequestAdditionalValuesHaveTagsAndEmptyNames(t *testing.T) {
	p, e := buildGetPrinterAttributesRequest("ipp://127.0.0.1:631/printers/CanonLBP2900")
	if e != nil {
		t.Fatal(e)
	}
	r := bytes.NewReader(p[9:])
	previous := ""
	count := 0
	for {
		tag, e := r.ReadByte()
		if e != nil {
			t.Fatal(e)
		}
		if tag == 3 {
			if r.Len() != 0 {
				t.Fatal("trailing bytes")
			}
			break
		}
		if tag < 0x10 {
			t.Fatalf("invalid tag %x", tag)
		}
		var n uint16
		if e = binary.Read(r, binary.BigEndian, &n); e != nil {
			t.Fatal(e)
		}
		name := make([]byte, n)
		if _, e = r.Read(name); e != nil && n > 0 {
			t.Fatal(e)
		}
		if n > 0 {
			previous = string(name)
		}
		if previous == "attributes-charset" && tag != 0x47 {
			t.Fatal("charset tag")
		}
		if previous == "attributes-natural-language" && tag != 0x48 {
			t.Fatal("language tag")
		}
		if e = binary.Read(r, binary.BigEndian, &n); e != nil || int(n) > r.Len() {
			t.Fatal("bad value length")
		}
		r.Seek(int64(n), 1)
		if previous == "requested-attributes" {
			count++
		}
	}
	if count < 10 {
		t.Fatal("missing requested capabilities", count)
	}
}
func TestRealIPPValuesAndCollections(t *testing.T) {
	p := []byte{2, 0, 0, 0, 0, 0, 0x12, 0x34, 4}
	add := func(tag byte, name string, value []byte) {
		p = append(p, tag, byte(len(name)>>8), byte(len(name)))
		p = append(p, []byte(name)...)
		p = append(p, byte(len(value)>>8), byte(len(value)))
		p = append(p, value...)
	}
	add(0x44, "media-supported", []byte("iso_a4_210x297mm"))
	add(0x44, "", []byte("iso_a5_148x210mm"))
	add(0x34, "media-col-database", nil)
	add(0x4a, "", []byte("media-size"))
	add(0x34, "", nil)
	add(0x4a, "", []byte("x-dimension"))
	add(0x21, "", []byte{0, 0, 0x52, 8})
	add(0x37, "", nil)
	add(0x37, "", nil)
	add(0x22, "color-supported", []byte{0})
	add(0x23, "printer-state", []byte{0, 0, 0, 3})
	add(0x44, "sides-supported", []byte("one-sided"))
	p = append(p, 3)
	a, e := parseGetPrinterAttributesResponse(p)
	if e != nil {
		t.Fatal(e)
	}
	if len(a["media-supported"]) != 2 || a["color-supported"][0] != "false" || a["printer-state"][0] != "3" || a["sides-supported"][0] != "one-sided" {
		t.Fatal(a)
	}
	for _, cut := range []int{8, len(p) - 2} {
		if _, e = parseGetPrinterAttributesResponse(p[:cut]); e == nil {
			t.Fatal("accepted truncated response")
		}
	}
}
