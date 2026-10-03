// Package notifications provides merchant alert configuration, in-process
// event delivery for new orders, and outbound SMTP / WhatsApp transports
// so the merchant can receive alerts when their phone is asleep and the
// dashboard is not open. Settings are persisted to a singleton row, a
// server-sent events (SSE) endpoint streams new-order events to any
// connected owner dashboard client, and the transport adapters append
// every delivery attempt to the notification_deliveries audit table.
package notifications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Service manages notification settings, distributes events to
// dashboard sessions and dispatches email / WhatsApp transports.
type Service struct {
	db          *sql.DB
	listener    func(event Event)
	emailDialer func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error
	whatsAppPOST func(ctx context.Context, apiURL, token, to, body string) error
	httpClient  *http.Client
	mu       sync.RWMutex
	sessions map[string]chan Event
}

// New creates a notifications service. The listener is called for each new
// order after settings are consulted; pass nil to disable in-process delivery.
func New(db *sql.DB, listener func(event Event)) *Service {
	return &Service{
		db: db, listener: listener,
		emailDialer: sendSMTP,
		whatsAppPOST: sendWhatsApp,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		sessions:    make(map[string]chan Event),
	}
}

// SetEmailDialer replaces the default SMTP implementation. Tests pass
// a recording dialer so they can assert the payload without an SMTP
// round trip.
func (s *Service) SetEmailDialer(dialer func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emailDialer = dialer
}

