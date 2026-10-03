-- Phase 8: Licensing and payments.
--
-- This slice introduces the licensing foundation: device key pair,
-- entitlement records and the audit trail that proves what the local
-- service verified, when, and from which public key. Payments land in
-- a follow-on migration (014_payments.sql) so each migration stays small
-- enough to reason about and to roll back independently.
--
--   1. installation_keys — singleton row carrying the device key pair
--      that authenticates this installation to the control plane. The
--      private key is encrypted with a key derived from the on-disk
--      data directory so a copy of the SQLite file alone cannot recover
--      the private key. The public key is stored verbatim; the control
--      plane uses it to identify the installation.
--
--   2. licenses — every entitlement the control plane has signed. The
--      `is_current` flag marks the row that should be active today; a
--      license renewal writes a new row and flips the flag inside one
--      transaction so a crash mid-renewal cannot leave two current rows.
--      `revoked_at` records a revocation timestamp without losing the
--      row for audit purposes.
--
--   3. license_events — append-only audit trail of activation, renewal,
--      verification, transfer and revocation events. The audit is the
--      merchant-visible record that the licence was verified and on
--      which timestamp; the dashboard renders the most recent entries
--      newest-first.
--
-- All tables are additive. No earlier table is altered. A pre-migration
-- snapshot is taken via the same VACUUM INTO procedure used by 002-013
-- so the operator can roll back to a verified state without losing
-- earlier business, pricing, orders, notifications, printers, ID-card
-- or passport data.

CREATE TABLE installation_keys (
    singleton              INTEGER PRIMARY KEY CHECK (singleton = 1),
    installation_id        TEXT    NOT NULL UNIQUE,
    public_key             TEXT    NOT NULL,
    encrypted_private_key  BLOB    NOT NULL,
    encryption_nonce       BLOB    NOT NULL,
    created_at             TEXT    NOT NULL,
    rotated_at             TEXT
);

CREATE TABLE licenses (
    id                  TEXT    PRIMARY KEY,
    installation_id     TEXT    NOT NULL,
    product             TEXT    NOT NULL,
    edition             TEXT    NOT NULL,
    device_fingerprint  TEXT    NOT NULL,
    issued_at           TEXT    NOT NULL,
    support_until       TEXT    NOT NULL,
    entitlements_json   TEXT    NOT NULL DEFAULT '[]',
    nonce               TEXT    NOT NULL,
    payload             BLOB    NOT NULL,
    signature           BLOB    NOT NULL,
    is_current          INTEGER NOT NULL DEFAULT 0
                        CHECK (is_current IN (0, 1)),
    received_at         TEXT    NOT NULL,
    revoked_at          TEXT,
    revoked_reason      TEXT
);

CREATE UNIQUE INDEX licenses_single_current
    ON licenses (is_current)
    WHERE is_current = 1;

CREATE INDEX licenses_installation_idx
    ON licenses (installation_id, received_at DESC);

CREATE TABLE license_events (
    id              TEXT    PRIMARY KEY,
    license_id      TEXT,
    event_type      TEXT    NOT NULL,
    detail          TEXT    NOT NULL DEFAULT '',
    payload_hash    TEXT,
    occurred_at     TEXT    NOT NULL
);

CREATE INDEX license_events_recent_idx
    ON license_events (occurred_at DESC);
