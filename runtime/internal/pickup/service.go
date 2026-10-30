// Package pickup implements the durable pickup state machine. It exposes no HTTP
// routes. Its privileged methods must only be wired to verified payment,
// preparation workers and the separately authenticated physical kiosk listener.
package pickup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"
)

var (
	ErrUnavailable = errors.New("pickup code unavailable")
	ErrPreparing   = errors.New("documents are not ready; please wait or ask the merchant")
	ErrCapacity    = errors.New("pickup code capacity unavailable; retry later")
)

type Service struct {
	db      *sql.DB
	aead    cipher.AEAD
	hashKey []byte
	now     func() time.Time
	ready   chan struct{}
}

// New receives two independent 32-byte installation keys from protected storage.
// Key provisioning/rotation is intentionally not implemented as a plaintext fallback.
func New(db *sql.DB, encryptionKey, lookupKey []byte) (*Service, error) {
	if len(encryptionKey) != 32 || len(lookupKey) != 32 {
		return nil, errors.New("two 32-byte pickup keys are required")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, aead: aead, hashKey: append([]byte(nil), lookupKey...), now: time.Now, ready: make(chan struct{}, 1)}, nil
}
func (s *Service) hash(code string) []byte {
	h := hmac.New(sha256.New, s.hashKey)
	h.Write([]byte(code))
	return h.Sum(nil)
}
func (s *Service) decrypt(order string, raw []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(raw) < n {
		return "", ErrUnavailable
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(order))
	if err != nil {
		return "", ErrUnavailable
	}
	return string(plain), nil
}

// IssueVerified is a privileged worker operation, NEVER a customer/owner request.
// Recover requires gateway evidence or a separate authenticated cash receipt.
// A paid status alone must never be used by a caller as evidence of verification.
// Duplicate callbacks return the existing code; expiry does not silently reissue it.
func (s *Service) IssueVerified(ctx context.Context, order, reference string) (string, error) {
	if reference == "" {
		return "", ErrUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT status FROM orders WHERE id=?", order).Scan(&status); err != nil {
		return "", err
	}
	if status != "paid" {
		var eligible bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_release_authorizations WHERE order_id=? AND payment_reference=?)", order, reference).Scan(&eligible); err != nil {
			return "", err
		}
		if status != "pending_payment" || !eligible {
			return "", ErrUnavailable
		}
	}
	var raw []byte
	var state string
	var expiry, created int64
	err = tx.QueryRowContext(ctx, "SELECT code_cipher,state,expires_at,created_at FROM kiosk_pickups WHERE order_id=?", order).Scan(&raw, &state, &expiry, &created)
	if err == nil {
		end, e := dayEnd(ctx, tx, time.Unix(created, 0))
		if e != nil {
			return "", e
		}
		if state != "active" || expiry <= s.now().Unix() || end <= s.now().Unix() {
			return "", ErrUnavailable
		}
		return s.decrypt(order, raw)
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE kiosk_pickups SET state='expired' WHERE state='active' AND expires_at<=?", s.now().Unix()); err != nil {
		return "", err
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM kiosk_pickups WHERE expires_at>?", s.now().Unix()).Scan(&active); err != nil {
		return "", err
	}
	if active >= 10000 {
		return "", ErrCapacity
	}
	// Bounded probing covers the namespace without silently reusing an active code.
	start, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	for i := 0; i < 10000; i++ {
		code := fmt.Sprintf("%04d", (int(start.Int64())+i)%10000)
		hash := s.hash(code)
		var used bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_pickups WHERE expires_at>? AND code_hash=?)", s.now().Unix(), hash).Scan(&used); err != nil {
			return "", err
		}
		if used {
			continue
		}
		nonce := make([]byte, s.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return "", err
		}
		sealed := s.aead.Seal(nonce, nonce, []byte(code), []byte(order))
		now := s.now().Unix()
		expiry, e := dayEnd(ctx, tx, s.now())
		if e != nil {
			return "", e
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO kiosk_pickups(order_id,payment_reference,code_hash,code_cipher,state,expires_at,created_at) VALUES(?,?,?,?,'active',?,?)`, order, reference, hash, sealed, expiry, now)
		if err != nil {
			return "", err
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return code, nil
	}
	return "", ErrCapacity
}

// MarkPrepared is called only after immutable artifacts/settings have been
// validated. The digest will be bound into the release transaction.
func (s *Service) MarkPrepared(ctx context.Context, order, digest string) error {
	decoded, decodeErr := hex.DecodeString(digest)
	if decodeErr != nil || len(decoded) != 32 {
		return errors.New("prepared SHA-256 digest required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE kiosk_pickups SET preparation='ready',prepared_digest=? WHERE order_id=? AND state='active' AND (preparation<>'ready' OR prepared_digest=?)`, digest, order, digest)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnavailable
	}
	return nil
}

