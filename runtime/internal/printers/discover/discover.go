// Package discover watches the operating system and the local network for
// printers and feeds the discovered identities into printers.Service.
//
// The package is intentionally backend-agnostic. The runtime is Windows-
// only; the supported backends are:
//
//   discover_windows.go  — Win32 print spooler (EnumPrintersW + DeviceCapabilities)
//   discover_ipp.go      — IPP / IPPS network printers (RFC 8011 Get-Printer-Attributes)
//   discover_test.go     — test fixtures only
//
// All implementations produce the same Discoverer interface so the rest of
// the runtime can treat local and network printers uniformly. No hard-coded
// printer models, paper sizes or option sets — every value comes from the
// driver (Win32 DeviceCapabilities) or from the IPP attributes the device
// reported. The runtime never invents a paper size, tray or finishing option.
package discover

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// Discovered is one printer the Discoverer found. The fields map directly
// onto printers.RegisterInput so the caller can hand the result to
// printers.Service.Register without any further translation.
type Discovered struct {
	Backend       printers.Backend
	QueueName     string
	DisplayName   string
	DriverName    string
	DriverVersion string
	URI           string
	Location      string
	IsDefault     bool
	Capabilities  *printers.Snapshot
	// Status reflects what the discoverer knows RIGHT NOW. The
	// runtime persists Status onto printers.Printer and updates it on
	// every successful poll or change notification.
	Status printers.Status
	// LastSeenAt is the timestamp the discoverer last heard from
	// the printer. A stale record (older than the staleness window
	// used by the registration loop) is dropped.
	LastSeenAt time.Time
	// Error is populated when the discoverer found the printer but
	// failed to capture capabilities (for instance, a Windows spooler
	// whose driver could not be opened). The printer is still
	// registered so the operator can finish setup manually, but the
	// capability panel is flagged as "partial".
	Error error
}

// Discoverer is the contract every backend implementation satisfies. The
// Watch method delivers newly-discovered or updated printers on the supplied
// channel; the channel is closed when ctx is cancelled so callers can range
// over it without managing timeouts themselves.
type Discoverer interface {
	// Backend returns the implementation's backend identifier.
	Backend() printers.Backend
	// Watch starts the discovery loop. Implementations may invoke Watch
	// multiple times across goroutines safely. Implementations MUST
	// close out when ctx.Done() fires so the caller's range loop
	// terminates cleanly.
	Watch(ctx context.Context, out chan<- Discovered)
}

// ErrUnknown is returned when no discoverer matches the requested backend.
// The registration loop emits this as a StatusError so the dashboard can
// surface "this queue references an unsupported backend" without crashing.
var ErrUnknown = errors.New("discoverer for backend not found")

// Registry holds the discoverers registered at startup. The runtime
// constructs a single Registry in main, hands it to printers.Service, and
// the service starts/stops the watchers as part of its lifecycle.
type Registry struct {
	mu         sync.RWMutex
	discoverers map[printers.Backend]Discoverer
}

// NewRegistry returns an empty Registry. Use Register to add discoverers.
func NewRegistry() *Registry {
	return &Registry{discoverers: map[printers.Backend]Discoverer{}}
}

// Register adds a Discoverer for the backend it reports. Re-registering a
// backend overwrites the previous entry — useful in tests, harmless in
// production because main never re-registers.
func (r *Registry) Register(d Discoverer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discoverers[d.Backend()] = d
}

// Get returns the discoverer for a backend. The boolean reports presence so
// callers can default to mock-only mode in tests.
func (r *Registry) Get(backend printers.Backend) (Discoverer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.discoverers[backend]
	return d, ok
}

// Backends enumerates the registered backends in deterministic order.
// Used by the registration loop to ensure every printer is visited.
func (r *Registry) Backends() []printers.Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]printers.Backend, 0, len(r.discoverers))
	for b := range r.discoverers {
		out = append(out, b)
	}
	return out
}

// All starts every registered discoverer's Watch loop concurrently. The
// returned channel is closed when ctx is cancelled OR every discoverer has
// closed its own output — whichever comes first.
//
// The function returns immediately; callers range over the returned channel
// inside their own goroutine.
func (r *Registry) All(ctx context.Context) <-chan Discovered {
	out := make(chan Discovered, 64)
	r.mu.RLock()
	discs := make([]Discoverer, 0, len(r.discoverers))
	for _, d := range r.discoverers {
		discs = append(discs, d)
	}
	r.mu.RUnlock()
	var wg sync.WaitGroup
	for _, d := range discs {
		wg.Add(1)
		go func(d Discoverer) {
			defer wg.Done()
			d.Watch(ctx, out)
		}(d)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
