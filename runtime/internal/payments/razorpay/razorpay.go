// Package razorpay implements the merchant-side Razorpay payment
// adapter for Print Catalyst On-Premise.
//
// The adapter implements payments.MerchantProvider against the
// public Razorpay REST API (https://api.razorpay.com/v1). It is
// deliberately written against the documented wire format instead
// of vendoring the official SDK so the runtime stays small, has
// zero external payment dependencies, and lets a security review
// inspect every byte that crosses the boundary.
//
// Three flows are supported:
//
//   1. Payment Links — the merchant creates a hosted link via
//      POST /v1/payment_links; the customer opens the short URL,
//      pays, and Razorpay fires a webhook back to the local
//      service. This is the recommended flow because it works
//      with no public origin on the merchant's machine.
//
//   2. Direct Orders — POST /v1/orders returns an order_id; the
//      merchant's portal hands that to the Razorpay Standard
//      Checkout on the customer browser. Useful when the merchant
//      already exposes the portal over HTTPS via a tunnel.
//
//   3. Manual — recorded as MethodCash / MethodUPI / etc. on the
//      same Intent record. The merchant approves offline; the
//      dashboard surfaces the manual flag separately.
//
// The merchant's API key (Key ID + Key Secret) and the webhook
// secret are stored encrypted-at-rest on the merchant's machine
// (see payments.Service.CreateProvider). The adapter's Create,
// VerifyWebhookSignature and ParseWebhook all read the secret
// from memory and never log it.
package razorpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
)

// Endpoint is the Razorpay REST root. The package default targets
// the production endpoint; tests override it via WithHTTPClient /
// the injected base URL.
var Endpoint = "https://api.razorpay.com/v1"

// HTTPClient is the HTTP client used for every Razorpay call. The
// default has a 30-second timeout — long enough for slow networks,
// short enough that the webhook push loop never stalls past its
// latency budget. Tests swap in a custom *http.Client pointing at
// an httptest.Server.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// Clock allows tests to inject deterministic time. The default is
// time.Now.
var Clock func() time.Time = time.Now

// Errors surfaced to callers. Sentinel errors match the payments
// package's vocabulary where possible so the HTTP layer's error
// mapper does the right thing without per-adapter branching.
var (
	ErrCredentials    = errors.New("razorpay credentials missing")
	ErrSignature      = errors.New("razorpay webhook signature does not match")
	ErrUnexpectedBody = errors.New("razorpay response body was not parseable JSON")
	ErrHTTP           = errors.New("razorpay HTTP call failed")
	ErrStatus         = errors.New("razorpay returned a non-2xx status")
)

// Credentials bundles the merchant-supplied Razorpay secrets. The
// key_id is used as the Basic-auth username and the key_secret as
// the password on every outbound call; the webhook_secret is used
// to verify webhook signatures.
//
// Credentials are passed to the adapter as an opaque byte slice
// that the payments package decrypts at load time. The wire
// encoding is JSON so we can evolve the schema without a database
// migration. The shape is exported for tests; production code
// should rely on ParseCredentials.
type Credentials struct {
	KeyID         string `json:"key_id"`
	KeySecret     string `json:"key_secret"`
	WebhookSecret string `json:"webhook_secret"`
}

// ParseCredentials decodes the encrypted-at-rest secret blob that
// payments.Service hands to the adapter. An empty or malformed
// blob returns ErrCredentials so the HTTP layer can surface a
// clean 409 "unconfigured" instead of a panic.
func ParseCredentials(secret []byte) (Credentials, error) {
	if len(secret) == 0 {
		return Credentials{}, ErrCredentials
	}
	trimmed := bytes.TrimSpace(secret)
	// Accept both JSON (the recommended encoding) and the legacy
	// newline-delimited encoding so merchants who stored their
	// credentials before this package existed keep working.
	if trimmed[0] == '{' {
		var c Credentials
		if err := json.Unmarshal(trimmed, &c); err != nil {
			return Credentials{}, fmt.Errorf("%w: %v", ErrCredentials, err)
		}
		if c.KeyID == "" || c.KeySecret == "" || c.WebhookSecret == "" {
			return Credentials{}, fmt.Errorf("%w: key_id, key_secret and webhook_secret are required", ErrCredentials)
		}
		return c, nil
	}
	parts := strings.SplitN(string(trimmed), "\n", 3)
	if len(parts) < 3 {
		return Credentials{}, fmt.Errorf("%w: expected JSON or key_id\\nkey_secret\\nwebhook_secret", ErrCredentials)
	}
	return Credentials{KeyID: parts[0], KeySecret: parts[1], WebhookSecret: parts[2]}, nil
}

