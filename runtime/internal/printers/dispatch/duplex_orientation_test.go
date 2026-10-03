package dispatch

import "testing"

func TestOrientationDuplex(t *testing.T) {
	for _, orientation := range []string{"portrait", "landscape"} {
		if got := orientationDuplex("one-sided", orientation); got != "one-sided" {
			t.Fatal(got)
		}
		want := "two-sided-long-edge"
		if orientation == "landscape" {
			want = "two-sided-short-edge"
		}
		for _, input := range []string{"two-sided-long-edge", "two-sided-short-edge"} {
			if got := orientationDuplex(input, orientation); got != want {
				t.Fatalf("%s %s: %s, want %s", orientation, input, got, want)
			}
		}
	}
}
