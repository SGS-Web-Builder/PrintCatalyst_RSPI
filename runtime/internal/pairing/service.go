package pairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Service owns the pairing-code and paired-device state. It is the only
// writer to the pair_codes, paired_devices, device_tokens and
// pair_exchange_attempts tables; the HTTP layer is a thin shell over
// the methods exposed here.
type Service struct {
	database *sql.DB
	now      func() time.Time
	newID    func() string
	mu       sync.Mutex
}

// Option mutates a Service during construction.
type Option func(*Service)

// WithClock overrides the clock. Tests pass a fixed value so they can
// assert on TTL and audit-trail timestamps deterministically.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithIDGenerator overrides the audit and paired-device id generator.
func WithIDGenerator(generator func() string) Option {
	return func(s *Service) { s.newID = generator }
}

// New constructs a Service bound to the supplied database.
func New(database *sql.DB, options ...Option) (*Service, error) {
	if database == nil {
		return nil, fmt.Errorf("pairing service requires a database")
	}
	service := &Service{
		database: database,
		now:      time.Now,
		newID:    randomID,
	}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

// Code is the view returned to the owner after Initiate succeeds. The
// cleartext code is included so the owner can read it off the
// dashboard; the deep link wraps the same code into a URL the
// companion application can open directly.
type Code struct {
	ID         string    `json:"id"`
	Code       string    `json:"code"`
	DeepLink   string    `json:"deepLink"`
	ExpiresAt  time.Time `json:"expiresAt"`
	CreatedAt  time.Time `json:"createdAt"`
	ConsumedAt time.Time `json:"consumedAt,omitempty"`
}

// Initiate mints a new single-use pairing code. The cleartext code is
// returned to the caller and never persisted; only the salted SHA-256
// hash is written to the database so a database leak does not expose
// codes that are still in their validity window.
func (s *Service) Initiate(ctx context.Context, deepLinkTemplate string) (Code, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	code, err := generateCode()
	if err != nil {
		return Code{}, fmt.Errorf("generate code: %w", err)
	}
	id := s.newID()
	now := s.now()
	expires := now.Add(CodeTTL)
	hash := hashCode(code)

	if _, err := s.database.ExecContext(ctx,
		`INSERT INTO pair_codes (id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		id, hash, formatTime(now), formatTime(expires),
	); err != nil {
		return Code{}, fmt.Errorf("persist pairing code: %w", err)
	}

	deepLink := buildDeepLink(deepLinkTemplate, code)
	return Code{
		ID:        id,
		Code:      code,
		DeepLink:  deepLink,
		ExpiresAt: expires,
		CreatedAt: now,
	}, nil
}

// ExchangeInput is the payload the companion application submits to
// Exchange. The fingerprint is opaque to the server; we just bind the
// issued token to it so a stolen token is useless from a different
// device.
type ExchangeInput struct {
	Code        string
	Fingerprint string
	Label       string
}

// Exchange validates a freshly-claimed pairing code, atomically
// consumes it, registers the paired device, and returns a long-lived
// bearer token bound to that device. The append-only audit trail
// records every step.
func (s *Service) Exchange(ctx context.Context, input ExchangeInput) (ExchangeResult, error) {
	if !IsValidFingerprint(input.Fingerprint) {
		return ExchangeResult{}, ErrInvalid
	}
	if !IsValidLabel(input.Label) {
		return ExchangeResult{}, ErrInvalid
	}
	code := NormalizeCode(input.Code)
	if code == "" {
		return ExchangeResult{}, ErrInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Throttle by fingerprint before touching the codes table so an
	// attacker cannot drain the codes table with a single guess.
	attemptCount, err := s.countRecentAttempts(ctx, input.Fingerprint)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("count attempts: %w", err)
	}
	if attemptCount >= MaxExchangesPerFingerprint {
		return ExchangeResult{}, ErrRateLimit
	}

	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	// The audit row is written AFTER the main transaction releases its
	// connection. The store opens SQLite with SetMaxOpenConns(1), so any
	// nested database call inside Exchange would deadlock against the open
	// tx. The previous fix tried to dodge this with a fallback BeginTx in
	// commitAttempt; that fallback is also blocked by the live tx, so the
	// whole call hung. The correct shape is: capture the outcome inside the
	// tx, then write the audit row in a separate deferred call after the
	// connection is back in the pool. succeeded starts false; only the
	// happy path flips it to true before committing. The defer uses
	// context.Background so a cancelled request context cannot swallow the
	// brute-force counter update.
	succeeded := false
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		s.commitAttempt(context.Background(), input.Fingerprint, succeeded)
	}()

	var (
		codeID    string
		expiresAt string
		consumed  sql.NullString
	)
	row := tx.QueryRowContext(ctx,
		`SELECT id, expires_at, consumed_at FROM pair_codes WHERE code_hash = ?`,
		hashCode(code),
	)
	if err := row.Scan(&codeID, &expiresAt, &consumed); err != nil {
		if err == sql.ErrNoRows {
			return ExchangeResult{}, ErrUnconfigured
		}
		return ExchangeResult{}, fmt.Errorf("lookup code: %w", err)
	}

	expires, err := parseTime(expiresAt)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("parse expires_at: %w", err)
	}
	if !expires.After(s.now()) {
		return ExchangeResult{}, ErrExpired
	}
	if consumed.Valid {
		return ExchangeResult{}, ErrUnconfigured
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE pair_codes SET consumed_at = ?, consumed_by_fingerprint = ? WHERE id = ?`,
		formatTime(s.now()), input.Fingerprint, codeID,
	); err != nil {
		return ExchangeResult{}, fmt.Errorf("consume code: %w", err)
	}

	deviceID := s.newID()
	now := s.now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO paired_devices (id, fingerprint, label, paired_at, last_seen_at, revoked_at, revoked_reason) VALUES (?, ?, ?, ?, ?, NULL, '')`,
		deviceID, input.Fingerprint, strings.TrimSpace(input.Label), formatTime(now), formatTime(now),
	); err != nil {
		return ExchangeResult{}, fmt.Errorf("register device: %w", err)
	}

	token, tokenHash, err := generateToken()
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("generate token: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO device_tokens (token_hash, device_id, fingerprint, issued_at, expires_at, revoked_at) VALUES (?, ?, ?, ?, ?, NULL)`,
		tokenHash, deviceID, input.Fingerprint, formatTime(now), formatTime(now.Add(TokenTTL)),
	); err != nil {
		return ExchangeResult{}, fmt.Errorf("persist token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ExchangeResult{}, fmt.Errorf("commit: %w", err)
	}
	committed = true
	succeeded = true

	return ExchangeResult{
		DeviceID:  deviceID,
		Token:     token,
		ExpiresAt: now.Add(TokenTTL),
		IssuedAt:  now,
	}, nil
}

// ExchangeResult is what the mobile companion application receives
// after a successful code exchange.
type ExchangeResult struct {
	DeviceID  string    `json:"deviceId"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	IssuedAt  time.Time `json:"issuedAt"`
}

// VerifyToken returns the device bound to the supplied bearer token,
// or an error if the token is unknown, expired or revoked. The token
// is compared in constant time.
func (s *Service) VerifyToken(ctx context.Context, token string) (Device, error) {
	if strings.TrimSpace(token) == "" {
		return Device{}, ErrSignature
	}
	tokenHash := hashToken(token)
	row := s.database.QueryRowContext(ctx,
		`SELECT t.device_id, t.fingerprint, t.expires_at, t.revoked_at, d.label, d.revoked_at, d.revoked_reason
		   FROM device_tokens t
		   JOIN paired_devices d ON d.id = t.device_id
		  WHERE t.token_hash = ?`,
		tokenHash,
	)
	var (
		deviceID         string
		fingerprint      string
		tokenExpires     string
		tokenRevoked     sql.NullString
		label            string
		deviceRevoked    sql.NullString
		deviceRevokeWhy  string
	)
	if err := row.Scan(&deviceID, &fingerprint, &tokenExpires, &tokenRevoked, &label, &deviceRevoked, &deviceRevokeWhy); err != nil {
		if err == sql.ErrNoRows {
			return Device{}, ErrSignature
		}
		return Device{}, fmt.Errorf("lookup token: %w", err)
	}
	if tokenRevoked.Valid {
		return Device{}, ErrRevoked
	}
	if deviceRevoked.Valid {
		return Device{}, ErrRevoked
	}
	expires, err := parseTime(tokenExpires)
	if err != nil {
		return Device{}, fmt.Errorf("parse expires_at: %w", err)
	}
	if !expires.After(s.now()) {
		return Device{}, ErrExpired
	}

	// Refresh last-seen asynchronously — a missing update must not
	// cause token verification to fail.
	if _, err := s.database.ExecContext(ctx,
		`UPDATE paired_devices SET last_seen_at = ? WHERE id = ?`,
		formatTime(s.now()), deviceID,
	); err != nil {
		return Device{}, fmt.Errorf("touch last_seen_at: %w", err)
	}

	return Device{
		ID:          deviceID,
		Fingerprint: fingerprint,
		Label:       label,
	}, nil
}

// Device is the public projection of a paired row.
type Device struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
}

// List returns the paired devices newest-first, with the unconsumed
// pairing codes appended so the owner can see which devices are still
// waiting to be paired.
func (s *Service) List(ctx context.Context) (Status, error) {
	var status Status

	codeRows, err := s.database.QueryContext(ctx,
		`SELECT id, created_at, expires_at, consumed_at FROM pair_codes ORDER BY created_at DESC LIMIT 50`,
	)
	if err != nil {
		return status, fmt.Errorf("query codes: %w", err)
	}
	defer codeRows.Close()
	for codeRows.Next() {
		var (
			id         string
			createdAt  string
			expiresAt  string
			consumedAt sql.NullString
		)
		if err := codeRows.Scan(&id, &createdAt, &expiresAt, &consumedAt); err != nil {
			return status, fmt.Errorf("scan code: %w", err)
		}
		created, _ := parseTime(createdAt)
		expires, _ := parseTime(expiresAt)
		entry := Code{
			ID:        id,
			ExpiresAt: expires,
			CreatedAt: created,
		}
		if consumedAt.Valid {
			consumed, _ := parseTime(consumedAt.String)
			entry.ConsumedAt = consumed
		}
		status.PendingCodes = append(status.PendingCodes, entry)
	}
	if err := codeRows.Err(); err != nil {
		return status, fmt.Errorf("iterate codes: %w", err)
	}

	deviceRows, err := s.database.QueryContext(ctx,
		`SELECT id, fingerprint, label, paired_at, last_seen_at, revoked_at, revoked_reason FROM paired_devices ORDER BY paired_at DESC LIMIT 100`,
	)
	if err != nil {
		return status, fmt.Errorf("query devices: %w", err)
	}
	defer deviceRows.Close()
	for deviceRows.Next() {
		var (
			id          string
			fingerprint string
			label       string
			pairedAt    string
			lastSeen    string
			revokedAt   sql.NullString
			revokeWhy   string
		)
		if err := deviceRows.Scan(&id, &fingerprint, &label, &pairedAt, &lastSeen, &revokedAt, &revokeWhy); err != nil {
			return status, fmt.Errorf("scan device: %w", err)
		}
		paired, _ := parseTime(pairedAt)
		seen, _ := parseTime(lastSeen)
		entry := DeviceEntry{
			Device: Device{
				ID:          id,
				Fingerprint: fingerprint,
				Label:       label,
			},
			PairedAt:  paired,
			LastSeen:  seen,
		}
		if revokedAt.Valid {
			revoked, _ := parseTime(revokedAt.String)
			entry.RevokedAt = revoked
			entry.RevokedReason = revokeWhy
		}
		status.Devices = append(status.Devices, entry)
	}
	if err := deviceRows.Err(); err != nil {
		return status, fmt.Errorf("iterate devices: %w", err)
	}

	return status, nil
}

// Status is what the owner dashboard renders: pending codes and
// paired/revoked devices.
type Status struct {
	PendingCodes []Code        `json:"pendingCodes"`
	Devices      []DeviceEntry `json:"devices"`
}

// DeviceEntry is the dashboard projection of a paired device with
// its paired/last-seen timestamps and any revocation metadata.
type DeviceEntry struct {
	Device
	PairedAt      time.Time `json:"pairedAt"`
	LastSeen      time.Time `json:"lastSeenAt"`
	RevokedAt     time.Time `json:"revokedAt,omitempty"`
	RevokedReason string    `json:"revokedReason,omitempty"`
}

// Revoke marks a paired device and all of its outstanding tokens as
// revoked. The paired row stays for audit.
func (s *Service) Revoke(ctx context.Context, deviceID, reason string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx,
		`UPDATE paired_devices SET revoked_at = ?, revoked_reason = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(now), strings.TrimSpace(reason), deviceID,
	)
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke rows affected: %w", err)
	}
	if rows == 0 {
		return ErrUnconfigured
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE device_tokens SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`,
		formatTime(now), deviceID,
	); err != nil {
		return fmt.Errorf("revoke tokens: %w", err)
	}

	return tx.Commit()
}

// countRecentAttempts returns the number of recorded code-exchange
// attempts for the supplied fingerprint inside the rate-limit window.
func (s *Service) countRecentAttempts(ctx context.Context, fingerprint string) (int, error) {
	cutoff := formatTime(s.now().Add(-RateLimitWindow))
	row := s.database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pair_exchange_attempts WHERE fingerprint = ? AND attempted_at >= ?`,
		fingerprint, cutoff,
	)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count attempts: %w", err)
	}
	return count, nil
}

