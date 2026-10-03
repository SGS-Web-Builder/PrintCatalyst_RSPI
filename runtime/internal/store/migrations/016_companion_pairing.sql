-- Phase 10a: Companion application pairing.
--
-- The iOS and Android companion applications authenticate against an
-- On-Premise installation by exchanging a short-lived pairing code
-- for a long-lived bearer token. The migration adds four additive
-- tables; no earlier table is altered.
--
--   1. pair_codes — single-use pairing codes minted from the owner
--      dashboard. The cleartext code is never stored; only the SHA-256
--      hash is persisted so a database leak does not expose codes
--      that are still inside their TTL window. created_at, expires_at
--      and the optional consumed_at + consumed_by_fingerprint columns
--      let the owner audit which codes were minted, which were
--      exchanged and which expired untouched.
--
--   2. paired_devices — every device the server has ever accepted a
--      pairing code from. The row carries the claimed fingerprint,
--      the human-readable label, paired_at, last_seen_at and the
--      optional revoked_at + revoked_reason columns so a lost phone
--      can be revoked without losing the audit trail.
--
--   3. device_tokens — bearer tokens issued after a successful code
--      exchange. Each token is bound to a paired device and a
--      fingerprint; the cleartext token is never stored, only its
--      SHA-256 hash. The tokens table supports rotation: a device
--      can hold several live tokens at once, and revoking a device
--      revokes every outstanding token in a single transaction.
--
--   4. pair_exchange_attempts — append-only audit of every exchange
--      attempt. Indexed newest-first on (fingerprint, attempted_at)
--      so the rate-limit query in the service can throttle repeated
--      guesses without a table scan.
--
-- The migration is additive. A pre-migration snapshot is taken via
-- the same VACUUM INTO procedure used by 002-015 so the operator can
-- roll back to a verified state without losing earlier payments,
-- licensing, tunnel, passport, ID-card, printer, notification, order
-- or pricing data.

CREATE TABLE pair_codes (
    id                       TEXT    PRIMARY KEY,
    code_hash                TEXT    NOT NULL UNIQUE,
    created_at               TEXT    NOT NULL,
    expires_at               TEXT    NOT NULL,
    consumed_at              TEXT,
    consumed_by_fingerprint  TEXT
);

CREATE INDEX pair_codes_active_idx
    ON pair_codes (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE paired_devices (
    id              TEXT    PRIMARY KEY,
    fingerprint     TEXT    NOT NULL,
    label           TEXT    NOT NULL,
    paired_at       TEXT    NOT NULL,
    last_seen_at    TEXT    NOT NULL,
    revoked_at      TEXT,
    revoked_reason  TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX paired_devices_fingerprint_idx
    ON paired_devices (fingerprint, paired_at DESC);

CREATE TABLE device_tokens (
    token_hash  TEXT    PRIMARY KEY,
    device_id   TEXT    NOT NULL REFERENCES paired_devices(id) ON DELETE CASCADE,
    fingerprint TEXT    NOT NULL,
    issued_at   TEXT    NOT NULL,
    expires_at  TEXT    NOT NULL,
    revoked_at  TEXT
);

CREATE INDEX device_tokens_device_idx
    ON device_tokens (device_id)
    WHERE revoked_at IS NULL;

CREATE INDEX device_tokens_fingerprint_idx
    ON device_tokens (fingerprint, issued_at DESC);

CREATE TABLE pair_exchange_attempts (
    id            TEXT    PRIMARY KEY,
    fingerprint   TEXT    NOT NULL,
    attempted_at  TEXT    NOT NULL,
    succeeded     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX pair_exchange_attempts_recent_idx
    ON pair_exchange_attempts (fingerprint, attempted_at DESC);
