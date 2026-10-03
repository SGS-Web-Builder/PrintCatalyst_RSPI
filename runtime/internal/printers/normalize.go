package printers

import (
	"strings"
)

// NormalizeIPPAttributes converts a flat map of IPP attribute keys to the
// value sets the spec requires (RFC 8011 §5.4) into a Snapshot. The
// normalizer is a pure function so it is exercised by fixture tests without
// needing a live printer.
//
// The following attribute families are recognized:
//
//   media-supported           → PaperSize list (custom sizes preserved)
//   media-col-database        → PaperSize list with explicit dimensions
//   media-source-supported    → Tray list (with "auto" → "tray-auto")
//   print-color-mode-supported → ColourModes
//   sides-supported           → SidesModes
//   finishings-supported      → Finishing (staple / punch)
//   output-bin-supported      → Finishing (output_bin)
//
// Unknown attributes are preserved in Snapshot.Raw so a future adapter can
// surface them without losing the original report.
func NormalizeIPPAttributes(attrs RawAttributes) *Snapshot {
	snap := &Snapshot{
		ColourModes: []string{},
		SidesModes:  []string{},
		PaperSizes:  []PaperSize{},
		Trays:       []Tray{},
		Finishing:   []FinishingOption{},
		Raw:         map[string]any{},
	}

	// Media-supported is a flat keyword list.
	for _, label := range attrs["media-supported"] {
		key := NormalizePaperKey(label)
		if key == "" {
			continue
		}
		if !containsPaperSize(snap.PaperSizes, key) {
			snap.PaperSizes = append(snap.PaperSizes, PaperSize{
				Key:      key,
				RawLabel: label,
			})
		}
	}

	// media-col-database entries look like
	//   media-col-database media-size x-dimension 21000 y-dimension 29700
	// We accept them as space-separated tokens.
	for _, entry := range attrs["media-col-database"] {
		size, ok := parseMediaColDatabase(entry)
		if !ok {
			continue
		}
		merged := upsertPaperSize(snap.PaperSizes, size)
		if merged.WidthMM == 0 && merged.HeightMM == 0 {
			// Driver gave us a structured entry but no dimensions; skip.
			_ = merged
		}
	}

	// Tray list.
	for _, label := range attrs["media-source-supported"] {
		trimmed := strings.TrimSpace(label)
		if trimmed == "" {
			continue
		}
		snap.Trays = append(snap.Trays, Tray{
			Key:      normalizeTrayKey(trimmed),
			RawLabel: trimmed,
		})
	}

	// Colour modes.
	for _, v := range attrs["print-color-mode-supported"] {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "monochrome", "mono", "gray", "grayscale":
			snap.ColourModes = append(snap.ColourModes, "monochrome")
		case "color", "colour", "rgb":
			snap.ColourModes = append(snap.ColourModes, "colour")
		}
	}

	// Sides.
	for _, v := range attrs["sides-supported"] {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "one-sided":
			snap.SidesModes = append(snap.SidesModes, "one-sided")
		case "two-sided-long-edge":
			snap.SidesModes = append(snap.SidesModes, "two-sided-long-edge")
		case "two-sided-short-edge":
			snap.SidesModes = append(snap.SidesModes, "two-sided-short-edge")
		}
	}

	// Finishings (staple, punch).
	for _, v := range attrs["finishings-supported"] {
		ft, key := normalizeFinishing(v)
		if ft == "" {
			continue
		}
		snap.Finishing = append(snap.Finishing, FinishingOption{Type: ft, Key: key, RawLabel: v})
	}

	// Output bins.
	for _, v := range attrs["output-bin-supported"] {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		snap.Finishing = append(snap.Finishing, FinishingOption{
			Type: FinishingOutputBin, Key: normalizeOutputBin(v), RawLabel: v,
		})
	}

	// Preserve unknown attributes for diagnostics.
	for k, v := range attrs {
		if isKnownIPPAttribute(k) {
			continue
		}
		snap.Raw[k] = append([]string(nil), v...)
	}

	snap.Fingerprint = CapabilityFingerprint(snap)
	return snap
}

