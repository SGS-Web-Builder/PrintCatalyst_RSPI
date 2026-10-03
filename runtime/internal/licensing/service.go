package licensing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Service owns the device key pair, the activated licence, and the
// append-only audit trail. The same Service instance is reused by the
// owner HTTP layer and the supervisor health check; the constructor
// is intentionally cheap so a future supervisor restart can rebuild it
// without losing the persisted state.
type Service struct {
	database        *sql.DB
	controlPlane    ControlPlane
	now             func() time.Time
	newID           func() string
	encryptionSeed  func() []byte
	mu              sync.Mutex
	lastVerifiedAt  time.Time
	offlineGraceEnd time.Time
}

// Option mutates a Service during construction.
type Option func(*Service)

// WithControlPlane replaces the local stub control plane with the
// supplied implementation. The local stub is the default so the
// service works in a fresh checkout without any external dependency.
func WithControlPlane(controlPlane ControlPlane) Option {
	return func(s *Service) { s.controlPlane = controlPlane }
}

// WithEncryptionSeed overrides the encryption seed. Tests pass a
// deterministic value so encrypted private keys can be inspected.
func WithEncryptionSeed(seed func() []byte) Option {
	return func(s *Service) { s.encryptionSeed = seed }
}

// WithClock overrides the clock. Tests pass a fixed value so they can
// assert on issued-at and support-until timestamps deterministically.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now; s.lastVerifiedAt = now() }
}

// WithIDGenerator overrides the audit ID generator.
func WithIDGenerator(generator func() string) Option {
	return func(s *Service) { s.newID = generator }
}

// New constructs a Service bound to the supplied database. The
// encryption seed defaults to SeedFromEnvironment, which mixes the
// data directory path, the host name and the machine identifier.
func New(database *sql.DB, dataDirectory string, options ...Option) (*Service, error) {
	if database == nil {
		return nil, fmt.Errorf("licensing service requires a database")
	}
	if strings.TrimSpace(dataDirectory) == "" {
		return nil, fmt.Errorf("licensing service requires a data directory")
	}
	service := &Service{
		database: database,
		now:      time.Now,
		newID:    randomID,
		encryptionSeed: func() []byte {
			return SeedFromEnvironment(filepath.Clean(dataDirectory))
		},
	}
	for _, option := range options {
		option(service)
	}
	if service.controlPlane == nil {
		if ControlPlanePrivateKeyHex == "" {
			service.controlPlane = disabledControlPlane{}
			return service, nil
		}
		if ControlPlanePrivateKeyHex == "" {
			service.controlPlane = disabledControlPlane{}
			return service, nil
		}
		stub, err := NewLocalControlPlane()
		if err != nil {
			return nil, fmt.Errorf("init local control plane: %w", err)
		}
		service.controlPlane = stub
	}
	return service, nil
}

// EnsureKeyPair guarantees an installation key pair exists in the
// database. On first run it generates, encrypts and persists a fresh
// key pair; on subsequent runs it returns the existing one. The
// returned public key is the same key the control plane will sign
// entitlements for.
func (s *Service) EnsureKeyPair(ctx context.Context) (ed25519.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	publicKey, _, err := s.loadKeyPair(ctx)
	if err == nil {
		return publicKey, nil
	}
	if !errors.Is(err, ErrUnconfigured) {
		return nil, err
	}
	pair, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	if err := s.persistKeyPair(ctx, pair); err != nil {
		return nil, err
	}
	if err := s.appendEvent(ctx, "", EventKeyGenerated, "device key generated", ""); err != nil {
		return nil, err
	}
	return pair.PublicKey, nil
}

// PublicKey returns the device public key. The boolean reports whether
// the key pair has been generated yet so the dashboard can decide
// between rendering the activation form and the licence summary.
//
// Ensure is a convenience wrapper that generates the key pair on
// first use. The HTTP layer calls Ensure before every read so the
// dashboard never sees a transient "unconfigured" state for an
// installation that has not yet activated.
func (s *Service) Ensure(ctx context.Context) (ed25519.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	publicKey, _, err := s.loadKeyPair(ctx)
	if err == nil {
		return publicKey, nil
	}
	if !errors.Is(err, ErrUnconfigured) {
		return nil, err
	}
	pair, generateErr := GenerateKeyPair()
	if generateErr != nil {
		return nil, generateErr
	}
	if persistErr := s.persistKeyPair(ctx, pair); persistErr != nil {
		return nil, persistErr
	}
	if appendErr := s.appendEvent(ctx, "", EventKeyGenerated, "device key generated on first read", ""); appendErr != nil {
		return nil, appendErr
	}
	return pair.PublicKey, nil
}

