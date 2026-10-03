// Package tunnel persists customer access settings for direct shop IPs and optional HTTPS tunnels, and renders the counter QR.
package tunnel

import (
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/portalorigin"
	"strings"
)

// Provider is the concrete tunnel implementation the merchant has selected.
// The persisted state stores the string form so a future migration to a new
// provider is an explicit merchant action, not a silent rewrite.
type Provider string

const (
	ProviderDirect      Provider = "direct"
	ProviderCloudflared Provider = "cloudflared"
	ProviderStub        Provider = "stub"
)

// Status is the tunnel lifecycle state. The transition graph is enforced in
// the service: status changes are written atomically with a matching event
// row so the dashboard and the audit trail cannot drift.
type Status string

const (
	StatusUnconfigured Status = "unconfigured"
	StatusStarting     Status = "starting"
	StatusVerifying    Status = "verifying"
	StatusOnline       Status = "online"
	StatusDegraded     Status = "degraded"
	StatusOffline      Status = "offline"
	StatusError        Status = "error"
)

// Config is the merchant-controlled tunnel configuration. The token is never
// echoed back to the dashboard after it is saved; only a fingerprint of it
// is, so a stolen owner session cannot read the credential out of the local
// API.
type Config struct {
	Provider     Provider `json:"provider"`
	PublicOrigin string   `json:"publicOrigin"`
	TunnelToken  string   `json:"tunnelToken,omitempty"`
	ShopRoute    string   `json:"shopRoute"`
	QRTargetPath string   `json:"qrTargetPath"`
}

// Snapshot is the public read model. Sensitive values are stripped.
type Snapshot struct {
	Provider               Provider `json:"provider"`
	PublicOrigin           string   `json:"publicOrigin"`
	PublicURL              string   `json:"publicURL"`
	TunnelTokenFingerprint string   `json:"tunnelTokenFingerprint"`
	Status                 Status   `json:"status"`
	LastVerifiedAt         int64    `json:"lastVerifiedAt"`
	LastVerifiedStatus     int      `json:"lastVerifiedStatus"`
	LastVerifiedError      string   `json:"lastVerifiedError"`
	LastAttemptAt          int64    `json:"lastAttemptAt"`
	LastError              string   `json:"lastError"`
	ShopRoute              string   `json:"shopRoute"`
	QRTargetPath           string   `json:"qrTargetPath"`
	QRTargetURL            string   `json:"qrTargetURL"`
	HasToken               bool     `json:"hasToken"`
	UpdatedAt              int64    `json:"updatedAt"`
}

// VerifyResult captures the outcome of a single public-origin probe.
type VerifyResult struct {
	Status       Status `json:"status"`
	HTTPStatus   int    `json:"httpStatus"`
	RoundTripMs  int64  `json:"roundTripMs"`
	Error        string `json:"error"`
	ProbedAt     int64  `json:"probedAt"`
	ProbedOrigin string `json:"probedOrigin"`
}

// Sentinel errors. The HTTP layer maps each one to a status code.
var (
	ErrInvalid      = errors.New("invalid tunnel configuration")
	ErrUnconfigured = errors.New("tunnel is not configured")
	ErrNoPublic     = errors.New("public origin is not configured")
	ErrNoToken      = errors.New("tunnel token is not configured")
)

// normalizeConfig trims and validates the merchant-supplied configuration.
// It returns ErrInvalid when a required field is empty or has a shape that
// the supervisor cannot use (e.g. a non-HTTPS public origin).
func normalizeConfig(in Config) (Config, error) {
	in.Provider = Provider(strings.ToLower(strings.TrimSpace(string(in.Provider))))
	if in.Provider == "" {
		in.Provider = ProviderDirect
	}
	if in.Provider != ProviderDirect && in.Provider != ProviderCloudflared && in.Provider != ProviderStub {
		return Config{}, fmt.Errorf("%w: provider %q is not supported", ErrInvalid, in.Provider)
	}
	in.PublicOrigin = strings.TrimSpace(in.PublicOrigin)
	if in.Provider == ProviderDirect && in.PublicOrigin == "" {
		return Config{}, fmt.Errorf("%w: shop IP address is required", ErrInvalid)
	}
	if in.PublicOrigin != "" {
		origin, err := portalorigin.Normalize(in.PublicOrigin)
		if err != nil {
			return Config{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if in.Provider != ProviderDirect && !strings.HasPrefix(origin, "https://") {
			return Config{}, fmt.Errorf("%w: tunnel origin must use https://", ErrInvalid)
		}
		in.PublicOrigin = origin
		if strings.ContainsAny(in.PublicOrigin, " \t\r\n") {
			return Config{}, fmt.Errorf("%w: public origin must not contain whitespace", ErrInvalid)
		}
	}
	in.TunnelToken = strings.TrimSpace(in.TunnelToken)
	if in.TunnelToken != "" && len(in.TunnelToken) < 8 {
		return Config{}, fmt.Errorf("%w: tunnel token is too short", ErrInvalid)
	}
	in.ShopRoute = strings.TrimSpace(in.ShopRoute)
	if in.ShopRoute != "" {
		if !strings.HasPrefix(in.ShopRoute, "/") {
			in.ShopRoute = "/" + in.ShopRoute
		}
		if strings.ContainsAny(in.ShopRoute, " \t\r\n") {
			return Config{}, fmt.Errorf("%w: shop route must not contain whitespace", ErrInvalid)
		}
	}
	if in.QRTargetPath == "" {
		in.QRTargetPath = "/portal/"
	} else if !strings.HasPrefix(in.QRTargetPath, "/") {
		in.QRTargetPath = "/" + in.QRTargetPath
	}
	if in.Provider == ProviderDirect {
		in.QRTargetPath = "/portal/"
		in.ShopRoute = ""
		in.TunnelToken = ""
	}
	return in, nil
}
