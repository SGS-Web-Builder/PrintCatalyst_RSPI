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
	return &Service{db: db, aead: aead, hashKey: append([]byte(nil), lookupKey...), now: time.Now}, nil
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
// The caller must have verified gateway signature, amount, currency and identity.
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
		return "", ErrUnavailable
	}
	var raw []byte
	var state string
	var expiry int64
	err = tx.QueryRowContext(ctx, "SELECT code_cipher,state,expires_at FROM kiosk_pickups WHERE order_id=?", order).Scan(&raw, &state, &expiry)
	if err == nil {
		if state != "active" || expiry <= s.now().Unix() {
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
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM kiosk_pickups WHERE state='active'").Scan(&active); err != nil {
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
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM kiosk_pickups WHERE state='active' AND code_hash=?)", hash).Scan(&used); err != nil {
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
		_, err = tx.ExecContext(ctx, `INSERT INTO kiosk_pickups(order_id,payment_reference,code_hash,code_cipher,state,expires_at,created_at) VALUES(?,?,?,?,'active',?,?)`, order, reference, hash, sealed, now+86400, now)
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
	var order, preparation, digest string
	err = tx.QueryRowContext(ctx, `SELECT p.order_id,p.preparation,p.prepared_digest FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id WHERE p.code_hash=? AND p.state='active' AND p.expires_at>? AND o.status='paid'`, s.hash(code), s.now().Unix()).Scan(&order, &preparation, &digest)
	if err == sql.ErrNoRows {
		return "", ErrUnavailable
	}
	if err != nil {
		return "", err
	}
	if preparation != "ready" || digest == "" {
		return "", ErrPreparing
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
	_, err = tx.ExecContext(ctx, "UPDATE orders SET print_requested=1 WHERE id=? AND status='paid'", order)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return order, nil
}