// PublicKey returns the device public key. The boolean reports whether
// the key pair has been generated yet so the dashboard can decide
// between rendering the activation form and the licence summary.
func (s *Service) PublicKey(ctx context.Context) (ed25519.PublicKey, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	publicKey, _, err := s.loadKeyPair(ctx)
	if err != nil {
		if errors.Is(err, ErrUnconfigured) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return publicKey, true, nil
}

// Activate asks the control plane to issue a signed entitlement for
// the current device key and persists it as the current licence. The
// previous current licence is flipped to non-current inside the same
// transaction so a crash mid-activation cannot leave two current
// rows.
//
// On first activation the device key pair does not exist yet; the
// service generates, encrypts and persists it before the control
// plane request so the first run is a single round trip.
func (s *Service) Activate(ctx context.Context) (License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	publicKey, _, err := s.loadKeyPair(ctx)
	if err != nil {
		if !errors.Is(err, ErrUnconfigured) {
			return License{}, err
		}
		pair, generateErr := GenerateKeyPair()
		if generateErr != nil {
			return License{}, generateErr
		}
		if persistErr := s.persistKeyPair(ctx, pair); persistErr != nil {
			return License{}, persistErr
		}
		if appendErr := s.appendEvent(ctx, "", EventKeyGenerated, "device key generated before first activation", ""); appendErr != nil {
			return License{}, appendErr
		}
		publicKey = pair.PublicKey
	}
	request := IssueRequest{
		InstallationID:    InstallationID(publicKey),
		DeviceFingerprint: Fingerprint(publicKey),
		PublicKey:         publicKey,
		RequestedAt:       s.now().UTC(),
		Edition:           EditionPerpetual,
		Entitlements:      []Entitlement{EntitlementPrintOrders, EntitlementIDCards, EntitlementPassport, EntitlementTunnel, EntitlementManualPay, EntitlementGateway},
		SupportUntil:      s.now().UTC().Add(365 * 24 * time.Hour),
	}
	envelope, err := s.controlPlane.IssueLicense(request)
	if err != nil {
		_ = s.appendEvent(ctx, "", EventActivationFail, err.Error(), "")
		return License{}, fmt.Errorf("issue licence: %w", err)
	}
	payload, err := VerifySignature(envelope, publicKey)
	if err != nil {
		_ = s.appendEvent(ctx, "", EventActivationFail, err.Error(), PayloadHash(envelope.Payload))
		return License{}, err
	}
	license, err := s.persistLicense(ctx, envelope, payload)
	if err != nil {
		return License{}, err
	}
	s.lastVerifiedAt = s.now().UTC()
	s.offlineGraceEnd = s.now().UTC().Add(OfflineGrace)
	if err := s.appendEvent(ctx, license.ID, EventActivated, "licence activated", PayloadHash(envelope.Payload)); err != nil {
		return license, err
	}
	if err := s.appendEvent(ctx, license.ID, EventVerified, "signature verified", PayloadHash(envelope.Payload)); err != nil {
		return license, err
	}
	return license, nil
}

// Refresh re-verifies the current licence. The signature is checked
// again and the offline-grace clock is rolled forward; the dashboard
// surfaces the timestamp of the last successful refresh.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	license, envelope, err := s.loadCurrentLicense(ctx)
	if err != nil {
		return err
	}
	publicKey, _, err := s.loadKeyPair(ctx)
	if err != nil {
		return err
	}
	if _, err := VerifySignature(envelope, publicKey); err != nil {
		_ = s.appendEvent(ctx, license.ID, EventVerifyFail, err.Error(), PayloadHash(envelope.Payload))
		return err
	}
	s.lastVerifiedAt = s.now().UTC()
	s.offlineGraceEnd = s.now().UTC().Add(OfflineGrace)
	return s.appendEvent(ctx, license.ID, EventVerified, "refresh succeeded", PayloadHash(envelope.Payload))
}

