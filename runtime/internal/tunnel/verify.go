package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/portalorigin"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// probeTimeout caps each public-origin probe. Local probes against a fast
// link should return in tens of milliseconds; this is the ceiling that
// surfaces as "degraded" in the dashboard.
const probeTimeout = 5 * time.Second

// verifyConfig tunes a single probe. Defaults are populated by the caller.
type verifyConfig struct {
	client         *http.Client
	expectedPath   string
	installationID string
}

// VerifyOption configures a single Verify call.
type VerifyOption func(*verifyConfig)

// WithExpectedPath selects the path to probe. Use WithInstallation to verify identity.
func WithExpectedPath(path string) VerifyOption {
	return func(c *verifyConfig) { c.expectedPath = path }
}

// WithInstallation checks that the domain reaches this installation, not a generic 200 page.
func WithInstallation(id string) VerifyOption {
	return func(c *verifyConfig) { c.expectedPath = "/healthz"; c.installationID = id }
}

// probeOrigin runs one public-origin probe and returns a structured result.
// It is exported only for tests; the service uses Verify, which wraps it
// with state-machine bookkeeping.
func probeOrigin(ctx context.Context, origin string, opts ...VerifyOption) VerifyResult {
	start := time.Now()
	result := VerifyResult{ProbedAt: start.Unix(), ProbedOrigin: origin}
	cfg := verifyConfig{
		client: &http.Client{
			Timeout: probeTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
				DialContext: (&net.Dialer{
					Timeout:   probeTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          4,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   probeTimeout,
				ResponseHeaderTimeout: probeTimeout,
				ExpectContinueTimeout: 1 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if origin == "" {
		result.Status = StatusError
		result.Error = "public origin is empty"
		return result
	}
	_, err := portalorigin.Normalize(origin)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Sprintf("public origin is not a valid HTTPS URL: %q", origin)
		return result
	}
	target := origin
	if cfg.expectedPath != "" {
		target = strings.TrimRight(origin, "/") + cfg.expectedPath
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		result.Status = StatusError
		result.Error = "failed to build probe request: " + err.Error()
		return result
	}
	request.Header.Set("User-Agent", "print-catalyst-on-premise/tunnel-probe")
	request.Header.Set("Accept", "text/html,application/json")
	// Forwarded headers are deliberately absent: the probe must not look like
	// a tunneled customer request.
	response, err := cfg.client.Do(request)
	if err != nil {
		result.Status = StatusError
		result.Error = classifyProbeError(err)
		result.RoundTripMs = time.Since(start).Milliseconds()
		return result
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	result.RoundTripMs = time.Since(start).Milliseconds()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.Status = StatusOnline
		if cfg.installationID != "" {
			var health struct {
				Service        string `json:"service"`
				InstallationID string `json:"installationId"`
			}
			if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health); err != nil || health.Service != "print-catalyst-on-premise" || health.InstallationID != cfg.installationID {
				result.Status = StatusError
				result.Error = "domain does not reach this Print Catalyst installation; check the tunnel service URL"
			}
		}
	case response.StatusCode >= 500:
		result.Status = StatusDegraded
		result.Error = fmt.Sprintf("public origin returned %d", response.StatusCode)
	default:
		result.Status = StatusOffline
		result.Error = fmt.Sprintf("public origin returned %d", response.StatusCode)
	}
	return result
}

// Verify probes the configured public origin and returns the structured
// outcome. It is the read-only entry point the dashboard uses to refresh the
// status badge.
func Verify(ctx context.Context, publicOrigin string, opts ...VerifyOption) VerifyResult {
	return probeOrigin(ctx, publicOrigin, opts...)
}

// classifyProbeError turns the rich set of HTTP errors into a single
// human-readable reason. We avoid leaking dialer internals to the dashboard.
func classifyProbeError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "probe cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "probe timed out"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "connection timed out"
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("connection failed: %s", urlErr.Err)
	}
	return fmt.Sprintf("connection failed: %s", err)
}
