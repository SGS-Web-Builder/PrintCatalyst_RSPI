package dispatch

import (
	"errors"
	"strings"
)

func wrapInvoice(text string, width int, measure func(string) (int, error)) ([]string, error) {
	var lines []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		var line []rune
		for _, r := range paragraph {
			if r < ' ' {
				r = ' '
			}
			next := append(append([]rune(nil), line...), r)
			w, err := measure(string(next))
			if err != nil {
				return nil, err
			}
			if w > width && len(line) == 0 {
				return nil, errors.New("invoice paper is too narrow for readable text")
			}
			if w > width && len(line) > 0 {
				lines = append(lines, string(line))
				line = []rune{r}
			} else {
				line = next
			}
		}
		lines = append(lines, string(line))
	}
	return lines, nil
}
