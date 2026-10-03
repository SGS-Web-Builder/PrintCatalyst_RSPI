// Package licensegate enforces website-issued, device-bound lifetime licences.
package licensegate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Set by the release build script. Neither can be overridden by merchant config.
var ReleaseURL = "https://licenses.printcatalyst.in"
var ReleasePublicKey string
var ErrRequired = errors.New("activate your permanent software licence in the local dashboard")

const Product = "print-catalyst-on-premise"
const maxLease = 7 * 24 * time.Hour

type envelope struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}
type claims struct {
	Product        string `json:"product"`
	LicenseID      string `json:"licenseId"`
	InstallationID string `json:"installationId"`
	DeviceKey      string `json:"deviceKey"`
	Permanent      bool   `json:"permanent"`
	Active         bool   `json:"active"`
	IssuedAt       int64  `json:"issuedAt"`
	ValidUntil     int64  `json:"validUntil"`
	Nonce          string `json:"nonce"`
}
type Status struct {
	Active     bool   `json:"active"`
	Configured bool   `json:"configured"`
	Permanent  bool   `json:"permanent"`
	LicenseID  string `json:"licenseId"`
	CheckUntil int64  `json:"checkUntil"`
	Message    string `json:"message"`
}
type activationState struct {
	claims claims
	err    error
}

type Client struct {
	activation        atomic.Pointer[activationState]
	db                *sql.DB
	installation, url string
	verify            ed25519.PublicKey
	key               ed25519.PrivateKey
	exchangeMu        sync.Mutex // Serializes online requests without blocking local checks.
	http              *http.Client
	now               func() time.Time
}

func New(db *sql.DB, installation, server, key string) (*Client, error) {
	c := &Client{db: db, installation: installation, url: strings.TrimRight(server, "/"), now: time.Now, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("licence redirects are not allowed") }}}
	if key != "" {
		raw, e := base64.StdEncoding.DecodeString(key)
		if e != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("invalid release licence public key")
		}
		c.verify = raw
	}
	if server != "" {
		u, e := url.Parse(server)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("licence server must be an HTTPS origin or base path")
		}
	}
	var sealed, pub []byte
	err := db.QueryRow(`SELECT private_key,public_key FROM permanent_license_device WHERE singleton=1`).Scan(&sealed, &pub)
	if err == nil {
		raw, e := unprotect(sealed)
		if e != nil {
			return nil, fmt.Errorf("device licence key cannot be opened on this device/service account: %w", e)
		}
		if len(raw) != ed25519.PrivateKeySize || !bytes.Equal(raw[32:], pub) {
			return nil, errors.New("invalid device licence key")
		}
		c.key = raw
		c.loadActivation(context.Background())
		return c, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	pub, c.key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sealed, err = protect(c.key)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`INSERT INTO permanent_license_device VALUES(1,?,?)`, sealed, pub)
	if err == nil {
		c.loadActivation(context.Background())
	}
	return c, err
}
func (c *Client) configured() bool { return c.url != "" && len(c.verify) == 32 }
func (c *Client) parse(e envelope) (claims, error) {
	var p claims
	if !c.configured() || !ed25519.Verify(c.verify, e.Payload, e.Signature) {
		return p, errors.New("licence signature is invalid")
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return p, err
	}
	if p.Product != Product || p.InstallationID != c.installation || p.DeviceKey != base64.StdEncoding.EncodeToString(c.key.Public().(ed25519.PublicKey)) || !p.Permanent || p.LicenseID == "" || p.ValidUntil <= p.IssuedAt || p.ValidUntil-p.IssuedAt > int64(maxLease.Seconds()) {
		return p, errors.New("licence does not match this installation")
	}
	return p, nil
}
func (c *Client) read(ctx context.Context) (claims, int64, error) {
	var raw []byte
	var seen int64
	var e envelope
	err := c.db.QueryRowContext(ctx, `SELECT envelope,last_seen FROM permanent_license_state WHERE singleton=1`).Scan(&raw, &seen)
	if err == sql.ErrNoRows {
		return claims{}, 0, ErrRequired
	}
	if err != nil {
		return claims{}, 0, err
	}
	if err = json.Unmarshal(raw, &e); err != nil {
		return claims{}, seen, err
	}
	p, err := c.parse(e)
	return p, seen, err
}

