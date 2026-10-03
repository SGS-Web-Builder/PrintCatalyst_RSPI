package printers

import (
	"context"
	"fmt"
)

// SetPaperEnabled replaces the current owner confirmation without changing driver capabilities.
func (s *Service) SetPaperEnabled(ctx context.Context, id, paper string, enabled bool, actor string) error {
	p, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	supported := false
	if p.Capabilities != nil {
		for _, size := range p.Capabilities.PaperSizes {
			if size.Key == paper {
				supported = true
			}
		}
	}
	if !supported {
		return fmt.Errorf("%w: paper size is not reported by this printer", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	if _, err = tx.ExecContext(ctx, `UPDATE printer_verifications SET invalidated_at=? WHERE printer_id=? AND capability_type='paper_size' AND capability_key=? AND invalidated_at IS NULL`, now, id, paper); err != nil {
		return err
	}
	if enabled {
		key, e := randomID()
		if e != nil {
			return e
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO printer_verifications(id,printer_id,capability_type,capability_key,status,evidence,tested_at,verified_at,verified_by) VALUES(?,?,'paper_size',?,'confirmed','Owner confirmed paper size for customer orders',0,?,?)`, key, id, paper, now, actor)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