// Adapter is the Razorpay-backed implementation of
// payments.MerchantProvider. Construct with New; tests construct it
// directly so they can inject a Credentials value and a custom
// http.Client.
type Adapter struct {
	httpClient *http.Client
	endpoint   string
	now        func() time.Time
}

// New returns a Razorpay adapter wired to the production endpoint
// and the default HTTP client. The adapter is safe for concurrent
// use.
func New() *Adapter {
	return &Adapter{
		httpClient: HTTPClient,
		endpoint:   Endpoint,
		now:        Clock,
	}
}

// WithHTTPClient swaps the outbound HTTP client. Used by tests to
// point the adapter at an httptest.Server.
func (a *Adapter) WithHTTPClient(client *http.Client) *Adapter {
	if client != nil {
		a.httpClient = client
	}
	return a
}

// WithEndpoint swaps the API base URL. Used by tests.
func (a *Adapter) WithEndpoint(raw string) *Adapter {
	if raw != "" {
		a.endpoint = strings.TrimRight(raw, "/")
	}
	return a
}

// Create issues a Razorpay Payment Link for the supplied intent and
// returns the redirect URL the customer uses to pay.
//
// The amount / currency / customer details come from the local
// intent; the local intent id is propagated as the Razorpay
// payment link's "notes.intent_id" so the webhook handler can
// correlate the callback to the right row without trusting the
// merchant's payload.
//
// Razorpay returns a short_url that we hand back verbatim. The
// short_url is a one-time-use hosted page; the customer pays there
// and Razorpay then fires payment_link.paid to our webhook.
func (a *Adapter) Create(intent payments.Intent, secret []byte) (payments.CreateResult, error) {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return payments.CreateResult{}, err
	}
	payload := map[string]any{
		"reference_id":   intent.ID,
		"amount":         intent.AmountMinor,
		"currency":       intent.Currency,
		"accept_partial": false,
		"description":    "Print Catalyst order " + intent.OrderID,
		"customer": map[string]string{
			"name":    intent.CustomerName,
			"contact": intent.CustomerPhone,
			"email":   intent.CustomerEmail,
		},
		"notify":          map[string]bool{"sms": true, "email": true},
		"reminder_enable": false,
		"expire_by":       a.now().Add(30 * time.Minute).Unix(),
		"notes": map[string]string{
			"intent_id":       intent.ID,
			"order_id":        intent.OrderID,
			"installation_id": intent.InstallationID,
		},
	}
	if intent.ReturnURL != "" {
		payload["callback_url"] = intent.ReturnURL
		payload["callback_method"] = "get"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return payments.CreateResult{}, fmt.Errorf("marshal razorpay request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, a.endpoint+"/payment_links", bytes.NewReader(body))
	if err != nil {
		return payments.CreateResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(creds.KeyID, creds.KeySecret)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return payments.CreateResult{}, fmt.Errorf("%w: %v", ErrHTTP, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return payments.CreateResult{}, fmt.Errorf("%w: status=%d body=%s", ErrStatus, resp.StatusCode, truncate(string(raw), 256))
	}
	var parsed struct {
		ID        string `json:"id"`
		ShortURL  string `json:"short_url"`
		Status    string `json:"status"`
		ExpiresAt int64  `json:"expire_by"`
		Amount    int64  `json:"amount"`
		Currency  string `json:"currency"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return payments.CreateResult{}, fmt.Errorf("%w: %v", ErrUnexpectedBody, err)
	}
	if parsed.ID == "" || parsed.ShortURL == "" {
		return payments.CreateResult{}, fmt.Errorf("%w: missing id or short_url in response", ErrUnexpectedBody)
	}
	expiresAt := time.Unix(parsed.ExpiresAt, 0)
	if parsed.ExpiresAt == 0 {
		expiresAt = a.now().Add(30 * time.Minute)
	}
	return payments.CreateResult{
		GatewayOrderID:   parsed.ID,
		GatewayPaymentID: "", // filled in by the webhook
		RedirectURL:      parsed.ShortURL,
		ExpiresAt:        expiresAt,
	}, nil
}

// VerifyWebhookSignature checks the X-Razorpay-Signature header
// against the HMAC-SHA256 of the raw body keyed with the webhook
// secret. The comparison is constant-time so a timing-attack
// observer cannot learn the expected signature byte-by-byte.
func (a *Adapter) VerifyWebhookSignature(rawBody []byte, signature string, secret []byte) error {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return err
	}
	if signature == "" {
		return fmt.Errorf("%w: empty signature header", ErrSignature)
	}
	expected := signBody(rawBody, creds.WebhookSecret)
	provided, err := hex.DecodeString(signature)
	if err != nil {
		// Razorpay ships the signature as hex but we also accept
		// base64 just in case the merchant wired a different
		// header in their dashboard.
		provided, err = base64.StdEncoding.DecodeString(signature)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrSignature, err)
		}
	}
	if !hmac.Equal(expected, provided) {
		return ErrSignature
	}
	return nil
}

// signBody returns HMAC-SHA256(rawBody, key) as raw bytes.
// Exported via package-internal tests.
func signBody(rawBody []byte, key string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(rawBody)
	return mac.Sum(nil)
}

// ParseWebhook decodes a Razorpay event payload into the
// payments.Webhook shape the local service consumes.
//
// Razorpay fires multiple event kinds; we map:
//
//	payment_link.paid   -> StatusCaptured
//	payment_link.cancelled -> StatusCancelled
//	payment_link.expired -> StatusExpired
//	payment.captured -> StatusCaptured
//	payment.failed   -> StatusFailed
//	payment.authorized -> StatusAuthorized
//
// Anything else returns an error so the runtime rejects unknown
// events instead of silently transitioning the local intent.
func (a *Adapter) ParseWebhook(rawBody []byte) (payments.Webhook, error) {
	type eventEnvelope struct {
		Entity    string   `json:"entity"`
		AccountID string   `json:"account_id"`
		ID        string   `json:"id"`
		Event     string   `json:"event"`
		Contains  []string `json:"contains"`
		CreatedAt int64    `json:"created_at"`
		Payload   struct {
			PaymentLink struct {
				Entity struct {
					ID       string            `json:"id"`
					Amount   int64             `json:"amount"`
					Currency string            `json:"currency"`
					Status   string            `json:"status"`
					Notes    map[string]string `json:"notes"`
				} `json:"entity"`
			} `json:"payment_link"`
			Payment struct {
				Entity struct {
					ID       string `json:"id"`
					Amount   int64  `json:"amount"`
					Currency string `json:"currency"`
					Status   string `json:"status"`
					OrderID  string `json:"order_id"`
					Method   string `json:"method"`
				} `json:"entity"`
			} `json:"payment"`
		} `json:"payload"`
	}
	var event eventEnvelope
	if err := json.Unmarshal(rawBody, &event); err != nil {
		return payments.Webhook{}, fmt.Errorf("parse razorpay event: %w", err)
	}
	if event.Entity != "event" {
		return payments.Webhook{}, fmt.Errorf("not a razorpay event envelope (entity=%q)", event.Entity)
	}
	status, err := mapEventStatus(event.Event, event.Payload.PaymentLink.Entity.Status, event.Payload.Payment.Entity.Status)
	if err != nil {
		return payments.Webhook{}, err
	}
	// The intent id lives in payment_link.notes (our flow) or, for
	// direct Order+Checkout flows, must be supplied by the
	// merchant as a "transfer" header — we do not support that yet
	// so the order_id / notes path is the only valid source.
	intentID := strings.TrimSpace(event.Payload.PaymentLink.Entity.Notes["intent_id"])
	// Fall back: if this is a payment.* event, try to look up by
	// gateway_order_id == payment.order_id against the local intent
	// table in the caller. We cannot do that lookup here because
	// the adapter has no database handle; the payments.Service
	// resolves intent by GatewayOrderID on transition.
	gatewayOrderID := event.Payload.PaymentLink.Entity.ID
	if gatewayOrderID == "" {
		gatewayOrderID = event.Payload.Payment.Entity.OrderID
	}
	gatewayPaymentID := event.Payload.Payment.Entity.ID
	amount := event.Payload.Payment.Entity.Amount
	if amount == 0 {
		amount = event.Payload.PaymentLink.Entity.Amount
	}
	currency := event.Payload.Payment.Entity.Currency
	if currency == "" {
		currency = event.Payload.PaymentLink.Entity.Currency
	}
	// Idempotency key: prefer the Razorpay event id (it is the only
	// globally unique identifier Razorpay stamps on the envelope)
	// and fall back to the (event, intent_id, gateway_payment_id)
	// tuple so a payload without an event id still dedupes correctly.
	idemKey := strings.TrimSpace(event.ID)
	if idemKey == "" {
		idemParts := []string{event.Event, intentID, gatewayPaymentID, gatewayOrderID}
		idemKey = strings.Join(idemParts, "|")
		if intentID == "" {
			// Payment-only events without our notes must be matched
			// later by gateway_order_id. The intent id is left blank
			// and the payments.Service.applyWebhookTransition will
			// resolve it via loadIntentByGatewayOrderID (out of scope
			// for v1, but we surface the gap cleanly).
			idemKey = strings.Join(append([]string{event.Event}, idemParts...), "|")
		}
	}
	receivedAt := time.Unix(event.CreatedAt, 0)
	if event.CreatedAt == 0 {
		receivedAt = a.now()
	}
	gatewayEventID := strings.TrimSpace(event.AccountID + ":" + event.Event + ":" + strings.TrimSpace(event.ID))
	if event.ID != "" {
		gatewayEventID = event.AccountID + ":" + event.ID
	}
	return payments.Webhook{
		GatewayEventID:   gatewayEventID,
		IdempotencyKey:   idemKey,
		IntentID:         intentID,
		GatewayOrderID:   gatewayOrderID,
		GatewayPaymentID: gatewayPaymentID,
		Status:           status,
		AmountMinor:      amount,
		Currency:         currency,
		ReceivedAt:       receivedAt,
	}, nil
}

// mapEventStatus converts a Razorpay event kind to a payments.Status.
// Returns ErrUnexpectedBody for events we do not handle so the
// runtime can log + ignore them instead of silently transitioning
// the local intent.
func mapEventStatus(event, linkStatus, paymentStatus string) (payments.Status, error) {
	switch event {
	case "payment_link.paid":
		return payments.StatusCaptured, nil
	case "payment_link.cancelled":
		return payments.StatusCancelled, nil
	case "payment_link.expired":
		return payments.StatusExpired, nil
	case "payment_link.partially_paid":
		// The merchant did not enable accept_partial so this should
		// never fire in production. Treat it as an authorised hold
		// waiting for the balance so the dashboard can surface the
		// unusual state instead of silently charging.
		return payments.StatusAuthorized, nil
	case "payment.captured":
		return payments.StatusCaptured, nil
	case "payment.authorized":
		return payments.StatusAuthorized, nil
	case "payment.failed":
		return payments.StatusFailed, nil
	case "order.paid":
		return payments.StatusCaptured, nil
	case "order.payment_failed":
		return payments.StatusFailed, nil
	default:
		// Allow "payment_link.*" events whose specific kind has not
		// been enumerated yet, as long as the link status is
		// recognisable. This keeps the adapter forward-compatible
		// with new Razorpay event names.
		if strings.HasPrefix(event, "payment_link.") && linkStatus != "" {
			switch linkStatus {
			case "paid":
				return payments.StatusCaptured, nil
			case "cancelled":
				return payments.StatusCancelled, nil
			case "expired":
				return payments.StatusExpired, nil
			case "partially_paid":
				return payments.StatusAuthorized, nil
			}
		}
		if strings.HasPrefix(event, "payment.") && paymentStatus != "" {
			switch paymentStatus {
			case "captured":
				return payments.StatusCaptured, nil
			case "authorized":
				return payments.StatusAuthorized, nil
			case "failed":
				return payments.StatusFailed, nil
			}
		}
		return "", fmt.Errorf("%w: unhandled razorpay event %q", ErrUnexpectedBody, event)
	}
}

// truncate keeps error bodies bounded so a misbehaving Razorpay
// response cannot balloon our log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Capture is a convenience that captures a previously-authorised
// payment. Print Catalyst On-Premise prefers auto-capture payment
// links (the default), so this is exposed for the manual-capture
// flow the dashboard exposes when the merchant wants to inspect
// the order before charging.
//
// Not part of the MerchantProvider interface — called from the
// dashboard's "capture now" button, not from the order flow.
func (a *Adapter) Capture(ctx context.Context, paymentID string, amountMinor int64, secret []byte) error {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"amount": amountMinor,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/payments/"+url.PathEscape(paymentID)+"/capture", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(creds.KeyID, creds.KeySecret)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHTTP, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("%w: status=%d body=%s", ErrStatus, resp.StatusCode, truncate(string(raw), 256))
	}
	return nil
}

// TestConnection verifies that the supplied Razorpay credentials
// authenticate against the Razorpay REST API. The test issues a
// GET against /v1/payment_links?count=1, the cheapest read-only
// call that exercises the Basic-auth credentials without
// triggering any side effect. A 200 means the key id and key
// secret are both valid; a 401 means Razorpay rejected the
// credentials; other statuses are surfaced so the dashboard can
// display the real Razorpay response.
//
// TestConnection satisfies payments.Connector; the dashboard calls
// it from the "Test connection" button on the payment setup wizard
// so the merchant can confirm their pasted keys are live before
// saving. The function never persists the supplied credentials and
// never includes them in returned error messages — only the
// status code and a truncated response body are surfaced.
func (a *Adapter) TestConnection(ctx context.Context, secret []byte) error {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return err
	}
	if creds.WebhookSecret == "" {
		return fmt.Errorf("%w: webhook_secret is required to enable webhook verification", ErrCredentials)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/payment_links?count=1", nil)
	if err != nil {
		return fmt.Errorf("build test request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(creds.KeyID, creds.KeySecret)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHTTP, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: status=%d body=%s", ErrCredentials, resp.StatusCode, truncate(string(raw), 256))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("%w: status=%d body=%s", ErrStatus, resp.StatusCode, truncate(string(raw), 256))
	}
	return nil
}

// RecoverCreate resolves a possibly successful POST before retrying it. The
// unique reference_id makes a lost response safe to recover across restarts.
func (a *Adapter) RecoverCreate(ctx context.Context, intent payments.Intent, secret []byte) (payments.CreateResult, error) {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return payments.CreateResult{}, err
	}
	endpoint := a.endpoint + "/payment_links?reference_id=" + url.QueryEscape(intent.ID)
	if intent.GatewayOrderID != "" {
		endpoint = a.endpoint + "/payment_links/" + url.PathEscape(intent.GatewayOrderID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return payments.CreateResult{}, err
	}
	req.SetBasicAuth(creds.KeyID, creds.KeySecret)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return payments.CreateResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return payments.CreateResult{}, fmt.Errorf("gateway recovery unavailable (%d)", resp.StatusCode)
	}
	type link struct {
		ID        string `json:"id"`
		Reference string `json:"reference_id"`
		URL       string `json:"short_url"`
		Amount    int64  `json:"amount"`
		Currency  string `json:"currency"`
		Status    string `json:"status"`
		Expires   int64  `json:"expire_by"`
	}
	var links []link
	if intent.GatewayOrderID != "" {
		var item link
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&item); err != nil {
			return payments.CreateResult{}, err
		}
		links = append(links, item)
	} else {
		var list struct {
			Links []link `json:"payment_links"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
			return payments.CreateResult{}, err
		}
		links = list.Links
	}
	for _, l := range links {
		if l.Reference != intent.ID && l.ID != intent.GatewayOrderID {
			continue
		}
		if l.Amount != intent.AmountMinor || l.Currency != intent.Currency {
			return payments.CreateResult{}, fmt.Errorf("gateway link amount does not match the order")
		}
		if l.Status != "created" || (l.Expires > 0 && l.Expires <= a.now().Unix()) {
			return payments.CreateResult{}, fmt.Errorf("payment link is %s; refresh status or ask the counter", l.Status)
		}
		if l.ID == "" || l.URL == "" {
			return payments.CreateResult{}, ErrUnexpectedBody
		}
		return payments.CreateResult{GatewayOrderID: l.ID, RedirectURL: l.URL, ExpiresAt: time.Unix(l.Expires, 0)}, nil
	}
	if intent.GatewayOrderID != "" {
		return payments.CreateResult{}, fmt.Errorf("existing payment link not found")
	}
	return a.Create(intent, secret)
}
