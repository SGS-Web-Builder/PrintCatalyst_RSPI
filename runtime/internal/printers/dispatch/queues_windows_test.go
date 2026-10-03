//go:build windows

package dispatch

import (
	"context"
	"os"
	"testing"
)

func TestWindowsQueueEnumeration(t *testing.T) {
	names, err := NewWinspoolBackend().Queues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Windows queues: %v", names)
	// This integration assertion is enabled on a machine with the supplied queue.
	if expected := os.Getenv("PC_TEST_EXPECT_QUEUE"); expected != "" {
		for _, name := range names {
			if name == expected {
				return
			}
		}
		t.Fatalf("installed queue %q missing from %v", expected, names)
	}
}
