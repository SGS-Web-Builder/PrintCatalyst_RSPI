package pricing

import (
	"context"
	"database/sql"
)

func (s *Service) Revision(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT revision FROM price_revision WHERE singleton=1").Scan(&n)
	return n, err
}
func checkRevision(ctx context.Context, tx *sql.Tx, expected *int64) error {
	if _, err := tx.ExecContext(ctx, "UPDATE price_revision SET revision=revision WHERE singleton=1"); err != nil {
		return err
	}
	var n int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM price_revision WHERE singleton=1").Scan(&n); err != nil {
		return err
	}
	if expected != nil && n != *expected {
		return ErrConflict
	}
	return nil
}
