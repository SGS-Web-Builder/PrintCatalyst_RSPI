// This file is build-tag-free. The runtime is Windows-only but the tests
// here exercise the Registry helper, which is identical on every platform;
// the fakeDiscoverer is supplied as a stand-in for both the real Win32
// discoverer and the IPP discoverer. The Windows-specific tests for the
// real EnumPrintersW path live in discover_windows_test.go.
package discover

import (
	"context"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

type fakeDiscoverer struct {
	backend printers.Backend
	out     []Discovered
	delay   time.Duration
}

func (f *fakeDiscoverer) Backend() printers.Backend { return f.backend }

func (f *fakeDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(f.delay):
		}
	}
	for _, d := range f.out {
		select {
		case <-ctx.Done():
			return
		case out <- d:
		}
	}
}

func TestRegistryRegister(t *testing.T) {
	reg := NewRegistry()
	d := &fakeDiscoverer{backend: printers.BackendWindows}
	reg.Register(d)
	got, ok := reg.Get(printers.BackendWindows)
	if !ok {
		t.Fatal("expected discoverer to be registered")
	}
	if got != d {
		t.Fatal("got unexpected discoverer")
	}
}

func TestRegistryBackends(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeDiscoverer{backend: printers.BackendWindows})
	reg.Register(&fakeDiscoverer{backend: printers.BackendMock})
	backends := reg.Backends()
	if len(backends) != 2 {
		t.Fatalf("backends = %d, want 2", len(backends))
	}
}

func TestRegistryAllStreamsAndCloses(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeDiscoverer{
		backend: printers.BackendWindows,
		out: []Discovered{{
			Backend:   printers.BackendWindows,
			QueueName: "HP-LaserJet",
		}, {
			Backend:   printers.BackendWindows,
			QueueName: "Brother-Inkjet",
		}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	count := 0
	for d := range reg.All(ctx) {
		if d.Backend != printers.BackendWindows {
			t.Fatalf("backend = %s", d.Backend)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestRegistryAllCancels(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeDiscoverer{
		backend: printers.BackendWindows,
		delay:   5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for range reg.All(ctx) {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("registry did not close after ctx cancel")
	}
}

// Removed: TestStubDiscovererBlocks relied on the non-Windows noop
// stub for NewWindows(). The runtime is Windows-only now; the real
// Win32 discoverer is exercised by the discover_windows_test.go file
// against an in-process EnumPrintersW shim.