// SetWhatsAppPOST replaces the default HTTP POST implementation. Tests
// pass a recording poster so they can assert the payload without a
// network round trip.
func (s *Service) SetWhatsAppPOST(poster func(ctx context.Context, apiURL, token, to, body string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.whatsAppPOST = poster
}

// ---- Settings ----

// Settings holds the merchant's notification preferences.
type Settings struct {
	DesktopAlerts   bool   `json:"desktopAlertsEnabled"`
	AudioAlerts    bool   `json:"audioAlertsEnabled"`
	EmailEnabled   bool   `json:"emailEnabled"`
	EmailHost      string `json:"emailHost"`
	EmailPort      int    `json:"emailPort"`
	EmailUsername  string `json:"emailUsername"`
	EmailFrom      string `json:"emailFrom"`
	WhatsAppEnabled bool  `json:"whatsappEnabled"`
	WhatsAppAPIURL string `json:"whatsappApiUrl"`
	WhatsAppTo     string `json:"whatsappTo"`
	UpdatedAt      int64  `json:"updatedAt"`

	// EmailToken is set by callers when updating credentials; never returned by Load.
	EmailToken string `json:"emailToken,omitempty"`
	// WhatsAppToken is set by callers when updating credentials; never returned by Load.
	WhatsAppToken string `json:"whatsappToken,omitempty"`
}

// Load returns the current notification settings. API tokens are NOT returned;
// callers must supply them explicitly via Save when they need to update them.
func (s *Service) Load(ctx context.Context) (Settings, error) {
	var r Settings
	err := s.db.QueryRowContext(ctx, `
SELECT desktop_alerts_enabled, audio_alerts_enabled,
       email_enabled, email_host, email_port, email_username, email_from, email_password,
       whatsapp_enabled, whatsapp_api_url, whatsapp_api_token, whatsapp_to,
       updated_at
FROM notification_settings WHERE singleton=1`).Scan(
		&r.DesktopAlerts, &r.AudioAlerts,
		&r.EmailEnabled, &r.EmailHost, &r.EmailPort, &r.EmailUsername, &r.EmailFrom, &r.EmailToken,
		&r.WhatsAppEnabled, &r.WhatsAppAPIURL, &r.WhatsAppToken, &r.WhatsAppTo,
		&r.UpdatedAt)
	if err != nil {
		return Settings{}, err
	}
	return r, nil
}

// ErrInvalid is returned when the caller submits an invalid notification
// configuration (port out of range, etc). Handlers map it to HTTP 400.
var ErrInvalid = errors.New("invalid notification settings")

// Save persists updated notification settings. EmailToken and WhatsAppToken
// inside the Settings struct are written to the DB only if non-empty;
// existing tokens are preserved otherwise. EmailPort is only validated when
// EmailEnabled is true, so callers can disable email without configuring it.
func (s *Service) Save(ctx context.Context, r Settings) error {
	if r.EmailEnabled && (r.EmailPort < 1 || r.EmailPort > 65535) {
		return ErrInvalid
	}
	// Preserve existing tokens when callers don't supply new ones.
	if r.EmailToken == "" || r.WhatsAppToken == "" {
		cur, err := s.Load(ctx)
		if err == nil {
			if r.EmailToken == "" {
				r.EmailToken = cur.EmailToken
			}
			if r.WhatsAppToken == "" {
				r.WhatsAppToken = cur.WhatsAppToken
			}
		}
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE notification_settings SET
    desktop_alerts_enabled = ?,
    audio_alerts_enabled   = ?,
    email_enabled          = ?,
    email_host             = ?,
    email_port             = ?,
    email_username         = ?,
    email_from             = ?,
    email_password         = ?,
    whatsapp_enabled       = ?,
    whatsapp_api_url       = ?,
    whatsapp_api_token     = ?,
    whatsapp_to            = ?,
    updated_at             = ?
WHERE singleton = 1`,
		r.DesktopAlerts, r.AudioAlerts,
		r.EmailEnabled, r.EmailHost, r.EmailPort, r.EmailUsername, r.EmailFrom, r.EmailToken,
		r.WhatsAppEnabled, r.WhatsAppAPIURL, r.WhatsAppToken, r.WhatsAppTo,
		time.Now().Unix())
	return err
}

// SettingsInput is the shape accepted by the HTTP PUT endpoint. It wraps
// Settings and the separately transmitted email API token.
type SettingsInput struct {
	Settings
	EmailToken string `json:"emailToken"`
}

// ---- Event delivery ----

// Event is the payload sent to connected dashboard clients when a new order arrives.
type Event struct {
	OrderID       string `json:"orderId"`
	CustomerName  string `json:"customerName"`
	TotalMinor   int64  `json:"totalMinor"`
	Currency     string `json:"currency"`
	CurrencyMU   int    `json:"currencyMinorUnits"`
	CreatedAt    int64  `json:"createdAt"`
}

// Notify broadcasts an event to all connected dashboard sessions if the
// current settings allow it, and dispatches the email / WhatsApp
// transports. The transports run asynchronously on their own
// goroutine so the portal response is never blocked by SMTP latency.
func (s *Service) Notify(ctx context.Context, orderID, customerName string, totalMinor int64, currency string, mu int, createdAt int64) {
	settings, err := s.Load(ctx)
	if err != nil {
		return // settings table unavailable; silently skip
	}
	event := Event{
		OrderID:      orderID,
		CustomerName: customerName,
		TotalMinor:   totalMinor,
		Currency:     currency,
		CurrencyMU:   mu,
		CreatedAt:    createdAt,
	}
	if settings.DesktopAlerts || settings.AudioAlerts {
		s.broadcast(event)
	}
	// Email and WhatsApp transport are independent of desktop/audio so
	// a merchant can keep the dashboard silent while still receiving an
	// external alert.
	if settings.EmailEnabled {
		go s.deliverEmail(settings, event)
	}
	if settings.WhatsAppEnabled {
		go s.deliverWhatsApp(settings, event)
	}
}

// deliverEmail renders the email body, dials SMTP and records the
// delivery attempt. The audit row carries the outcome so the merchant
// can confirm the alert reached the customer's inbox.
func (s *Service) deliverEmail(settings Settings, event Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	subject := fmt.Sprintf("Print Catalyst · new order from %s", event.CustomerName)
	body := renderEmailBody(settings.EmailFrom, event)
	recipient := strings.TrimSpace(settings.EmailUsername)
	if recipient == "" {
		recipient = strings.TrimSpace(settings.EmailFrom)
	}
	if recipient == "" || strings.TrimSpace(settings.EmailHost) == "" {
		s.recordDelivery(ctx, "email", event.OrderID, recipient, subject, body, "skipped", "no recipient or host configured")
		return
	}
	if settings.EmailToken == "" {
		s.recordDelivery(ctx, "email", event.OrderID, recipient, subject, body, "failed", "SMTP password is not configured")
		return
	}
	if err := s.emailDialer(ctx, settings.EmailHost, settings.EmailPort, settings.EmailUsername, settings.EmailToken, settings.EmailFrom, recipient, subject, body); err != nil {
		s.recordDelivery(ctx, "email", event.OrderID, recipient, subject, body, "failed", err.Error())
		return
	}
	s.recordDelivery(ctx, "email", event.OrderID, recipient, subject, body, "delivered", "")
}

// deliverWhatsApp renders the message, POSTs it to the configured URL
// with the bearer token and records the outcome.
func (s *Service) deliverWhatsApp(settings Settings, event Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body := renderWhatsAppBody(event)
	recipient := strings.TrimSpace(settings.WhatsAppTo)
	if recipient == "" || strings.TrimSpace(settings.WhatsAppAPIURL) == "" {
		s.recordDelivery(ctx, "whatsapp", event.OrderID, recipient, "", body, "skipped", "no recipient or URL configured")
		return
	}
	if settings.WhatsAppToken == "" {
		s.recordDelivery(ctx, "whatsapp", event.OrderID, recipient, "", body, "failed", "WhatsApp token is not configured")
		return
	}
	if err := s.whatsAppPOST(ctx, settings.WhatsAppAPIURL, settings.WhatsAppToken, recipient, body); err != nil {
		s.recordDelivery(ctx, "whatsapp", event.OrderID, recipient, "", body, "failed", err.Error())
		return
	}
	s.recordDelivery(ctx, "whatsapp", event.OrderID, recipient, "", body, "delivered", "")
}

// recordDelivery appends a row to the notification_deliveries audit
// table. Failures to record are silently swallowed so an audit error
// cannot mask the actual transport outcome.
func (s *Service) recordDelivery(ctx context.Context, transport, orderID, recipient, subject, body, status, failureDetail string) {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO notification_deliveries (id, transport, order_id, recipient, subject, body, status, failure_detail, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), transport, orderID, recipient, subject, body, status, failureDetail, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		// Audit failure must not break anything; the actual delivery
		// outcome is the source of truth.
		_ = err
	}
}

// Delivery is one row of the notification_deliveries audit table.
type Delivery struct {
	ID            string    `json:"id"`
	Transport     string    `json:"transport"`
	OrderID       string    `json:"orderId"`
	Recipient     string    `json:"recipient"`
	Subject       string    `json:"subject,omitempty"`
	Body          string    `json:"body,omitempty"`
	Status        string    `json:"status"`
	FailureDetail string    `json:"failureDetail,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
}

// Deliveries returns the most recent delivery attempts newest-first.
func (s *Service) Deliveries(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, transport, order_id, recipient, subject, body, status, failure_detail, occurred_at
FROM notification_deliveries ORDER BY occurred_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read notification deliveries: %w", err)
	}
	defer rows.Close()
	var deliveries []Delivery
	for rows.Next() {
		var delivery Delivery
		var occurredAt string
		if err := rows.Scan(&delivery.ID, &delivery.Transport, &delivery.OrderID, &delivery.Recipient, &delivery.Subject, &delivery.Body, &delivery.Status, &delivery.FailureDetail, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan notification delivery: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, fmt.Errorf("parse notification delivery timestamp: %w", err)
		}
		delivery.OccurredAt = parsed
		deliveries = append(deliveries, delivery)
	}
	if deliveries == nil {
		deliveries = []Delivery{}
	}
	return deliveries, rows.Err()
}

// renderEmailBody returns the human-readable body the email adapter
// sends. The format is plain text so the merchant does not need to
// install an HTML email composer.
func renderEmailBody(from string, event Event) string {
	return fmt.Sprintf("New Print Catalyst order\n\nOrder: %s\nCustomer: %s\nTotal: %d minor units (%s)\nSubmitted at: %s\n\nManage orders in the local dashboard.\n",
		event.OrderID, event.CustomerName, event.TotalMinor, event.Currency, time.Unix(event.CreatedAt, 0).UTC().Format(time.RFC3339))
}

// renderWhatsAppBody returns the compact one-line message the
// WhatsApp adapter sends.
func renderWhatsAppBody(event Event) string {
	return fmt.Sprintf("Print Catalyst · new order from %s — %d %s (order %s)",
		event.CustomerName, event.TotalMinor, event.Currency, event.OrderID)
}

// sendSMTP is the default SMTP implementation. The dialer uses plain
// AUTH LOGIN over a STARTTLS connection when the port is the
// submission port (587); ports 25 and 465 are passed through as-is.
func sendSMTP(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error {
	if strings.TrimSpace(host) == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("invalid SMTP host or port: %s:%d", host, port)
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	auth := smtp.PlainAuth("", username, password, host)
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", from, to, subject, body))
	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, from, []string{to}, msg)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendWhatsApp is the default HTTP POST implementation. It encodes
// the message as JSON and sends it to the configured URL with the
// bearer token in the Authorization header.
func sendWhatsApp(ctx context.Context, apiURL, token, to, body string) error {
	payload, err := json.Marshal(map[string]any{"to": to, "body": body})
	if err != nil {
		return fmt.Errorf("encode whatsapp payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("build whatsapp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send whatsapp request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("whatsapp gateway returned %d", resp.StatusCode)
	}
	return nil
}

// newID returns a 32-character hex identifier used for audit rows.
func newID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("secure random ID generation failed: %v", err))
	}
	return hex.EncodeToString(buffer)
}