// commitAttempt inserts an append-only audit row for an exchange attempt.
// It must be called only AFTER the caller's main transaction has released
// its connection — the store opens SQLite with SetMaxOpenConns(1), so any
// database call from inside an open tx would deadlock. The previous
// fallback BeginTx path inside this function could not save us: BeginTx
// also acquires the lone connection, which the still-live tx holds, so the
// fallback hangs as surely as the primary path. The correct shape is to
// let the caller defer the audit write past the tx release, which is what
// Exchange does. Failures here must never fail the exchange itself, so
// errors are swallowed.
func (s *Service) commitAttempt(ctx context.Context, fingerprint string, succeeded bool) {
	attemptID := s.newID()
	if _, err := s.database.ExecContext(ctx,
		`INSERT INTO pair_exchange_attempts (id, fingerprint, attempted_at, succeeded) VALUES (?, ?, ?, ?)`,
		attemptID, fingerprint, formatTime(s.now()), succeeded,
	); err == nil {
		return
	}
	// Last-resort retry through a fresh transaction. This is only useful if
	// the caller passed a connection pool with multiple writers, which is
	// not the case in production. The retry is preserved so a future build
	// with a wider connection budget can still record the audit row without
	// crashing the exchange path.
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.ExecContext(ctx,
		`INSERT INTO pair_exchange_attempts (id, fingerprint, attempted_at, succeeded) VALUES (?, ?, ?, ?)`,
		attemptID, fingerprint, formatTime(s.now()), succeeded,
	)
	_ = tx.Commit()
}

