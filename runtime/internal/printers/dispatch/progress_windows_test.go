//go:build windows

package dispatch

import "testing"

func TestWindowsJobState(t *testing.T) {
	for _, tc := range []struct {
		flags uint32
		want  string
	}{{0, "pending"}, {1, "pending"}, {8, "processing"}, {16, "printing"}, {4096, "printing"}, {128, "completed"}, {2, "blocked"}, {32, "blocked"}, {64, "blocked"}, {512, "blocked"}, {1024, "blocked"}, {4, "review"}, {256, "review"}, {128 | 2, "blocked"}} {
		got, _ := windowsJobState(tc.flags)
		if got != tc.want {
			t.Fatalf("%x: %s want %s", tc.flags, got, tc.want)
		}
	}
}