// loadActivation verifies the device-bound signature once at startup or after
// explicit activation. Existing signed permanent grants remain valid indefinitely.
func (c *Client) loadActivation(ctx context.Context) {
	p, _, err := c.read(ctx)
	if err == nil && !p.Active {
		err = ErrRequired
	}
	c.activation.Store(&activationState{claims: p, err: err})
}

// Check is an in-memory activation flag read: no network, disk, signature or
// clock work occurs on the order/dispatch path.
func (c *Client) Check(ctx context.Context) error {
	state := c.activation.Load()
	if state == nil {
		return ErrRequired
	}
	return state.err
}
func (c *Client) Status(ctx context.Context) Status {
	s := Status{Configured: c.configured()}
	state := c.activation.Load()
	if state == nil {
		s.Message = ErrRequired.Error()
		return s
	}
	s.Permanent = state.claims.Permanent
	s.LicenseID = state.claims.LicenseID
	if state.err != nil {
		s.Message = state.err.Error()
	} else {
		s.Active = true
		s.Message = "Permanently activated on this PC — no recurring licence checks"
	}
	return s
}
func (c *Client) Activate(ctx context.Context, key string) error {
	if len(key) < 16 || len(key) > 128 {
		return errors.New("enter the licence key supplied by the software provider")
	}
	return c.exchange(ctx, "activate", strings.TrimSpace(key))
}

// Refresh is retained for older dashboards; it only reads local activation.
func (c *Client) Refresh(ctx context.Context) error { return c.Check(ctx) }
func (c *Client) exchange(ctx context.Context, action, key string) error {
	c.exchangeMu.Lock()
	defer c.exchangeMu.Unlock()
	if !c.configured() {
		return errors.New("licence server is not configured in this build")
	}
	licenseID := ""
	if action == "refresh" {
		p, _, err := c.read(ctx)
		if err != nil {
			return err
		}
		licenseID = p.LicenseID
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"action": action, "licenseKey": key, "licenseId": licenseID, "installationId": c.installation, "deviceKey": base64.StdEncoding.EncodeToString(c.key.Public().(ed25519.PublicKey)), "nonce": hex.EncodeToString(nonce), "timestamp": c.now().Unix()})
	requestBody, _ := json.Marshal(envelope{Payload: body, Signature: ed25519.Sign(c.key, body)})
	req, err := http.NewRequestWithContext(ctx, "POST", c.url+"/api.php", bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("licence server unreachable; check the internet connection")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("licence request rejected (%d); check the key or contact the provider", res.StatusCode)
	}
	var e envelope
	if err = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&e); err != nil {
		return errors.New("invalid licence server response")
	}
	p, err := c.parse(e)
	if err != nil {
		return err
	}
	now := c.now().Unix()
	if p.Nonce != hex.EncodeToString(nonce) || p.IssuedAt < now-300 || p.IssuedAt > now+300 || p.ValidUntil <= now || (licenseID != "" && p.LicenseID != licenseID) {
		return errors.New("licence response is stale or does not match the request")
	}
	raw, _ := json.Marshal(e)
	// Persist before publishing the activation flag. Concurrent activations
	// are serialized by exchangeMu; readers never acquire that lock.
	_, err = c.db.ExecContext(ctx, `INSERT INTO permanent_license_state VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET envelope=excluded.envelope,last_seen=MAX(last_seen,excluded.last_seen)`, raw, now)
	if err != nil {
		return err
	}
	c.loadActivation(ctx)
	return c.Check(ctx)
}
