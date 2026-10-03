package tunnel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service owns the durable tunnel state. The HTTP layer and the dashboard
// both call into it. All writes go through a single transaction so the
// snapshot in tunnel_state and the append in tunnel_events can never drift.
type Service struct {
	database  *sql.DB
	now       func() time.Time
	newID     func() string
	monotonic int64
}

// New wires a Service to the local SQLite database. now and newID are
// overridable for deterministic tests.
func New(database *sql.DB) *Service {
	return &Service{database: database, now: time.Now, newID: randomID}
}

// monotonicNow returns a UnixMilli timestamp that is strictly greater than
// every previous timestamp the service has produced, even when two writes
// happen in the same real-time millisecond. It is the only allowed source
// of occurred_at values so the events list orders deterministically.
func (s *Service) monotonicNow() int64 {
	base := s.now().UnixMilli()
	if base <= s.monotonic {
		base = s.monotonic + 1
	}
	s.monotonic = base
	return base
}

// Snapshot returns the current tunnel state. It is safe to call before any
// row exists; the zero value reflects an unconfigured tunnel.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	var (
		provider           string
		publicOrigin       string
		tokenRef           string
		status             string
		lastVerifiedAt     sql.NullInt64
		lastVerifiedStatus sql.NullInt64
		lastVerifiedError  string
		lastAttemptAt      sql.NullInt64
		lastError          string
		qrTargetPath       string
		shopRoute          string
		updatedAt          int64
	)
	err := s.database.QueryRowContext(ctx, `
SELECT provider, public_origin, tunnel_token_ref, status,
       last_verified_at, last_verified_status, last_verified_error,
       last_attempt_at, last_error, qr_target_path, shop_route, updated_at
FROM tunnel_state WHERE singleton = 1`).Scan(
		&provider, &publicOrigin, &tokenRef, &status,
		&lastVerifiedAt, &lastVerifiedStatus, &lastVerifiedError,
		&lastAttemptAt, &lastError, &qrTargetPath, &shopRoute, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{
			Provider:     ProviderDirect,
			PublicOrigin: "",
			PublicURL:    "",
			Status:       StatusUnconfigured,
			QRTargetPath: "/portal/",
			UpdatedAt:    s.now().Unix(),
		}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read tunnel state: %w", err)
	}
	return Snapshot{
		Provider:               Provider(provider),
		PublicOrigin:           publicOrigin,
		PublicURL:              joinPublicURL(publicOrigin, qrTargetPath, shopRoute),
		TunnelTokenFingerprint: tokenRef,
		Status:                 Status(status),
		LastVerifiedAt:         lastVerifiedAt.Int64,
		LastVerifiedStatus:     int(lastVerifiedStatus.Int64),
		LastVerifiedError:      lastVerifiedError,
		LastAttemptAt:          lastAttemptAt.Int64,
		LastError:              lastError,
		ShopRoute:              shopRoute,
		QRTargetPath:           qrTargetPath,
		QRTargetURL:            joinPublicURL(publicOrigin, qrTargetPath, ""),
		HasToken:               strings.TrimSpace(tokenRef) != "",
		UpdatedAt:              updatedAt,
	}, nil
}

// SaveConfig writes a new configuration and returns the resulting snapshot.
// The tunnel token, when supplied, is stored only as a fingerprint — the
// raw value never touches disk, only the SHA-256-derived 12-char digest.
func (s *Service) SaveConfig(ctx context.Context, in Config) (Snapshot, error) {
	normalized, err := normalizeConfig(in)
	if err != nil {
		return Snapshot{}, err
	}
	fingerprint := ""
	if normalized.TunnelToken != "" {
		fingerprint = Fingerprint(normalized.TunnelToken)
	}
	now := s.monotonicNow()
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin tunnel config: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tunnel_state (singleton, provider, public_origin, tunnel_token_ref, status,
                          qr_target_path, shop_route, updated_at)
VALUES (1, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
  status = 'unconfigured',
  last_verified_at = NULL,
  last_verified_status = NULL,
  last_verified_error = '',
  provider        = excluded.provider,
  public_origin   = excluded.public_origin,
  tunnel_token_ref = CASE WHEN excluded.tunnel_token_ref = ''
                          THEN tunnel_state.tunnel_token_ref
                          ELSE excluded.tunnel_token_ref END,
  qr_target_path  = excluded.qr_target_path,
  shop_route      = excluded.shop_route,
  updated_at      = excluded.updated_at`,
		string(normalized.Provider), normalized.PublicOrigin, fingerprint, string(StatusUnconfigured),
		normalized.QRTargetPath, normalized.ShopRoute, now,
	); err != nil {
		return Snapshot{}, fmt.Errorf("write tunnel state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tunnel_events (id, occurred_at, status, detail)
VALUES (?, ?, 'unconfigured', ?)`,
		s.newID(), now, fmt.Sprintf("config saved; provider=%s; origin=%q; token=%s",
			normalized.Provider, normalized.PublicOrigin, fingerprintOrNone(fingerprint)),
	); err != nil {
		return Snapshot{}, fmt.Errorf("record tunnel event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit tunnel config: %w", err)
	}
	return s.Snapshot(ctx)
}

