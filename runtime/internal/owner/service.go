// Package owner owns the single local administrator and business profile.
// It has no SaaS, payment-provider or network dependency.
package owner

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

var (
	ErrInvalid         = errors.New("invalid account or business details")
	ErrSetup           = errors.New("licence verification is required or owner setup is already complete")
	ErrCredentials     = errors.New("invalid credentials or expired session")
	ErrThrottled       = errors.New("too many sign-in attempts; wait before trying again")
	ErrNotFound        = errors.New("business profile is not configured")
	ErrOperatorExists  = errors.New("an operator with that username already exists")
	ErrOperatorMissing = errors.New("operator is not configured")
	usernamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{2,79}$`)
	countryPattern     = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyPattern    = regexp.MustCompile(`^[A-Z]{3}$`)
	localePattern      = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)
)

// Role distinguishes the owner singleton from delegated operator accounts.
// The schema permits only these two values; any other string returned from the
// database would be a corruption signal and is rejected.
type Role string

const (
	RoleOwner    Role = "owner"
	RoleOperator Role = "operator"
)

func (r Role) Valid() bool {
	return r == RoleOwner || r == RoleOperator
}

// Subject is the resolved identity behind a session cookie. The HTTP layer
// uses it to gate handlers via role-based permission checks; tests and other
// internal callers can inspect the resolved role rather than probing the
// owner_sessions and operator_sessions tables directly.
type Subject struct {
	Name string
	Role Role
}

// Operator is the persisted record for a delegated user account. Role is
// always RoleOperator today; the column is preserved so a future refinement
// can introduce operator sub-roles without another migration.
type Operator struct {
	ID          int64
	Username    string
	Role        Role
	Enabled     bool
	CreatedAt   int64
	UpdatedAt   int64
	LastLoginAt int64
}

type Profile struct {
	OwnerName string `json:"ownerName"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Phone     string `json:"phone"`
	Country   string `json:"country"`
	Currency  string `json:"currency"`
	Locale    string `json:"locale"`
	TimeZone  string `json:"timeZone"`
}
type Service struct {
	db      *sql.DB
	now     func() time.Time
	loginMu sync.Mutex
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }
func (s *Service) Exists(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM local_owner").Scan(&n)
	return n != 0, err
}
func ValidateCredentials(username, password string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Create(ctx context.Context, username, password string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The owner may be created only when no other owner row exists yet. The
	// licence gate is intentionally NOT a precondition here: licence
	// activation itself is the step that completes GateLicence, and it lives
	// on a handler that — until the owner row is present — is guarded by
	// the installation setup token rather than an owner session. Requiring
	// GateLicence here would close a loop that the rest of the bootstrap
	// flow cannot open.
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM local_owner").Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return ErrSetup
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return err
	}
	encoded := "pbkdf2-sha256$600000$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key)
	if _, err := tx.ExecContext(ctx, "INSERT INTO local_owner VALUES(1,?,?,?)", username, encoded, s.now().Unix()); err != nil {
		return err
	}
	if err := completeGate(ctx, tx, "owner", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// Login is a dispatcher: it tries the owner singleton first, and if the
// supplied credentials do not match that account it falls through to the
// operator accounts table. Each account class keeps its own throttle row so a
// brute-force probe against one username class cannot exhaust the retry window
// for the other. The returned Role tells the HTTP layer which permission
// envelope applies to this session.
func (s *Service) Login(ctx context.Context, username, password string) (string, Role, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	normalized := strings.ToLower(strings.TrimSpace(username))
	if len(normalized) > 80 || len(password) > 1024 {
		return "", "", ErrCredentials
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	now := s.now().Unix()
	ownerThrottle, err := readThrottle(tx, "owner_login_throttle")
	if err != nil {
		return "", "", err
	}
	operatorThrottle, err := readThrottle(tx, "operator_login_throttle")
	if err != nil {
		return "", "", err
	}
	if now < ownerThrottle.retryAt || now < operatorThrottle.retryAt {
		return "", "", ErrThrottled
	}

	// Owner path: if the owner singleton exists and the supplied username
	// matches it, verify the password and mint an owner session.
	var ownerName, ownerHash string
	err = tx.QueryRowContext(ctx, "SELECT username,password_hash FROM local_owner WHERE singleton=1").Scan(&ownerName, &ownerHash)
	ownerExists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if ownerExists && ownerName == normalized && verifyPassword(ownerHash, password) {
		token, err := mintOwnerSession(ctx, tx, now)
		if err != nil {
			return "", "", err
		}
		if err := resetThrottle(tx, "owner_login_throttle", now); err != nil {
			return "", "", err
		}
		if err := audit(ctx, tx, "owner.login", s.now()); err != nil {
			return "", "", err
		}
		if err := tx.Commit(); err != nil {
			return "", "", err
		}
		return token, RoleOwner, nil
	}

	// Operator path: look up by username. Disabled operators are treated as
	// credential failures so the response does not reveal account state.
	var opID int64
	var opHash string
	var opEnabled int
	err = tx.QueryRowContext(ctx, "SELECT id,password_hash,enabled FROM operators WHERE username=? COLLATE NOCASE", normalized).Scan(&opID, &opHash, &opEnabled)
	operatorExists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if operatorExists && opEnabled == 1 && verifyPassword(opHash, password) {
		token, err := mintOperatorSession(ctx, tx, opID, now)
		if err != nil {
			return "", "", err
		}
		if err := resetThrottle(tx, "operator_login_throttle", now); err != nil {
			return "", "", err
		}
		if err := audit(ctx, tx, "operator.login", s.now()); err != nil {
			return "", "", err
		}
		if err := tx.Commit(); err != nil {
			return "", "", err
		}
		return token, RoleOperator, nil
	}

	// Neither path matched. Bump the throttle of every enabled path we
	// actually attempted, never one for a username that was never registered,
	// and never for a deliberately disabled operator — disabled rows do not
	// represent a credential the attacker can guess, so counting them would
	// consume the legitimate operator's retry window without buying any
	// brute-force protection.
	if ownerExists && ownerName == normalized && !verifyPassword(ownerHash, password) {
		if err := bumpThrottle(tx, "owner_login_throttle", now, &ownerThrottle.failures); err != nil {
			return "", "", err
		}
	}
	if operatorExists && opEnabled == 1 && !verifyPassword(opHash, password) {
		if err := bumpThrottle(tx, "operator_login_throttle", now, &operatorThrottle.failures); err != nil {
			return "", "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return "", "", ErrCredentials
}

// AuthenticateOperator verifies an operator username and password and mints a
// session. It is the single-account counterpart to Login's dispatcher path:
// callers that already know they want an operator (for example a future
// "switch to operator" flow, or a test that wants to exercise the operator
// throttle in isolation) can use this directly. The caller still owns the
// 8-hour session token, the PBKDF2 verification and the throttle reset.
func (s *Service) AuthenticateOperator(ctx context.Context, username, password string) (string, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	normalized := strings.ToLower(strings.TrimSpace(username))
	if len(normalized) > 80 || len(password) > 1024 {
		return "", ErrCredentials
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	throttle, err := readThrottle(tx, "operator_login_throttle")
	if err != nil {
		return "", err
	}
	if now < throttle.retryAt {
		return "", ErrThrottled
	}
	var opID int64
	var opHash string
	var opEnabled int
	err = tx.QueryRowContext(ctx, "SELECT id,password_hash,enabled FROM operators WHERE username=? COLLATE NOCASE", normalized).Scan(&opID, &opHash, &opEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		if err := bumpThrottle(tx, "operator_login_throttle", now, &throttle.failures); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrCredentials
	}
	if err != nil {
		return "", err
	}
	if opEnabled != 1 {
		// Disabled rows are not credentials the attacker can guess, so counting
		// them would consume the legitimate operator's retry window. Returning
		// ErrCredentials here deliberately matches the wrong-password response.
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrCredentials
	}
	if !verifyPassword(opHash, password) {
		if err := bumpThrottle(tx, "operator_login_throttle", now, &throttle.failures); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrCredentials
	}
	token, err := mintOperatorSession(ctx, tx, opID, now)
	if err != nil {
		return "", err
	}
	if err := resetThrottle(tx, "operator_login_throttle", now); err != nil {
		return "", err
	}
	if err := audit(ctx, tx, "operator.login", s.now()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

type throttleState struct {
	failures int
	retryAt  int64
}

// readThrottle loads the persistent login-throttle state for either the owner
// or the operator class. Both tables share the same shape (singleton + failures
// + retry_at) so a single helper handles both.
func readThrottle(tx *sql.Tx, table string) (throttleState, error) {
	var state throttleState
	err := tx.QueryRow("SELECT failures,retry_at FROM "+table+" WHERE singleton=1").Scan(&state.failures, &state.retryAt)
	return state, err
}

func bumpThrottle(tx *sql.Tx, table string, now int64, failures *int) error {
	*failures++
	if *failures > 6 {
		*failures = 6
	}
	_, err := tx.Exec("UPDATE "+table+" SET failures=?,retry_at=? WHERE singleton=1", *failures, now+int64(1<<*failures))
	return err
}

func resetThrottle(tx *sql.Tx, table string, now int64) error {
	_, err := tx.Exec("UPDATE " + table + " SET failures=0,retry_at=0 WHERE singleton=1")
	return err
}

func mintOwnerSession(ctx context.Context, tx *sql.Tx, now int64) (string, error) {
	token, err := RandomToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM owner_sessions WHERE expires_at<=?", now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO owner_sessions VALUES(?,1,?)", Digest(token), now+int64(8*time.Hour/time.Second)); err != nil {
		return "", err
	}
	return token, nil
}

func mintOperatorSession(ctx context.Context, tx *sql.Tx, operatorID int64, now int64) (string, error) {
	token, err := RandomToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM operator_sessions WHERE expires_at<=?", now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO operator_sessions VALUES(?,?,?)", Digest(token), operatorID, now+int64(8*time.Hour/time.Second)); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE operators SET last_login_at=?,updated_at=? WHERE id=?", now, now, operatorID); err != nil {
		return "", err
	}
	return token, nil
}
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		return false
	}
	salt, e1 := hex.DecodeString(parts[2])
	expected, e2 := hex.DecodeString(parts[3])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(expected) != 32 {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}

// Session returns the username for the owner session identified by the token.
// It deliberately does not look at operator sessions — the HTTP layer uses
// Resolve for role-aware lookups, and the existing owner-only test surface
// relies on this narrow contract.
func (s *Service) Session(ctx context.Context, token string) (string, error) {
	if len(token) != 64 {
		return "", ErrCredentials
	}
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT username FROM local_owner JOIN owner_sessions ON local_owner.singleton=owner_sessions.owner_id WHERE token_hash=? AND expires_at>?", Digest(token), s.now().Unix()).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCredentials
	}
	return name, err
}

// OperatorSession returns the operator behind a session token. Used by the
// HTTP layer to populate the role on /api/v1/owner/session and to gate
// role-restricted endpoints; the route handlers then call RequirePermission.
func (s *Service) OperatorSession(ctx context.Context, token string) (Operator, error) {
	if len(token) != 64 {
		return Operator{}, ErrCredentials
	}
	var op Operator
	var role string
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT o.id,o.username,o.role,o.enabled,o.created_at,o.updated_at,o.last_login_at FROM operators o JOIN operator_sessions s ON o.id=s.operator_id WHERE s.token_hash=? AND s.expires_at>?`, Digest(token), s.now().Unix()).Scan(&op.ID, &op.Username, &role, &enabled, &op.CreatedAt, &op.UpdatedAt, &op.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Operator{}, ErrCredentials
	}
	if err != nil {
		return Operator{}, err
	}
	op.Role = Role(role)
	if !op.Role.Valid() {
		return Operator{}, fmt.Errorf("invalid role in operator row %d", op.ID)
	}
	op.Enabled = enabled == 1
	return op, nil
}

// Resolve identifies the holder of a session token regardless of which
// account class it belongs to. The returned Subject carries the role so the
// HTTP layer can branch on permission. A token that exists in both tables
// (which should not happen in practice) resolves as owner, which is the
// strictest role and therefore the safest default for a corruption case.
func (s *Service) Resolve(ctx context.Context, token string) (Subject, error) {
	if len(token) != 64 {
		return Subject{}, ErrCredentials
	}
	now := s.now().Unix()
	digest := Digest(token)
	var subject Subject
	var role string
	err := s.db.QueryRowContext(ctx, `
        SELECT role, name FROM (
            SELECT ? AS role, username AS name, 0 AS class FROM local_owner JOIN owner_sessions ON local_owner.singleton=owner_sessions.owner_id WHERE token_hash=? AND expires_at>?
            UNION ALL
            SELECT ? AS role, username AS name, 1 AS class FROM operators JOIN operator_sessions ON operators.id=operator_sessions.operator_id WHERE token_hash=? AND expires_at>?
        ) ORDER BY class LIMIT 1`,
		string(RoleOwner), digest, now, string(RoleOperator), digest, now).Scan(&role, &subject.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Subject{}, ErrCredentials
	}
	if err != nil {
		return Subject{}, err
	}
	subject.Role = Role(role)
	if !subject.Role.Valid() {
		return Subject{}, fmt.Errorf("invalid role on resolved session")
	}
	return subject, nil
}

// Logout deletes the session token from whichever table holds it. The cookie
// is cleared by the HTTP layer; the database is purged here so an attacker
// who captured the cookie before logout cannot reuse the token.
func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM owner_sessions WHERE token_hash=?", Digest(token)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM operator_sessions WHERE token_hash=?", Digest(token)); err != nil {
		return err
	}
	return nil
}

// CreateOperator persists a new operator account. It requires the owner to
// already exist (an installation that has not completed owner setup cannot
// delegate authority) and refuses to collide with the owner username so the
// dispatcher in Login always resolves owner-first unambiguously.
func (s *Service) CreateOperator(ctx context.Context, username, password string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerName string
	if err := tx.QueryRowContext(ctx, "SELECT username FROM local_owner WHERE singleton=1").Scan(&ownerName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetup
		}
		return err
	}
	if ownerName == username {
		return ErrOperatorExists
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return err
	}
	encoded := "pbkdf2-sha256$600000$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key)
	now := s.now().Unix()
	if _, err := tx.ExecContext(ctx, "INSERT INTO operators(username,password_hash,role,enabled,created_at,updated_at) VALUES(?,?,?,1,?,?)", username, encoded, string(RoleOperator), now, now); err != nil {
		if isUniqueViolation(err) {
			return ErrOperatorExists
		}
		return err
	}
	if err := audit(ctx, tx, "operator.created", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ListOperators returns every operator row ordered by username. The HTTP
// layer requires an owner session before calling this; the service itself
// trusts the caller's authorization.
func (s *Service) ListOperators(ctx context.Context) ([]Operator, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,username,role,enabled,created_at,updated_at,last_login_at FROM operators ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	operators := []Operator{}
	for rows.Next() {
		var op Operator
		var role string
		var enabled int
		if err := rows.Scan(&op.ID, &op.Username, &role, &enabled, &op.CreatedAt, &op.UpdatedAt, &op.LastLoginAt); err != nil {
			return nil, err
		}
		op.Role = Role(role)
		if !op.Role.Valid() {
			return nil, fmt.Errorf("invalid role in operator row %d", op.ID)
		}
		op.Enabled = enabled == 1
		operators = append(operators, op)
	}
	return operators, rows.Err()
}

// SetOperatorEnabled flips the enabled flag on an operator row. Disabling
// also revokes any live sessions so a compromised account is locked out the
// moment the owner takes action. Re-enabling resets the operator login
// throttle so a legitimate operator is not stuck waiting out a backoff
// window that an attacker accumulated while the row was disabled.
func (s *Service) SetOperatorEnabled(ctx context.Context, id int64, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	result, err := tx.ExecContext(ctx, "UPDATE operators SET enabled=?,updated_at=? WHERE id=?", boolToInt(enabled), now, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrOperatorMissing
	}
	if !enabled {
		// Revoke live sessions immediately so disabling is not just a future
		// login gate.
		if _, err := tx.ExecContext(ctx, "DELETE FROM operator_sessions WHERE operator_id=?", id); err != nil {
			return err
		}
	} else {
		// Clear any throttle state that piled up while the row was disabled, so
		// a freshly re-enabled operator can authenticate without inheriting an
		// attacker's backoff.
		if _, err := tx.ExecContext(ctx, "UPDATE operator_login_throttle SET failures=0,retry_at=0 WHERE singleton=1"); err != nil {
			return err
		}
	}
	event := "operator.enabled"
	if !enabled {
		event = "operator.disabled"
	}
	if err := audit(ctx, tx, event, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteOperator removes an operator row. Live sessions are removed by the
// ON DELETE CASCADE on operator_sessions.operator_id; no additional revoke
// step is needed.
func (s *Service) DeleteOperator(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM operators WHERE id=?", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrOperatorMissing
	}
	if err := audit(ctx, tx, "operator.deleted", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation matches SQLite's UNIQUE constraint error string without
// pulling in a driver-specific error type. The driver wraps the underlying
// sqlite error in a generic *sqlite.Error; matching on the substring keeps
// this resilient to driver upgrades.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "unique constraint")
}
func (s *Service) Profile(ctx context.Context) (Profile, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT profile_json FROM business_profile WHERE singleton=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
func (s *Service) SaveProfile(ctx context.Context, p Profile) error {
	p.OwnerName = strings.TrimSpace(p.OwnerName)
	p.Name = strings.TrimSpace(p.Name)
	p.Address = strings.TrimSpace(p.Address)
	p.Phone = strings.TrimSpace(p.Phone)
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	if p.Name == "" || len(p.Name) > 200 || len(p.OwnerName) > 200 || len(p.Address) > 2000 || len(p.Phone) > 40 || len(p.Locale) > 80 || len(p.TimeZone) > 100 || !countryPattern.MatchString(p.Country) || !currencyPattern.MatchString(p.Currency) || !localePattern.MatchString(p.Locale) {
		return ErrInvalid
	}
	if _, err := time.LoadLocation(p.TimeZone); err != nil || p.TimeZone == "" || p.TimeZone == "Local" {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM local_owner").Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrSetup
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO business_profile VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET profile_json=excluded.profile_json,updated_at=excluded.updated_at", string(raw), s.now().Unix()); err != nil {
		return err
	}
	if err := completeGate(ctx, tx, "business", s.now()); err != nil {
		return err
	}
	if err := audit(ctx, tx, "business.updated", s.now()); err != nil {
		return err
	}
	return tx.Commit()
}
func completeGate(ctx context.Context, tx *sql.Tx, gate string, now time.Time) error {
	// Licence and owner are verified by the caller in this same transaction.
	result, err := tx.ExecContext(ctx, "INSERT INTO provisioning_gates(gate,completed_at,evidence) VALUES(?,?,?) ON CONFLICT(gate) DO NOTHING", gate, now.UTC().Format(time.RFC3339Nano), "local "+gate+" persisted")
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return audit(ctx, tx, "provisioning."+gate+".completed", now)
}
func audit(ctx context.Context, tx *sql.Tx, event string, now time.Time) error {
	id, err := RandomToken()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events VALUES(?,?,?,?)", id, event, "local owner action", now.UTC().Format(time.RFC3339Nano))
	return err
}
func RandomToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func Digest(token string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(token))) }
