-- Phase 3D: operators with server-enforced roles.
--
-- Operators are additional local user accounts that the owner can create so
-- day-to-day tasks (reading pricing, editing pricing, viewing reports) can be
-- delegated without sharing the owner password. The owner remains the only
-- role permitted to manage business settings, payment configuration, licence
-- data or operator membership. Roles are server-enforced: every protected
-- handler is gated by a permission check, never by the UI alone.
--
-- Operator sessions are stored separately from owner_sessions so the existing
-- local_owner singleton row and its FK semantics are not touched. Operators
-- share the same HttpOnly SameSite=Strict cookie, the same CSRF token
-- derivation and the same eight-hour session lifetime; the session row just
-- points at operators instead of local_owner.
--
-- Passwords use the same PBKDF2-SHA256 + 600,000 iterations + 16-byte salt
-- format as the owner, stored as the same `pbkdf2-sha256$600000$...$...`
-- envelope. Existing operators.password_hash reads are handled by the shared
-- verifyPassword helper.
CREATE TABLE operators (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('operator')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX operators_enabled_idx ON operators (enabled);

CREATE TABLE operator_sessions (
    token_hash TEXT PRIMARY KEY NOT NULL,
    operator_id INTEGER NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);
CREATE INDEX operator_sessions_expiry_idx ON operator_sessions (expires_at);

-- Operators share the same throttling mechanism as the owner, but with its
-- own backoff row so a brute-force attempt against one username class cannot
-- exhaust the other's retry window.
CREATE TABLE operator_login_throttle (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    failures INTEGER NOT NULL DEFAULT 0,
    retry_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO operator_login_throttle (singleton) VALUES(1);