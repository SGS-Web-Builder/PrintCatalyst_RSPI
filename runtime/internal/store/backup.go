package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

// VACUUM INTO takes a consistent SQLite snapshot, including committed WAL data.
// This is a protected local rollback snapshot, not an encrypted export backup.
func (s *Store) backupBeforeMigration(ctx context.Context, version string) (result error) {
	files, err := localfiles.New(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	name := filepath.Base(s.path) + ".before-" + version + "-" + fmt.Sprint(time.Now().UnixNano()) + ".backup"
	if err := files.WriteAtomic(name, strings.NewReader("")); err != nil {
		return fmt.Errorf("prepare migration backup: %w", err)
	}
	defer func() {
		if result != nil {
			_ = files.Remove(name)
		}
	}()
	if _, err := s.database.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(filepath.Dir(s.path), name)); err != nil {
		return fmt.Errorf("snapshot before migration: %w", err)
	}
	snapshot, err := sql.Open("sqlite", filepath.Join(filepath.Dir(s.path), name))
	if err != nil {
		return err
	}
	defer snapshot.Close()
	var integrity string
	if err := snapshot.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("verify migration snapshot: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("migration snapshot integrity check failed")
	}
	return nil
}
