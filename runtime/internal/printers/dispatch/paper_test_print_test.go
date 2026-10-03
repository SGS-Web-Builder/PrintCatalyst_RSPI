package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type paperTestBackend struct {
	mockBackend
	queue string
	ref   DocumentRef
	body  string
}

func (b *paperTestBackend) Submit(ctx context.Context, queue string, body []byte, ref DocumentRef) (string, error) {
	b.queue = queue
	b.ref = ref
	b.body = string(body)
	return b.mockBackend.Submit(ctx, queue, body, ref)
}
func TestPaperTestTargetsSelectedQueueAndSize(t *testing.T) {
	b := &paperTestBackend{mockBackend: mockBackend{queues: []string{"Selected", "Default"}}}
	d := NewWithDefaultQueue(nil, nil, b, DispatcherConfig{DefaultQueue: "Default"})
	result, err := d.TestPrintOn(context.Background(), "Selected", "A3", 297, 420)
	if err != nil {
		t.Fatal(err)
	}
	if result.Queue != "Selected" || b.queue != "Selected" || b.ref.PaperSize != "A3" || b.ref.Copies != 1 || b.ref.PageStart != 1 || b.ref.PageEnd != 1 {
		t.Fatalf("wrong target/options: %+v", b.ref)
	}
	if !strings.Contains(b.body, "A3 - 297 x 420 mm") || !strings.Contains(b.body, "841.89 1190.55") {
		t.Fatal("test PDF has wrong paper dimensions")
	}
	if _, err = d.TestPrintOn(context.Background(), "Missing", "A4", 210, 297); err == nil {
		t.Fatal("missing printer accepted")
	}
	if _, err = d.TestPrintOn(context.Background(), "Selected", "A4", 0, 0); err == nil {
		t.Fatal("missing dimensions accepted")
	}
	b.fail = errors.New("spool failure")
	if _, err = d.TestPrintOn(context.Background(), "Selected", "A4", 210, 297); err == nil {
		t.Fatal("submission failure hidden")
	}
}
