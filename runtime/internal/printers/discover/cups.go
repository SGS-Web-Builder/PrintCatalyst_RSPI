package discover

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"net/http"
	"time"
)

// CUPSDiscoverer exposes only queues installed on the local scheduler. USB and
// network devices are configured in CUPS; queue options feed the existing UI.
type CUPSDiscoverer struct {
	ipp    *IPPDiscoverer
	queues interface {
		Queues(context.Context) ([]string, error)
	}
}

func NewCUPS() *CUPSDiscoverer {
	ipp := NewIPP()
	ipp.client = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &CUPSDiscoverer{ipp: ipp, queues: dispatch.NewCUPSBackend()}
}
func (d *CUPSDiscoverer) Backend() printers.Backend { return printers.BackendIPP }
func (d *CUPSDiscoverer) Watch(ctx context.Context, out chan<- Discovered) {
	timer := time.NewTicker(8 * time.Second)
	defer timer.Stop()
	for {
		d.tick(ctx, out)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (d *CUPSDiscoverer) tick(ctx context.Context, out chan<- Discovered) {
	queues, err := d.queues.Queues(ctx)
	if err == nil {
		d.ipp.uris = nil
		for _, q := range queues {
			d.ipp.uris = append(d.ipp.uris, "ipp://127.0.0.1:631/printers/"+q)
		}
	}
	// When enumeration fails, probe known queues to surface offline/error state.
	d.ipp.tick(ctx, out)
}
