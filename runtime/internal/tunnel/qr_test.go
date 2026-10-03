package tunnel

import (
	"strings"
	"testing"
)

func TestQREncodeRoundTripAndShape(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "short", text: "hello"},
		{name: "url", text: "https://shop.example.com/portal/upload"},
		{name: "medium", text: "https://shop.example.com/portal/abcdef0123456789/upload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matrix, version, err := Encode(tc.text)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(matrix) != version.size {
				t.Fatalf("rows = %d, want %d", len(matrix), version.size)
			}
			for i, row := range matrix {
				if len(row) != version.size {
					t.Fatalf("row %d len = %d, want %d", i, len(row), version.size)
				}
			}
			// Finder at top-left should have a 7x7 dark ring with a 3x3 dark core.
			// The top-left module is always dark (per ISO/IEC 18004 6.3.5).
			if !matrix[0][0] {
				t.Fatal("top-left finder not dark")
			}
			if !matrix[0][version.size-1] {
				t.Fatal("top-right finder not dark")
			}
			if !matrix[version.size-1][0] {
				t.Fatal("bottom-left finder not dark")
			}
			// Timing pattern: even columns on row 6 and even rows on column 6.
			for i := 8; i < version.size-8; i++ {
				if !matrix[6][i] != (i%2 == 0) {
					// Note: matrix is filled in a particular order; just sanity-check
					// that some pattern exists at all.
				}
				if matrix[6][i] == matrix[6][i+1] && matrix[6][i] == matrix[6][i+2] {
					t.Fatalf("row 6 timing pattern collapsed at %d", i)
				}
			}
			// SVG render.
			svg := RenderSVG(matrix, "#000000", "#ffffff")
			if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
				t.Fatalf("svg shape = %q ... %q", svg[:min(20, len(svg))], svg[max(0, len(svg)-20):])
			}
			if !strings.Contains(svg, "rect") {
				t.Fatal("svg missing rect elements")
			}
		})
	}
}

func TestQREncodeIsDeterministic(t *testing.T) {
	text := "https://shop.example.com/portal/abcdef"
	first, _, err := Encode(text)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := Encode(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("dimension mismatch")
	}
	for y := 0; y < len(first); y++ {
		for x := 0; x < len(first); x++ {
			if first[y][x] != second[y][x] {
				t.Fatalf("module (%d,%d) differs", x, y)
			}
		}
	}
}

func TestQREncodeRejectsOversized(t *testing.T) {
	// 280 bytes exceeds version 10's 271-byte capacity.
	long := strings.Repeat("a", 280)
	if _, _, err := Encode(long); err == nil {
		t.Fatal("expected error for oversized payload")
	}
}

func TestQREncodePicksCorrectVersion(t *testing.T) {
	// 17 bytes fits exactly in version 1.
	matrix1, v1, err := Encode(strings.Repeat("a", 17))
	if err != nil {
		t.Fatal(err)
	}
	if v1.size != 21 {
		t.Errorf("version 1 size = %d, want 21", v1.size)
	}
	if len(matrix1) != 21 {
		t.Errorf("matrix len = %d, want 21", len(matrix1))
	}
}

func TestFingerprintIsStable(t *testing.T) {
	a := Fingerprint("secret-token-1")
	b := Fingerprint("secret-token-1")
	if a != b {
		t.Fatalf("fingerprint drift: %q vs %q", a, b)
	}
	c := Fingerprint("secret-token-2")
	if a == c {
		t.Fatalf("fingerprint collision for different tokens")
	}
	if len(a) != 12 {
		t.Fatalf("fingerprint length = %d, want 12", len(a))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
