package pageselection

import (
	"encoding/json"
	"fmt"
)

// Resolve preserves source-page order and rejects invalid or duplicate pages.
// A nil selection is the legacy contiguous range; an empty selection is invalid.
func Resolve(selected []int, start, end, total int) ([]int, error) {
	if total < 1 || start < 1 || end < start || end > total {
		return nil, fmt.Errorf("invalid page range")
	}
	if selected == nil {
		selected = make([]int, end-start+1)
		for i := range selected {
			selected[i] = start + i
		}
	}
	if len(selected) == 0 || len(selected) > total {
		return nil, fmt.Errorf("select at least one valid page")
	}
	previous := 0
	for _, page := range selected {
		if page < 1 || page > total || page <= previous {
			return nil, fmt.Errorf("selected pages must be unique, ascending and within 1–%d", total)
		}
		previous = page
	}
	return selected, nil
}
func Encode(pages []int) string { b, _ := json.Marshal(pages); return string(b) }
func Decode(raw string) ([]int, error) {
	var pages []int
	err := json.Unmarshal([]byte(raw), &pages)
	return pages, err
}
