// Package webnotify delivers optional, durable payment notifications separately
// from payment verification and physical print release.
package webnotify

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type Service struct {
	db              *sql.DB
	public, private string
	client          *http.Client
	mu              sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
	send            func(context.Context, []byte, *webpush.Subscription, int) (int, error)
}

func New(db *sql.DB) (*Service, error) {
	s := &Service{db: db}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`INSERT OR IGNORE INTO webpush_keys VALUES(1,?,?)`, private, public); err != nil {
		return nil, err
	}
	if err = db.QueryRow(`SELECT private_key,public_key FROM webpush_keys WHERE singleton=1`).Scan(&s.private, &s.public); err != nil {
		return nil, err
	}
	s.client = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DialContext: publicDial}}
	s.send = func(ctx context.Context, body []byte, sub *webpush.Subscription, ttl int) (int, error) {
		response, err := webpush.SendNotificationWithContext(ctx, body, sub, &webpush.Options{HTTPClient: s.client, Subscriber: "https://printcatalyst.in", VAPIDPublicKey: s.public, VAPIDPrivateKey: s.private, TTL: ttl})
		if err != nil {
			return 0, errors.New("push delivery failed")
		}
		defer response.Body.Close()
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return response.StatusCode, nil
	}
	return s, nil
}
func (s *Service) PublicKey() string { return s.public }

// Only known browser push services; no arbitrary customer-supplied destinations.
func Validate(sub webpush.Subscription) error {
	u, e := url.Parse(sub.Endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || len(sub.Endpoint) > 2048 {
		return errors.New("unsupported push endpoint")
	}
	allowed := (u.Hostname() == "fcm.googleapis.com" && strings.HasPrefix(u.Path, "/fcm/send/")) || (u.Hostname() == "updates.push.services.mozilla.com" && strings.HasPrefix(u.Path, "/wpush/"))
	if !allowed {
		return errors.New("this browser push provider is not supported")
	}
	auth, e := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if e != nil || len(auth) != 16 {
		return errors.New("invalid push authentication key")
	}
	key, e := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if e != nil {
		return errors.New("invalid push key")
	}
	if _, e = ecdh.P256().NewPublicKey(key); e != nil {
		return errors.New("invalid push key")
	}
	return nil
}
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range ips {
		if !a.IP.IsGlobalUnicast() || a.IP.IsPrivate() || a.IP.IsLoopback() || a.IP.IsLinkLocalUnicast() {
			return nil, errors.New("non-public push destination")
		}
	}
	for _, a := range ips {
		c, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if e == nil {
			return c, nil
		}
	}
	return nil, errors.New("push destination unavailable")
}
func (s *Service) Subscribe(ctx context.Context, id string, sub webpush.Subscription) error {
	if err := Validate(sub); err != nil {
		return err
	}
	raw, _ := json.Marshal(sub)
	_, err := s.db.ExecContext(ctx, `INSERT INTO webpush_orders(order_id,subscription,created_at) VALUES(?,?,?) ON CONFLICT(order_id) DO UPDATE SET subscription=excluded.subscription`, id, string(raw), time.Now().Unix())
	return err
}
func (s *Service) Unsubscribe(ctx context.Context, id string) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM webpush_orders WHERE order_id=?`, id)
	return e
}
func (s *Service) tick(ctx context.Context) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `DELETE FROM webpush_orders WHERE created_at<?`, now-2*86400)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.order_id,n.subscription,o.share_token,p.expires_at FROM webpush_orders n JOIN orders o ON o.id=n.order_id JOIN kiosk_pickups p ON p.order_id=o.id WHERE n.sent_at IS NULL AND n.attempts<8 AND n.next_attempt<=? AND o.status='paid' AND p.payment_reference<>'' AND p.state='active' AND p.expires_at>? LIMIT 10`, now, now)
	if err != nil {
		return err
	}
	type item struct {
		id, raw, token string
		expires        int64
	}
	var items []item
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.raw, &it.token, &it.expires); err != nil {
			break
		}
		items = append(items, it)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE webpush_orders SET attempts=attempts+1,next_attempt=? WHERE order_id=?`, now+60, it.id); err != nil {
			return err
		}
		var sub webpush.Subscription
		if json.Unmarshal([]byte(it.raw), &sub) != nil || Validate(sub) != nil {
			continue
		}
		// No OTP appears on the lock screen. The encrypted push contains an order
		// capability in notification data for recovery even if the original tab died.
		message := "Tap to view your pickup code."
		var otp bool
		if e := s.db.QueryRowContext(ctx, "SELECT otp_enabled FROM kiosk_control WHERE singleton=1").Scan(&otp); e != nil {
			return e
		}
		if !otp {
			message = "Your paid order will print automatically when ready. Tap to view its status."
		}
		body, _ := json.Marshal(map[string]any{"title": "Payment received", "body": message, "orderId": it.id, "token": it.token, "expires": it.expires})
		ttl := int(it.expires - now)
		if ttl > 300 {
			ttl = 300
		}
		if ttl <= 0 {
			continue
		}
		status, e := s.send(ctx, body, &sub, ttl)
		if e == nil && status >= 200 && status < 300 {
			_, err = s.db.ExecContext(ctx, `UPDATE webpush_orders SET sent_at=? WHERE order_id=?`, now, it.id)
		}
		if e == nil && (status == 404 || status == 410 || (status >= 400 && status < 500 && status != 429)) {
			err = s.Unsubscribe(ctx, it.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Name() string { return "customer-push" }
func (s *Service) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = s.tick(ctx)
			}
		}
	}()
	return nil
}
func (s *Service) Ready(context.Context) error { return nil }
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.cancel = nil
	s.mu.Unlock()
	return nil
}
