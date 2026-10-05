package prepared

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAbandonedCleanupPreservesPublishedAndRecent(t *testing.T) {
	s, dir := fixture(t)
	id, err := s.Publish("order-one", input())
	if err != nil {
		t.Fatal(err)
	}
	old := ".preparing-0123456789abcdef0123456789abcdef"
	recent := ".preparing-abcdef0123456789abcdef0123456789"
	for _, name := range []string{old, recent, ".preparing-unknown"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, name := range []string{old, id, ".preparing-unknown"} {
		past := now.Add(-48 * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, name), past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CleanupAbandoned(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatal("old staging survives", err)
	}
	for _, name := range []string{recent, id, ".preparing-unknown"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("retained entry removed", name, err)
		}
	}
	if _, _, err := s.Load("order-one", id); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}
func input() []Input {
	return []Input{{LineID: "line-one", Queue: "shop-printer", Settings: Settings{Paper: "iso_a4_210x297mm", Colour: "monochrome", Sides: "two-sided-long-edge", Copies: 2}, PDF: []byte("%PDF-1.7\nsynthetic storage fixture, not a rendered PDF\n")}}
}
func TestRoundTripFrozenPlanAndRestart(t *testing.T) {
	s, dir := fixture(t)
	in := input()
	id, err := s.Publish("order-one", in)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.Publish("order-one", in)
	if err != nil || same != id {
		t.Fatal("non-idempotent publish", err)
	}
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	m, data, err := second.Load("order-one", id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Jobs[0].Settings != in[0].Settings || m.Jobs[0].Queue != in[0].Queue || !bytes.Equal(data[0], in[0].PDF) {
		t.Fatal("plan lost")
	}
	// Changing any frozen output setting produces a distinct identity.
	in[0].Settings.Copies = 3
	changed, err := s.Publish("order-one", in)
	if err != nil || changed == id {
		t.Fatal("copies not bound", err)
	}
	in[0].Queue = "other-printer"
	queue, err := s.Publish("order-one", in)
	if err != nil || queue == changed {
		t.Fatal("queue not bound", err)
	}
	if _, _, err = second.Load("other-order", id); err == nil {
		t.Fatal("cross-order load accepted")
	}
	// Consumer owns returned bytes; mutations cannot affect stored files.
	data[0][0] = '!'
	_, fresh, err := second.Load("order-one", id)
	if err != nil || fresh[0][0] != '%' {
		t.Fatal("mutable return altered storage")
	}
}
func TestRejectTamperingAndIncompleteBundles(t *testing.T) {
	for _, kind := range []string{"pdf", "manifest", "missing", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := fixture(t)
			id, err := s.Publish("order", input())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, id, "000.pdf")
			switch kind {
			case "pdf":
				err = os.WriteFile(path, []byte("%PDF-forged"), 0600)
			case "manifest":
				err = os.WriteFile(filepath.Join(dir, id, "manifest.json"), []byte(`{}`), 0600)
			case "missing":
				err = os.Remove(path)
			case "truncated":
				err = os.WriteFile(path, []byte("%PDF-"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, data, err := s.Load("order", id); err == nil || data != nil {
				t.Fatal("invalid bundle released bytes")
			}
			if _, err := s.Publish("order", input()); err == nil {
				t.Fatal("silently replaced corrupted prepared order")
			}
		})
	}
}
func TestRejectInvalidInputsAndPaths(t *testing.T) {
	s, _ := fixture(t)
	for _, id := range []string{"../outside", "", string(bytes.Repeat([]byte("a"), 65))} {
		if _, _, err := s.Load("order", id); err == nil {
			t.Fatal("accepted invalid digest")
		}
	}
	for _, kind := range []string{"empty", "duplicate", "copies", "sides", "not-pdf", "queue"} {
		t.Run(kind, func(t *testing.T) {
			in := input()
			switch kind {
			case "empty":
				in = nil
			case "duplicate":
				in = append(in, in[0])
			case "copies":
				in[0].Settings.Copies = 0
			case "sides":
				in[0].Settings.Sides = "unknown"
			case "not-pdf":
				in[0].PDF = []byte("invalid")
			case "queue":
				in[0].Queue = "bad\nqueue"
			}
			if _, err := s.Publish("order", in); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
func TestConcurrentPublishAndMultiJobIntegrity(t *testing.T) {
	s, dir := fixture(t)
	in := input()
	invoice := in[0]
	invoice.LineID = "invoice"
	invoice.Invoice = true
	invoice.Settings.Paper = "iso_a6_105x148mm"
	invoice.Settings.Tray = "Tray 2"
	in = append(in, invoice)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.Publish("order", in)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	var id string
	for v := range ids {
		if id != "" && v != id {
			t.Fatal("concurrent identity mismatch")
		}
		id = v
	}
	m, data, err := s.Load("order", id)
	if err != nil || len(data) != 2 || !m.Jobs[1].Invoice || m.Jobs[1].Settings.Tray != "Tray 2" {
		t.Fatal("invoice lost", err)
	}
	if err = os.Remove(filepath.Join(dir, id, "001.pdf")); err != nil {
		t.Fatal(err)
	}
	if _, data, err = s.Load("order", id); err == nil || data != nil {
		t.Fatal("returned partial order")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("staging files leaked", len(entries))
	}
}

func TestCompletedDeletionBoundToOrder(t *testing.T) {
	s, _ := fixture(t)
	id, err := s.Publish("order", input())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCompleted("other", id); err == nil {
		t.Fatal("cross-order deletion allowed")
	}
	if _, _, err = s.Load("order", id); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCompleted("order", "../outside"); err == nil {
		t.Fatal("path escape accepted")
	}
	if err = s.DeleteCompleted("order", id); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCompleted("order", id); err != nil {
		t.Fatal("idempotent cleanup failed", err)
	}
}

func TestStreamedPublicationAndTamperAfterPreflight(t *testing.T) {
	s, dir := fixture(t)
	calls := 0
	id, err := s.PublishJobs("stream-order", 2, func(i int) (Input, error) {
		calls++
		in := input()[0]
		if i == 1 {
			in.LineID = "second"
		}
		return in, nil
	})
	if err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	m, err := s.Verify("stream-order", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadJob(id, m.Jobs[1]); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, id, m.Jobs[1].File), []byte("%PDF-corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadJob(id, m.Jobs[1]); err == nil {
		t.Fatal("tamper accepted after preflight")
	}
}