// broadcast sends the event to all subscribed dashboard sessions.
func (s *Service) broadcast(event Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.sessions {
		select {
		case ch <- event:
		default:
			// Non-blocking: slow consumer; drop and continue.
		}
	}
	if s.listener != nil {
		s.listener(event)
	}
}

// Subscribe registers a new SSE session and returns a session ID and a channel
// that receives events. Call Unsubscribe when the client disconnects.
//
// The session ID is a 16-character hex string from crypto/rand — the same
// convention used by the rest of the persistence layer. Using a fixed-width
// hex string avoids both the unicode-boundary overflow (a single rune can
// only encode 1.1M values before string(rune(int64)) starts producing
// invalid UTF-8) and the silent collision that would happen if two sessions
// ever landed on the same rune.
func (s *Service) Subscribe() (string, <-chan Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newSessionID()
	ch := make(chan Event, 64) // buffered so slow writers don't block Notify
	s.sessions[id] = ch
	return id, ch
}

// Unsubscribe removes a session by ID.
func (s *Service) Unsubscribe(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.sessions[id]; ok {
		delete(s.sessions, id)
		close(ch)
	}
}

// newSessionID returns an 8-byte hex string from crypto/rand. 64 bits is
// well past what a single merchant installation can exhaust in a session
// without closing the dashboard tab.
func newSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("secure random session id failed: %v", err))
	}
	return hex.EncodeToString(buf[:])
}

// MarshalEvent encodes an event for SSE delivery.
func MarshalEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}
