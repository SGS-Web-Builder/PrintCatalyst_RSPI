package kiosk

import (
	"context"
	"database/sql"
	"time"
)

type bucket struct {
	scope               string
	window, limit, base int64
}

var buckets = []bucket{{"installation", 300, 30, 60}, {"physical-kiosk", 30, 5, 30}}

type usage struct{ start, attempts, blocked, strikes, seen int64 }

// reserve counts every authenticated attempt, including malformed or correct
// codes. Reservations commit before code lookup; crashes cannot erase attempts.
// Both buckets are installation-owned, never selected by a request parameter.
func (s *Server) reserve(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	values := make([]usage, len(buckets))
	for i, b := range buckets {
		v := &values[i]
		err = tx.QueryRowContext(ctx, "SELECT window_start,attempts,blocked_until,strikes,last_seen FROM kiosk_throttle WHERE scope=?", b.scope).Scan(&v.start, &v.attempts, &v.blocked, &v.strikes, &v.seen)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
		if now < v.seen {
			return v.seen - now + 1, nil
		}
		if now < v.blocked {
			return v.blocked - now, nil
		}
		if now-v.seen >= int64((10 * time.Minute).Seconds()) {
			v.strikes = 0
		}
		if now-v.start >= b.window {
			v.start = now
			v.attempts = 0
		}
		if v.attempts >= b.limit {
			delay := b.base * (1 << min(v.strikes, 5))
			v.strikes = min(v.strikes+1, 5)
			v.blocked = now + delay
			v.seen = now
			if err = saveUsage(ctx, tx, b.scope, *v); err != nil {
				return 0, err
			}
			if err = tx.Commit(); err != nil {
				return 0, err
			}
			return delay, nil
		}
	}
	for i, b := range buckets {
		v := values[i]
		v.attempts++
		v.seen = now
		if err = saveUsage(ctx, tx, b.scope, v); err != nil {
			return 0, err
		}
	}
	return 0, tx.Commit()
}
func saveUsage(ctx context.Context, tx *sql.Tx, scope string, v usage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO kiosk_throttle VALUES(?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET window_start=excluded.window_start,attempts=excluded.attempts,blocked_until=excluded.blocked_until,strikes=excluded.strikes,last_seen=excluded.last_seen`, scope, v.start, v.attempts, v.blocked, v.strikes, v.seen)
	return err
}