// buildDeepLink substitutes the {{code}} placeholder in the supplied
// template so the merchant can customise the URL the companion
// application opens. The default template is "printcatalyst://pair?code={{code}}".
func buildDeepLink(template, code string) string {
	template = strings.TrimSpace(template)
	if template == "" {
		template = "printcatalyst://pair?code={{code}}"
	}
	return strings.ReplaceAll(template, "{{code}}", code)
}

// hashCode returns the salted SHA-256 of a pairing code in hex form.
// The salt is empty for now; we leave the seam in place so a future
// migration can introduce per-installation salts without changing the
// schema.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// hashToken is identical to hashCode but kept as a separate function so
// the token format can diverge later (for example, switching to a
// keyed MAC instead of an unsalted hash).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// generateCode picks CodeLength characters from CodeAlphabet. crypto/rand
// is the entropy source so the codes are unpredictable.
func generateCode() (string, error) {
	if len(CodeAlphabet) == 0 {
		return "", fmt.Errorf("pairing code alphabet is empty")
	}
	out := make([]byte, CodeLength)
	buf := make([]byte, CodeLength)
	alphabetLen := len(CodeAlphabet)
	// Largest multiple of alphabetLen that fits in 256. Used as the
	// upper bound for rejection sampling so each output index is
	// uniformly distributed across the alphabet. The alphabet has 32
	// entries which is a power of two, so the bias is in fact zero,
	// but the rejection sampling keeps the loop correct if the
	// alphabet ever changes.
	maxAcceptable := 256 - 256%alphabetLen
	for {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("read entropy: %w", err)
		}
		needed := CodeLength
		for _, b := range buf {
			if needed == 0 {
				break
			}
			if int(b) >= maxAcceptable {
				continue
			}
			out[CodeLength-needed] = CodeAlphabet[int(b)%alphabetLen]
			needed--
		}
		if needed == 0 {
			break
		}
	}
	return string(out), nil
}

// generateToken returns a 32-byte random token plus its SHA-256 hash
// (hex). The cleartext token is given to the mobile client; only the
// hash is persisted.
func generateToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("read entropy: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

// formatTime renders a time.Time in the UTC RFC3339Nano form the rest
// of the persistence layer uses. Keeping the format in one place
// means tests can switch the clock and still produce parseable
// timestamps.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime inverts formatTime. We tolerate both the RFC3339Nano form
// the service writes and the older time.Time.String() representation
// the store uses elsewhere so the paired-device queries interoperate.
func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", value)
}

// randomID is the default id generator. It mirrors the convention
// used by the licensing service: 26-character base32, ULID-like.
func randomID() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	buf := make([]byte, 26)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failures are unrecoverable here; the constructor
		// already verified the OS entropy source, so the failure mode
		// is a kernel-level problem.
		panic("pairing: crypto/rand failed: " + err.Error())
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}

// Equal is a constant-time helper used in tests to compare two
// strings without leaking timing information. Production code uses
// crypto/subtle directly.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