// Status returns the projection the dashboard renders. The boolean
// fields reflect whether the licence is unconfigured, revoked, in
// offline grace or showing clock rollback.
func (s *Service) Status(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	publicKey, _, err := s.loadKeyPair(ctx)
	if err != nil {
		if errors.Is(err, ErrUnconfigured) {
			return Status{Unconfigured: true}, nil
		}
		return Status{}, err
	}
	status := Status{
		InstallationID: InstallationID(publicKey),
		PublicKey:      hex.EncodeToString(publicKey),
		Fingerprint:    Fingerprint(publicKey),
		VerifiedAt:     timePtr(s.lastVerifiedAt),
	}
	license, envelope, err := s.loadCurrentLicense(ctx)
	if err != nil {
		if errors.Is(err, ErrUnconfigured) {
			status.Unconfigured = true
			return status, nil
		}
		return status, err
	}
	if _, err := VerifySignature(envelope, publicKey); err != nil {
		// A current licence that does not match the current device
		// key indicates a Transfer happened since activation. Clear
		// the current flag inside the same transaction so the next
		// Status call returns Unconfigured instead of a stale row.
		if errors.Is(err, ErrMismatch) {
			if _, clearErr := s.database.ExecContext(ctx, `UPDATE licenses SET is_current = 0 WHERE id = ?`, license.ID); clearErr != nil {
				return status, fmt.Errorf("clear stale licence: %w", clearErr)
			}
			_ = s.appendEvent(ctx, license.ID, EventTransferred, "stale licence cleared after device transfer", "")
			status.Unconfigured = true
			return status, nil
		}
		_ = s.appendEvent(ctx, license.ID, EventVerifyFail, err.Error(), PayloadHash(envelope.Payload))
		return status, err
	}
	current := license
	status.Current = &current
	status.Entitlements = current.Entitlements
	status.SupportUntil = timePtr(current.SupportUntil)
	if current.Revoked {
		status.Revoked = true
		status.RevokedReason = current.RevokedReason
	}
	graceEnd := s.offlineGraceEnd
	if graceEnd.IsZero() {
		graceEnd = current.ReceivedAt.Add(OfflineGrace)
	}
	if graceEnd.After(s.now().UTC()) {
		status.OfflineGrace = true
		remaining := graceEnd.Sub(s.now().UTC())
		status.GraceRemaining = remaining.Truncate(time.Minute).String()
	}
	if current.ReceivedAt.After(s.now().UTC().Add(ClockSkewTolerance)) {
		status.ClockRollback = true
	}
	return status, nil
}

// Revoke marks the current licence as revoked. The dashboard surfaces
// the reason and the audit log records the transition. Revocation is
// non-destructive: the row remains for audit purposes.
func (s *Service) Revoke(ctx context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrInvalid
	}
	license, _, err := s.loadCurrentLicense(ctx)
	if err != nil {
		return err
	}
	if _, err := s.database.ExecContext(ctx, `
UPDATE licenses SET revoked_at = ?, revoked_reason = ? WHERE id = ?`,
		s.now().UTC().Format(time.RFC3339Nano), reason, license.ID); err != nil {
		return fmt.Errorf("revoke licence: %w", err)
	}
	license.Revoked = true
	return s.appendEvent(ctx, license.ID, EventRevoked, reason, "")
}

// Transfer regenerates the device key pair. The previous key pair and
// any licence signed against it are invalidated; the activation form
// re-opens so the merchant can request a fresh entitlement for the
// new device.
func (s *Service) Transfer(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.loadKeyPair(ctx); err != nil {
		if errors.Is(err, ErrUnconfigured) {
			return ErrUnconfigured
		}
		return err
	}
	pair, err := GenerateKeyPair()
	if err != nil {
		return err
	}
	if err := s.persistKeyPair(ctx, pair); err != nil {
		return err
	}
	if _, err := s.database.ExecContext(ctx, `
UPDATE licenses SET revoked_at = ?, revoked_reason = ? WHERE is_current = 1 AND revoked_at IS NULL`,
		s.now().UTC().Format(time.RFC3339Nano), "device transfer"); err != nil {
		return fmt.Errorf("invalidate previous licence: %w", err)
	}
	if err := s.appendEvent(ctx, "", EventKeyRotated, "device key rotated", ""); err != nil {
		return err
	}
	return s.appendEvent(ctx, "", EventTransferred, "device transfer recorded; re-activation required", "")
}

