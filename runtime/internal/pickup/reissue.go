package pickup

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
)

// ReissueExpired is owner-authorized recovery, never payment verification or
// physical release. Only an existing verified pickup with no dispatch history
// can receive a replacement. Retrying a successful request keeps its new code.
func (s *Service) ReissueExpired(ctx context.Context, order string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, status, reference string
	var expires int64
	var oldHash []byte
	err = tx.QueryRowContext(ctx, `SELECT p.state,p.expires_at,p.code_hash,p.payment_reference,o.status
 FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id WHERE p.order_id=?`, order).Scan(&state, &expires, &oldHash, &reference, &status)
	if err != nil {
		return ErrUnavailable
	}
	if status != "paid" || reference == "" || (state != "active" && state != "expired") {
		return ErrUnavailable
	}
	var dispatched bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kiosk_releases WHERE order_id=?)
 OR EXISTS(SELECT 1 FROM print_submissions j JOIN order_lines l ON l.id=j.line_id WHERE l.order_id=?)`, order, order).Scan(&dispatched)
	if err != nil {
		return err
	}
	if dispatched {
		return ErrUnavailable
	}
	now := s.now().Unix()
	if state == "active" && expires > now {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kiosk_pickups SET state='expired' WHERE state='active' AND expires_at<=?`, now); err != nil {
		return err
	}
	start, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return err
	}
	for i := 0; i < 10000; i++ {
		code := fmt.Sprintf("%04d", (int(start.Int64())+i)%10000)
		hash := s.hash(code)
		if string(hash) == string(oldHash) {
			continue
		}
		var used bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kiosk_pickups WHERE state='active' AND code_hash=?)`, hash).Scan(&used); err != nil {
			return err
		}
		if used {
			continue
		}
		nonce := make([]byte, s.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return err
		}
		sealed := s.aead.Seal(nonce, nonce, []byte(code), []byte(order))
		if _, err = tx.ExecContext(ctx, `UPDATE kiosk_pickups SET state='active',code_hash=?,code_cipher=?,expires_at=? WHERE order_id=?`, hash, sealed, now+86400, order); err != nil {
			return err
		}
		return tx.Commit()
	}
	return ErrCapacity
}