func isKnownIPPAttribute(k string) bool {
	switch k {
	case "media-supported", "media-col-database", "media-source-supported",
		"print-color-mode-supported", "sides-supported",
		"finishings-supported", "finishings-col-database",
		"output-bin-supported":
		return true
	}
	return false
}

func containsPaperSize(list []PaperSize, key string) bool {
	for _, p := range list {
		if p.Key == key {
			return true
		}
	}
	return false
}

func upsertPaperSize(list []PaperSize, size PaperSize) PaperSize {
	for i, p := range list {
		if p.Key == size.Key {
			merged := p
			if merged.RawLabel == "" {
				merged.RawLabel = size.RawLabel
			}
			if merged.WidthMM == 0 {
				merged.WidthMM = size.WidthMM
			}
			if merged.HeightMM == 0 {
				merged.HeightMM = size.HeightMM
			}
			list[i] = merged
			return merged
		}
	}
	list = append(list, size)
	return size
}

// parseMediaColDatabase extracts a paper key + dimensions from one
// "media-col-database" entry. Tokens are space-separated; we look for
// "media-size" followed by "x-dimension" and "y-dimension" in micrometres.
func parseMediaColDatabase(entry string) (PaperSize, bool) {
	tokens := strings.Fields(entry)
	if len(tokens) == 0 {
		return PaperSize{}, false
	}
	key := ""
	widthMicrons := 0
	heightMicrons := 0
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "media-size", "media-label", "media":
			if i+1 < len(tokens) {
				key = NormalizePaperKey(tokens[i+1])
			}
		case "x-dimension":
			if i+1 < len(tokens) {
				widthMicrons = atoiSafe(tokens[i+1])
			}
		case "y-dimension":
			if i+1 < len(tokens) {
				heightMicrons = atoiSafe(tokens[i+1])
			}
		}
	}
	if key == "" {
		key = NormalizePaperKey(tokens[0])
	}
	if key == "" {
		return PaperSize{}, false
	}
	return PaperSize{
		Key:      key,
		RawLabel: entry,
		WidthMM:  widthMicrons / 100,
		HeightMM: heightMicrons / 100,
	}, true
}

func normalizeTrayKey(label string) string {
	cleaned := strings.ToLower(strings.TrimSpace(label))
	switch cleaned {
	case "", "auto", "default", "printer-default":
		return "tray-auto"
	case "manual", "bypass", "multi-purpose", "multi purpose tray", "mp tray":
		return "tray-bypass"
	}
	if strings.HasPrefix(cleaned, "tray ") {
		return "tray-" + strings.TrimPrefix(cleaned, "tray ")
	}
	return "tray-" + cleaned
}

// normalizeFinishing maps an IPP finishing keyword (or numeric enum from
// RFC 8011 §5.4.13) to a normalized type + key.
func normalizeFinishing(raw string) (FinishingType, string) {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	cleaned = strings.TrimPrefix(cleaned, "3:")
	switch cleaned {
	case "staple", "staple-top-left", "staple-top-right", "staple-bottom-left", "staple-bottom-right":
		return FinishingStaple, "staple"
	case "punch", "punch-2-hole", "punch-3-hole", "punch-4-hole":
		return FinishingPunch, "punch"
	case "3":
		// IPP enum shorthand for "staple".
		return FinishingStaple, "staple"
	case "4", "5", "6", "7", "8", "9":
		// 4 = punch (deprecated); 5–9 = punch patterns.
		return FinishingPunch, "punch"
	}
	return "", ""
}

func normalizeOutputBin(label string) string {
	cleaned := strings.ToLower(strings.TrimSpace(label))
	switch cleaned {
	case "face-down", "facedown":
		return "output-facedown"
	case "face-up", "faceup":
		return "output-faceup"
	}
	return "output-" + cleaned
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