// SetStatus records a status transition outside of a probe. It is used by
// the supervisor to mark the tunnel as starting before the probe runs and
// as offline after a graceful shutdown.
func (s *Service) SetStatus(ctx context.Context, status Status, detail string) (Snapshot, error) {
	if !validStatus(string(status)) {
		return Snapshot{}, fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
	}
	now := s.monotonicNow()
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin tunnel status: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
UPDATE tunnel_state SET status = ?, last_error = ?, updated_at = ?
WHERE singleton = 1`,
		string(status), detail, now,
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("write tunnel status: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// No row yet — insert the singleton so the supervisor can mark an
		// unconfigured tunnel as starting.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tunnel_state (singleton, provider, public_origin, tunnel_token_ref, status,
                          qr_target_path, shop_route, updated_at, last_error)
VALUES (1, 'cloudflared', '', '', ?, '/portal/', '', ?, ?)
ON CONFLICT(singleton) DO NOTHING`,
			string(status), now, detail,
		); err != nil {
			return Snapshot{}, fmt.Errorf("insert tunnel status: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tunnel_events (id, occurred_at, status, detail)
VALUES (?, ?, ?, ?)`,
		s.newID(), now, string(status), detail,
	); err != nil {
		return Snapshot{}, fmt.Errorf("record tunnel event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit tunnel status: %w", err)
	}
	return s.Snapshot(ctx)
}

// ProbeAndRecord actively probes the configured public origin and records
// the outcome. It returns the snapshot after the probe completes.
func (s *Service) ProbeAndRecord(ctx context.Context, opts ...VerifyOption) (Snapshot, VerifyResult, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, VerifyResult{}, err
	}
	if snap.PublicOrigin == "" {
		return snap, VerifyResult{
			Status: StatusUnconfigured,
			Error:  "public origin is not configured",
		}, nil
	}
	// Mark the tunnel as verifying while the probe is in flight.
	verifying, err := s.SetStatus(ctx, StatusVerifying, "public origin probe in progress")
	if err != nil {
		return snap, VerifyResult{}, err
	}
	result := Verify(ctx, snap.PublicOrigin, append([]VerifyOption{WithExpectedPath(snap.QRTargetPath)}, opts...)...)
	now := s.monotonicNow()
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return verifying, result, fmt.Errorf("begin tunnel probe: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE tunnel_state SET
  status                = ?,
  last_verified_at      = ?,
  last_verified_status  = ?,
  last_verified_error   = ?,
  last_attempt_at       = ?,
  last_error            = ?,
  updated_at            = ?
WHERE singleton = 1`,
		string(result.Status), now, result.HTTPStatus, result.Error,
		now, result.Error, now,
	); err != nil {
		return verifying, result, fmt.Errorf("write tunnel probe: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tunnel_events (id, occurred_at, status, detail, http_status, round_trip_ms)
VALUES (?, ?, ?, ?, ?, ?)`,
		s.newID(), now, string(result.Status),
		describeProbe(result), result.HTTPStatus, result.RoundTripMs,
	); err != nil {
		return verifying, result, fmt.Errorf("record tunnel probe event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return verifying, result, fmt.Errorf("commit tunnel probe: %w", err)
	}
	final, err := s.Snapshot(ctx)
	if err != nil {
		return verifying, result, err
	}
	return final, result, nil
}

// Events returns the most recent tunnel transitions, newest first.
func (s *Service) Events(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.database.QueryContext(ctx, `
SELECT id, occurred_at, status, detail, http_status, round_trip_ms
FROM tunnel_events
ORDER BY occurred_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read tunnel events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var (
			e          Event
			httpStatus sql.NullInt64
			rtt        sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Status, &e.Detail, &httpStatus, &rtt); err != nil {
			return nil, fmt.Errorf("scan tunnel event: %w", err)
		}
		e.HTTPStatus = int(httpStatus.Int64)
		e.RoundTripMs = rtt.Int64
		events = append(events, e)
	}
	return events, rows.Err()
}

// Event is the audit row shape exposed by Events.
type Event struct {
	ID          string `json:"id"`
	OccurredAt  int64  `json:"occurredAt"`
	Status      Status `json:"status"`
	Detail      string `json:"detail"`
	HTTPStatus  int    `json:"httpStatus"`
	RoundTripMs int64  `json:"roundTripMs"`
}

// joinPublicURL composes the public URL the QR code points at. The shop
// route is appended (with a single slash) only when the QR target path does
// not already end in the shop route.
func joinPublicURL(origin, path, shopRoute string) string {
	if origin == "" {
		return ""
	}
	base := strings.TrimRight(origin, "/")
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		cleaned = "/portal/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	full := base + cleaned
	if shopRoute != "" && !strings.Contains(full, shopRoute) {
		if !strings.HasSuffix(full, "/") {
			full += "/"
		}
		full += strings.TrimPrefix(shopRoute, "/")
	}
	return full
}

func describeProbe(r VerifyResult) string {
	switch r.Status {
	case StatusOnline:
		return fmt.Sprintf("public origin returned %d in %d ms", r.HTTPStatus, r.RoundTripMs)
	case StatusDegraded:
		return fmt.Sprintf("public origin returned %d (degraded)", r.HTTPStatus)
	case StatusOffline:
		return fmt.Sprintf("public origin returned %d (offline)", r.HTTPStatus)
	default:
		if r.Error != "" {
			return r.Error
		}
		return "public origin probe failed"
	}
}

func fingerprintOrNone(fp string) string {
	if fp == "" {
		return "none"
	}
	return fp
}

func validStatus(s string) bool {
	switch Status(s) {
	case StatusUnconfigured, StatusStarting, StatusVerifying,
		StatusOnline, StatusDegraded, StatusOffline, StatusError:
		return true
	}
	return false
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("secure random ID generation failed: %v", err))
	}
	return hex.EncodeToString(buf)
}
