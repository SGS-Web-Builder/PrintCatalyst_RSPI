package dispatch

import (
	"encoding/binary"
	"testing"
)

func TestPrinterHealthUnknownAndReportedSupplies(t *testing.T) {
	num := func(n int) ippValue {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(n))
		return ippValue{0x21, b}
	}
	attrs := map[string][]ippValue{"marker-names": {{0x41, []byte("Black")}, {0x41, []byte("Maintenance box")}}, "marker-levels": {num(68), num(-1)}, "queued-job-count": {num(0)}}
	h := healthFromAttributes(attrs, PrinterHealth{State: "unavailable"})
	if len(h.Supplies) != 2 || h.Supplies[0].Level == nil || *h.Supplies[0].Level != 68 || h.Supplies[1].Level != nil || h.State != "unavailable" {
		t.Fatalf("invented telemetry: %+v", h)
	}
	attrs["marker-levels"] = []ippValue{num(50)}
	h = healthFromAttributes(attrs, PrinterHealth{})
	for _, s := range h.Supplies {
		if s.Level != nil {
			t.Fatal("misaligned supplies must remain unknown")
		}
	}
}
