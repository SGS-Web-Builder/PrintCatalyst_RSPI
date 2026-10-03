// Package dispatch provides a QueueResolver implementation backed by SQLite.
// The resolver reads the printers table directly so the dispatch package does
// not need to import the printers service (no circular dependency).
package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// DBQueueResolver reads printer queue names from the database directly,
// avoiding a circular dependency between the dispatch and printers packages.
// It satisfies the QueueResolver interface declared in dispatch.go.
type DBQueueResolver struct {
	db *sql.DB
}

// NewDBQueueResolver returns a DBQueueResolver bound to the supplied database.
func NewDBQueueResolver(db *sql.DB) *DBQueueResolver {
	return &DBQueueResolver{db: db}
}

// QueueFor returns the spooler queue name for the given printer ID.
// Returns "" with nil error when the printer is not found or has been
// removed. The dispatcher treats an empty string as "no queue resolved"
// and skips dispatching to that printer so a misconfigured installation
// does not silently spam the first available printer.
func (r *DBQueueResolver) QueueFor(ctx context.Context, printerID string) (string, error) {
	if printerID == "" {
		return "", nil
	}
	var queue string
	err := r.db.QueryRowContext(ctx,
		`SELECT queue_name FROM printers WHERE id=? AND enabled=1 AND removed_at IS NULL`,
		printerID).Scan(&queue)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return queue, err
}

// DefaultQueue returns the queue name of the merchant's default printer
// (the one marked is_default=1). Returns "" with nil error when no
// default printer is configured.
func (r *DBQueueResolver) DefaultQueue(ctx context.Context) (string, error) {
	var primary string
	if err := r.db.QueryRowContext(ctx, `SELECT primary_printer_id FROM business_settings WHERE singleton=1`).Scan(&primary); err != nil {
		return "", err
	}
	if primary != "" {
		return r.QueueFor(ctx, primary)
	}
	var queue string
	err := r.db.QueryRowContext(ctx,
		`SELECT queue_name FROM printers WHERE is_default=1 AND enabled=1 AND removed_at IS NULL LIMIT 1`,
	).Scan(&queue)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return queue, err
}

// CompatibleQueue rechecks capabilities at dispatch in case a driver changed
// after checkout. A configured primary printer is a constraint, not a hint.
func (r *DBQueueResolver) CompatibleQueue(ctx context.Context, selected, service string, ref DocumentRef) (string, error) {
	if selected == "" && service == "" {
		if err := r.db.QueryRowContext(ctx, "SELECT primary_printer_id FROM business_settings WHERE singleton=1").Scan(&selected); err != nil {
			return "", err
		}
	}
	fleet, err := printers.Eligible(ctx, r.db, service, selected)
	if err != nil {
		return "", err
	}
	for _, p := range fleet {
		if p.Supports(ref.PaperSize, ref.ColourMode, ref.Sides) {
			return p.Queue, nil
		}
	}
	return "", errors.New("no eligible printer supports this print configuration")
}
