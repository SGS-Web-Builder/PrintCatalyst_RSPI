package dispatch

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

type SupplyReading struct {
	Name   string `json:"name"`
	Colour string `json:"colour"`
	Type   string `json:"type"`
	Level  *int   `json:"level"`
}
type PrinterHealth struct {
	State     string          `json:"state"`
	Reasons   []string        `json:"reasons"`
	Message   string          `json:"message"`
	Model     string          `json:"model"`
	Queued    *int            `json:"queued"`
	Accepting *bool           `json:"accepting"`
	Supplies  []SupplyReading `json:"supplies"`
	CheckedAt int64           `json:"checkedAt"`
	Source    string          `json:"source"`
}

// Only queries the local scheduler, never an arbitrary URL from a remote browser.
func (b *CUPSBackend) PrinterHealth(ctx context.Context, queue string) (PrinterHealth, error) {
	h := PrinterHealth{State: "unavailable", CheckedAt: time.Now().Unix(), Source: "CUPS/IPP", Supplies: []SupplyReading{}, Reasons: []string{}}
	if !cupsQueue.MatchString(queue) {
		return h, errors.New("not a local CUPS queue")
	}
	path := "/printers/" + queue
	a, err := b.request(ctx, 11, path, []ippAttribute{ippText("printer-uri", 0x45, "ipp://localhost:631"+path), ippText("requested-attributes", 0x44, "all")}, nil, nil)
	if err != nil {
		return h, err
	}
	return healthFromAttributes(a, h), nil
}
func healthFromAttributes(a map[string][]ippValue, h PrinterHealth) PrinterHealth {
	texts := func(key string) []string {
		out := []string{}
		for _, v := range a[key] {
			if v.tag >= 0x40 && v.tag <= 0x49 {
				out = append(out, string(v.value))
			}
		}
		return out
	}
	first := func(key string) string {
		v := texts(key)
		if len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if n, ok := oneNumber(a, "printer-state", 0x23); ok {
		switch n {
		case 3:
			h.State = "idle"
		case 4:
			h.State = "printing"
		case 5:
			h.State = "stopped"
		}
	}
	h.Reasons = texts("printer-state-reasons")
	h.Message = first("printer-state-message")
	h.Model = first("printer-make-and-model")
	if n, ok := oneNumber(a, "queued-job-count", 0x21); ok && n <= 2147483647 {
		v := int(n)
		h.Queued = &v
	}
	if v := a["printer-is-accepting-jobs"]; len(v) == 1 && v[0].tag == 0x22 && len(v[0].value) == 1 {
		b := v[0].value[0] == 1
		h.Accepting = &b
	}
	// CUPS marker arrays are positional. Missing/sentinel values remain unknown.
	names := texts("marker-names")
	colours := texts("marker-colors")
	types := texts("marker-types")
	levels := a["marker-levels"]
	if len(names) == 1 && strings.Contains(names[0], ",") {
		names = strings.Split(names[0], ",")
	}
	if len(colours) == 1 && strings.Contains(colours[0], ",") {
		colours = strings.Split(colours[0], ",")
	}
	if len(types) == 1 && strings.Contains(types[0], ",") {
		types = strings.Split(types[0], ",")
	}
	h.Supplies = []SupplyReading{}
	for i, name := range names {
		r := SupplyReading{Name: name}
		if len(colours) == len(names) {
			r.Colour = colours[i]
		}
		if len(types) == len(names) {
			r.Type = types[i]
		}
		if len(levels) == len(names) && levels[i].tag == 0x21 && len(levels[i].value) == 4 {
			n := int(int32(binary.BigEndian.Uint32(levels[i].value)))
			if n >= 0 && n <= 100 {
				r.Level = &n
			}
		}
		h.Supplies = append(h.Supplies, r)
	}
	return h
}
