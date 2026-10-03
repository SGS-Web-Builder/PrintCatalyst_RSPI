// Package autodelete removes completed print files and enforces a 24-hour upload lifetime.
// Order and document metadata remain available for accounting and cross-checking.
package autodelete

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
)

// SweeperConfig bundles the tunables for the sweeper loop.
type SweeperConfig struct {
	// SweepInterval is how often the background loop runs. Defaults
	// to one second; the dashboard's "purge now" button can also
	// trigger an out-of-band pass via PurgeExpired.
	SweepInterval time.Duration
	// RetentionGrace is retained for compatibility; the fixed policy ignores it.
	RetentionGrace time.Duration
}

// DefaultSweeperConfig returns the production defaults.
func DefaultSweeperConfig() SweeperConfig {
	return SweeperConfig{
		SweepInterval:  time.Second,
		RetentionGrace: time.Hour,
	}
}

// Sweeper is the document lifecycle service.
type Sweeper struct {
	db        *sql.DB
	docs      *documents.Service
	config    SweeperConfig
	mu        sync.Mutex
	lastError error
}

// New returns a Sweeper ready to run. backend is optional; without it
// the sweeper only handles the retention path.
func New(db *sql.DB, docs *documents.Service, config SweeperConfig) *Sweeper {
	if config.SweepInterval <= 0 {
		config.SweepInterval = time.Second
	}
	if config.RetentionGrace < 0 {
		config.RetentionGrace = 0
	}
	return &Sweeper{db: db, docs: docs, config: config}
}

// Run is the main loop. It returns when ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context) error {
	if s.docs == nil {
		return errors.New("autodelete: documents service is required")
	}
	ticker := time.NewTicker(s.config.SweepInterval)
	defer ticker.Stop()
	if _, err := s.PurgeExpired(ctx); err != nil {
		s.setLastError(err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.PurgeExpired(ctx); err != nil {
				s.setLastError(err)
			}
		}
	}
}

// MarkDispatched records spool submission only; it does not authorize deletion.
func (s *Sweeper) MarkDispatched(ctx context.Context, documentID string) error {
	if documentID == "" {
		return errors.New("document id is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE documents SET dispatched_at = ? WHERE id = ?`,
		time.Now().Unix(), documentID)
	return err
}

// PurgeExpired removes confirmed-complete files or unpaid uploads at least 24 hours old.
// Paid/uncollected kiosk orders survive code expiry and await completion or cancellation.
// Spool submission alone is not completion. Legacy retention settings do not override this policy.
func (s *Sweeper) PurgeExpired(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT d.id,d.storage_path FROM documents d
JOIN orders o ON o.id=d.order_id
WHERE d.purged_at=0 AND (
 (d.created_at<=? AND o.status NOT IN ('paid','dispatched')
  AND NOT EXISTS(SELECT 1 FROM kiosk_pickups p WHERE p.order_id=o.id AND o.status NOT IN ('completed','cancelled')))
 OR o.status='completed' OR
 (EXISTS(SELECT 1 FROM order_lines l WHERE l.document_id=d.id)
  AND NOT EXISTS(SELECT 1 FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id
   WHERE l.document_id=d.id AND COALESCE(j.progress,'')<>'completed')) OR
 (EXISTS(SELECT 1 FROM order_lines l WHERE l.order_id=o.id)
  AND NOT EXISTS(SELECT 1 FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id
   WHERE l.order_id=o.id AND COALESCE(j.progress,'')<>'completed')))
`, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		return 0, fmt.Errorf("query purge candidates: %w", err)
	}
	defer rows.Close()
	var purged int
	var paths []string
	var ids []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return purged, err
		}
		ids = append(ids, id)
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return purged, err
	}
	rows.Close()
	var failures []error
	for i, path := range paths {
		if err := s.docs.Delete(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("purge blob %s: %w", path, err))
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE documents SET purged_at=? WHERE id=?`, time.Now().Unix(), ids[i]); err != nil {
			failures = append(failures, fmt.Errorf("mark purged document %s: %w", ids[i], err))
			continue
		}
		purged++
	}
	err = errors.Join(failures...)
	s.setLastError(err)
	return purged, err
}

// PurgeOrder explicitly removes all files for an order while retaining metadata.
func (s *Sweeper) PurgeOrder(ctx context.Context, orderID string) (int, error) {
	if orderID == "" {
		return 0, errors.New("order id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT d.id, d.storage_path FROM documents d WHERE d.order_id = ? AND d.purged_at=0`, orderID)
	if err != nil {
		return 0, fmt.Errorf("query order documents: %w", err)
	}
	defer rows.Close()
	var ids []string
	var paths []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return 0, err
		}
		ids = append(ids, id)
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	purged := 0
	rows.Close()
	var failures []error
	for i, path := range paths {
		if err := s.docs.Delete(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("purge %s: %w", path, err))
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE documents SET purged_at=? WHERE id=?`, time.Now().Unix(), ids[i]); err != nil {
			failures = append(failures, fmt.Errorf("mark purged document %s: %w", ids[i], err))
			continue
		}
		purged++
	}
	return purged, errors.Join(failures...)
}

func (s *Sweeper) setLastError(err error) {
	s.mu.Lock()
	s.lastError = err
	s.mu.Unlock()
}

// LastError returns the most recent sweeper error.
func (s *Sweeper) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}