// Events returns the most recent audit entries newest-first. The
// dashboard uses this to render the transition list.
func (s *Service) Events(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.database.QueryContext(ctx, `
SELECT id, COALESCE(license_id, ''), event_type, detail, COALESCE(payload_hash, ''), occurred_at
FROM license_events ORDER BY occurred_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read licence events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var occurredAt string
		if err := rows.Scan(&event.ID, &event.LicenseID, &event.EventType, &event.Detail, &event.PayloadHash, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan licence event: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, fmt.Errorf("parse licence event timestamp: %w", err)
		}
		event.OccurredAt = parsed
		events = append(events, event)
	}
	if events == nil {
		events = []Event{}
	}
	return events, rows.Err()
}

// VerifyOnStart is invoked at runtime start to re-check the signature
// before any commercial endpoint runs. The boolean reports whether
// the licence is currently acceptable; the error reports the
// verification failure so the supervisor can decide between hard
// failure and offline grace.
//
// The audit log records a verification failure but does not panic; the
// caller decides whether to start the service at all.
func (s *Service) VerifyOnStart(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	publicKey, _, err := s.loadKeyPair(ctx)
	if err != nil {
		if errors.Is(err, ErrUnconfigured) {
			return false, nil
		}
		return false, err
	}
	license, envelope, err := s.loadCurrentLicense(ctx)
	if err != nil {
		if errors.Is(err, ErrUnconfigured) {
			return false, nil
		}
		return false, err
	}
	if _, err := VerifySignature(envelope, publicKey); err != nil {
		_ = s.appendEvent(ctx, license.ID, EventVerifyFail, err.Error(), PayloadHash(envelope.Payload))
		return false, nil
	}
	if license.Revoked {
		return false, nil
	}
	if license.SupportUntil.Before(s.now().UTC()) {
		return false, nil
	}
	s.lastVerifiedAt = s.now().UTC()
	s.offlineGraceEnd = s.now().UTC().Add(OfflineGrace)
	return true, nil
}

// loadKeyPair reads and decrypts the persisted device key pair.
func (s *Service) loadKeyPair(ctx context.Context) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	var installationID, publicKeyHex string
	var ciphertext, nonce []byte
	var createdAt string
	row := s.database.QueryRowContext(ctx, `
SELECT installation_id, public_key, encrypted_private_key, encryption_nonce, created_at
FROM installation_keys WHERE singleton = 1`)
	if err := row.Scan(&installationID, &publicKeyHex, &ciphertext, &nonce, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrUnconfigured
		}
		return nil, nil, fmt.Errorf("read installation key: %w", err)
	}
	publicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, nil, ErrInvalid
	}
	seed := s.encryptionSeed()
	if len(seed) == 0 {
		return nil, nil, fmt.Errorf("encryption seed is empty")
	}
	key := DeriveEncryptionKey(seed)
	privateKey, err := DecryptPrivateKey(ciphertext, nonce, key)
	if err != nil {
		return nil, nil, err
	}
	if installationID != InstallationID(ed25519.PublicKey(publicKey)) {
		return nil, nil, ErrInvalid
	}
	return ed25519.PublicKey(publicKey), privateKey, nil
}

// persistKeyPair writes the device key pair to the singleton row.
func (s *Service) persistKeyPair(ctx context.Context, pair KeyPair) error {
	publicKeyHex := hex.EncodeToString(pair.PublicKey)
	seed := s.encryptionSeed()
	if len(seed) == 0 {
		return fmt.Errorf("encryption seed is empty")
	}
	key := DeriveEncryptionKey(seed)
	ciphertext, nonce, err := EncryptPrivateKey(pair.PrivateKey, key)
	if err != nil {
		return err
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.database.ExecContext(ctx, `
INSERT INTO installation_keys (singleton, installation_id, public_key, encrypted_private_key, encryption_nonce, created_at, rotated_at)
VALUES (1, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(singleton) DO UPDATE SET
    installation_id = excluded.installation_id,
    public_key = excluded.public_key,
    encrypted_private_key = excluded.encrypted_private_key,
    encryption_nonce = excluded.encryption_nonce,
    rotated_at = ?`,
		InstallationID(pair.PublicKey), publicKeyHex, ciphertext, nonce, timestamp, timestamp)
	if err != nil {
		return fmt.Errorf("persist installation key: %w", err)
	}
	return nil
}

// loadCurrentLicense returns the current licence envelope.
func (s *Service) loadCurrentLicense(ctx context.Context) (License, SignedLicense, error) {
	var license License
	var issuedAt, supportUntil, receivedAt string
	var entitlementsJSON string
	var payload, signature []byte
	var revokedAt sql.NullString
	var revokedReason sql.NullString
	row := s.database.QueryRowContext(ctx, `
SELECT id, installation_id, product, edition, device_fingerprint, issued_at, support_until,
       entitlements_json, nonce, payload, signature, is_current, received_at, revoked_at, revoked_reason
FROM licenses WHERE is_current = 1 LIMIT 1`)
	if err := row.Scan(&license.ID, &license.InstallationID, &license.Product, &license.Edition,
		&license.DeviceFingerprint, &issuedAt, &supportUntil, &entitlementsJSON, &license.Nonce,
		&payload, &signature, &license.IsCurrent, &receivedAt, &revokedAt, &revokedReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return license, SignedLicense{}, ErrUnconfigured
		}
		return license, SignedLicense{}, fmt.Errorf("read current licence: %w", err)
	}
	parsedIssued, err := time.Parse(time.RFC3339Nano, issuedAt)
	if err != nil {
		return license, SignedLicense{}, fmt.Errorf("parse issued_at: %w", err)
	}
	parsedSupport, err := time.Parse(time.RFC3339Nano, supportUntil)
	if err != nil {
		return license, SignedLicense{}, fmt.Errorf("parse support_until: %w", err)
	}
	parsedReceived, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		return license, SignedLicense{}, fmt.Errorf("parse received_at: %w", err)
	}
	if err := json.Unmarshal([]byte(entitlementsJSON), &license.Entitlements); err != nil {
		return license, SignedLicense{}, fmt.Errorf("decode entitlements: %w", err)
	}
	license.IssuedAt = parsedIssued
	license.SupportUntil = parsedSupport
	license.ReceivedAt = parsedReceived
	if revokedAt.Valid {
		license.Revoked = true
	}
	if revokedReason.Valid {
		license.RevokedReason = revokedReason.String
	}
	return license, SignedLicense{Payload: payload, Signature: signature}, nil
}

// persistLicense writes the envelope as the new current licence.
func (s *Service) persistLicense(ctx context.Context, envelope SignedLicense, payload signedPayload) (License, error) {
	licenseID := s.newID()
	entitlementsJSON, err := json.Marshal(payload.Entitlements)
	if err != nil {
		return License{}, fmt.Errorf("encode entitlements: %w", err)
	}
	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	transaction, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return License{}, fmt.Errorf("begin licence transaction: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `UPDATE licenses SET is_current = 0 WHERE is_current = 1`); err != nil {
		return License{}, fmt.Errorf("clear previous current licence: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO licenses (id, installation_id, product, edition, device_fingerprint,
    issued_at, support_until, entitlements_json, nonce, payload, signature,
    is_current, received_at, revoked_at, revoked_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, NULL, NULL)`,
		licenseID, payload.InstallationID, payload.Product, payload.Edition, payload.DeviceFingerprint,
		payload.IssuedAt.UTC().Format(time.RFC3339Nano), payload.SupportUntil.UTC().Format(time.RFC3339Nano),
		string(entitlementsJSON), payload.Nonce, envelope.Payload, envelope.Signature,
		timestamp); err != nil {
		return License{}, fmt.Errorf("insert current licence: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return License{}, fmt.Errorf("commit licence: %w", err)
	}
	return License{
		ID:                licenseID,
		InstallationID:    payload.InstallationID,
		Product:           payload.Product,
		Edition:           payload.Edition,
		DeviceFingerprint: payload.DeviceFingerprint,
		IssuedAt:          payload.IssuedAt.UTC(),
		SupportUntil:      payload.SupportUntil.UTC(),
		Entitlements:      payload.Entitlements,
		Nonce:             payload.Nonce,
		IsCurrent:         true,
		ReceivedAt:        s.now().UTC(),
	}, nil
}

// appendEvent records a row in the audit log.
func (s *Service) appendEvent(ctx context.Context, licenseID, eventType, detail, payloadHash string) error {
	var hash sql.NullString
	if payloadHash != "" {
		hash = sql.NullString{String: payloadHash, Valid: true}
	}
	var licenseRef sql.NullString
	if licenseID != "" {
		licenseRef = sql.NullString{String: licenseID, Valid: true}
	}
	_, err := s.database.ExecContext(ctx, `
INSERT INTO license_events (id, license_id, event_type, detail, payload_hash, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		s.newID(), licenseRef, eventType, detail, hash, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record licence event: %w", err)
	}
	return nil
}

// timePtr returns a pointer to the supplied time. Helper for building
// the read model with optional timestamp fields.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// randomID returns a 32-character hex identifier.
func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("secure random ID generation failed: %v", err))
	}
	return hex.EncodeToString(buffer)
}