// Claim must only be called after kiosk authentication and durable rate limiting.
// It performs no licence, gateway or printer calls. A committed release survives
// restart. Repeated/parallel entries cannot create another release.
func (s *Service) Claim(ctx context.Context, code string) (string, error) {
	return s.claim(ctx, code, "")
}

func (s *Service) claim(ctx context.Context, code, expectedOrder string) (string, error) {
	return s.claimMode(ctx, code, expectedOrder, false)
}
func (s *Service) claimMode(ctx context.Context, code, expectedOrder string, automatic bool) (string, error) {
	if len(code) != 4 {
		return "", ErrUnavailable
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return "", ErrUnavailable
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE kiosk_control SET paused=paused WHERE singleton=1"); err != nil {
		return "", err
	}
	var otpEnabled bool
	if err = tx.QueryRowContext(ctx, "SELECT otp_enabled FROM kiosk_control WHERE singleton=1").Scan(&otpEnabled); err != nil {
		return "", err
	}
	if automatic && otpEnabled {
		return "", ErrUnavailable
	}
	if automatic {
		var verified bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kiosk_release_authorizations v JOIN orders o ON o.id=v.order_id WHERE v.order_id=? AND NOT EXISTS(SELECT 1 FROM print_submissions j JOIN order_lines l ON l.id=j.line_id WHERE l.order_id=o.id) AND NOT EXISTS(SELECT 1 FROM separator_invoices si WHERE si.order_id=o.id AND si.state IN ('submitting','submitted')))`, expectedOrder).Scan(&verified); err != nil {
			return "", err
		}
		if !verified {
			return "", ErrUnavailable
		}
	}
	var order, preparation, digest string
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT p.order_id,p.preparation,p.prepared_digest,p.created_at FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id WHERE p.code_hash=? AND p.state='active' AND (p.expires_at>? OR ?) AND (?='' OR p.order_id=?) AND o.id IN (SELECT id FROM kiosk_eligible_orders) AND NOT EXISTS(SELECT 1 FROM remote_refunds f WHERE f.order_id=o.id)`, s.hash(code), s.now().Unix(), automatic, expectedOrder, expectedOrder).Scan(&order, &preparation, &digest, &created)
	if err == sql.ErrNoRows {
		return "", ErrUnavailable
	}
	if err != nil {
		return "", err
	}
	if expectedOrder != "" && expectedOrder != order {
		return "", ErrUnavailable
	}
	end, e := dayEnd(ctx, tx, time.Unix(created, 0))
	if e != nil {
		return "", e
	}
	if !automatic && end <= s.now().Unix() {
		return "", ErrUnavailable
	}
	if preparation != "ready" || digest == "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO kiosk_waiting_releases VALUES(?,?,?) ON CONFLICT(order_id) DO UPDATE SET code_hash=excluded.code_hash,pickup_created_at=excluded.pickup_created_at`, order, s.hash(code), created); err != nil {
			return "", err
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return order, ErrPreparing
	}
	result, err := tx.ExecContext(ctx, "UPDATE kiosk_pickups SET state='claimed' WHERE order_id=? AND state='active'", order)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrUnavailable
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO kiosk_releases(order_id,prepared_digest,claimed_at) VALUES(?,?,?)", order, digest, s.now().Unix())
	if err != nil {
		return "", err
	}
	if automatic {
		if _, err = tx.ExecContext(ctx, "UPDATE kiosk_releases SET release_mode='automatic' WHERE order_id=?", order); err != nil {
			return "", err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE orders SET print_requested=1 WHERE id=? AND id IN (SELECT id FROM kiosk_eligible_orders)", order)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM kiosk_waiting_releases WHERE order_id=?", order); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return order, nil
}
