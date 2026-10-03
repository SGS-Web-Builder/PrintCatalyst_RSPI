-- Phase 8b: Payments.
--
-- This slice introduces the local payment surface: the merchant's payment
-- provider configurations, the local payment intent that bridges the
-- customer portal to the gateway, the signed authorisation the platform
-- Razorpay broker returns, the append-only ledger of every state change,
-- the webhook receiver with idempotency tracking, and the manual cash /
-- UPI approval rows the merchant can use when no gateway is configured.
--
--   1. payment_providers — one row per configured provider
--      (razorpay_platform, razorpay_merchant, manual). The platform row
--      is implicit; the merchant owns every other row. `encrypted_secret`
--      stores the merchant-owned gateway secret (key id, webhook secret,
--      API token) encrypted with the same AES-256-GCM key the licensing
--      device key uses so the SQLite file alone cannot recover them.
--
--   2. payment_intents — every payment attempt the local service has
--      created for an order. Carries the idempotency key the gateway
--      will dedupe on, the customer contact details, the gateway's
--      order / payment identifiers once known, and the lifecycle status.
--      The unique index on `idempotency_key` guarantees a duplicate
--      portal submission cannot create two intents.
--
--   3. payment_authorizations — the signed authorisation the control
--      plane returns for the platform Razorpay path. The local service
--      verifies the signature and persists the envelope before marking
--      the order as paid. One authorisation per intent; the unique
--      index on `intent_id` enforces that.
--
--   4. payment_attempts — append-only log of every external interaction
--      (create, redirect, webhook, capture, verify, manual_approve,
--      manual_reject) so a support call can replay what happened.
--
--   5. payment_webhook_events — every webhook delivery with its
--      gateway event id, the raw payload, the supplied signature, the
--      verification result, the dedupe idempotency key, and the
--      processed timestamp. A unique index on `idempotency_key` means
--      a retried webhook cannot be processed twice.
--
--   6. payment_ledger — every state change to a payment intent, with
--      the actor (system, owner, operator name) and the amount delta.
--      The audit is the merchant-visible record of who authorised what.
--
--   7. manual_payments — explicit cash / UPI / bank-transfer approvals.
--      The brief requires the offline path never to be presented as
--      gateway-verified payment; this table records who approved, when,
--      and the reference the customer supplied.
--
-- All tables are additive. No earlier table is altered. A pre-migration
-- snapshot is taken via the same VACUUM INTO procedure used by 002-013
-- so the operator can roll back to a verified state without losing
-- earlier licensing, tunnel, passport, ID-card, printer, notification,
-- order or pricing data.

CREATE TABLE payment_providers (
    id                  TEXT    PRIMARY KEY,
    kind                TEXT    NOT NULL
                        CHECK (kind IN ('razorpay_platform', 'razorpay_merchant', 'manual')),
    display_name        TEXT    NOT NULL,
    enabled             INTEGER NOT NULL DEFAULT 1
                        CHECK (enabled IN (0, 1)),
    is_default          INTEGER NOT NULL DEFAULT 0
                        CHECK (is_default IN (0, 1)),
    config_json         TEXT    NOT NULL DEFAULT '{}',
    encrypted_secret    BLOB,
    secret_nonce        BLOB,
    created_at          TEXT    NOT NULL,
    updated_at          TEXT    NOT NULL
);

CREATE UNIQUE INDEX payment_providers_single_default
    ON payment_providers (is_default)
    WHERE is_default = 1;

CREATE TABLE payment_intents (
    id                  TEXT    PRIMARY KEY,
    order_id            TEXT    NOT NULL,
    installation_id     TEXT    NOT NULL,
    provider_id         TEXT    NOT NULL,
    amount_minor        INTEGER NOT NULL CHECK (amount_minor >= 0),
    currency            TEXT    NOT NULL,
    currency_minor_units INTEGER NOT NULL CHECK (currency_minor_units >= 0),
    status              TEXT    NOT NULL
                        CHECK (status IN ('pending', 'redirected', 'authorized', 'captured', 'failed', 'cancelled', 'expired')),
    gateway_order_id    TEXT,
    gateway_payment_id  TEXT,
    customer_name       TEXT    NOT NULL DEFAULT '',
    customer_phone      TEXT    NOT NULL DEFAULT '',
    customer_email      TEXT    NOT NULL DEFAULT '',
    idempotency_key     TEXT    NOT NULL UNIQUE,
    created_at          TEXT    NOT NULL,
    updated_at          TEXT    NOT NULL
);

CREATE INDEX payment_intents_order_idx
    ON payment_intents (order_id, created_at DESC);

CREATE INDEX payment_intents_status_idx
    ON payment_intents (status, created_at DESC);

CREATE TABLE payment_authorizations (
    id                  TEXT    PRIMARY KEY,
    intent_id           TEXT    NOT NULL UNIQUE,
    installation_id     TEXT    NOT NULL,
    order_id            TEXT    NOT NULL,
    gateway_payment_id  TEXT    NOT NULL,
    amount_minor        INTEGER NOT NULL,
    currency            TEXT    NOT NULL,
    currency_minor_units INTEGER NOT NULL,
    authorized_at       TEXT    NOT NULL,
    nonce               TEXT    NOT NULL,
    payload             BLOB    NOT NULL,
    signature           BLOB    NOT NULL,
    verified_at         TEXT    NOT NULL
);

CREATE TABLE payment_attempts (
    id                  TEXT    PRIMARY KEY,
    intent_id           TEXT    NOT NULL,
    attempt_kind        TEXT    NOT NULL
                        CHECK (attempt_kind IN ('create', 'redirect', 'webhook', 'capture', 'verify', 'manual_approve', 'manual_reject')),
    status              TEXT    NOT NULL,
    detail              TEXT    NOT NULL DEFAULT '',
    occurred_at         TEXT    NOT NULL
);

CREATE INDEX payment_attempts_intent_idx
    ON payment_attempts (intent_id, occurred_at DESC);

CREATE TABLE payment_webhook_events (
    id                  TEXT    PRIMARY KEY,
    provider_id         TEXT    NOT NULL,
    gateway_event_id    TEXT    NOT NULL,
    intent_id           TEXT,
    idempotency_key     TEXT    NOT NULL UNIQUE,
    raw_payload         BLOB    NOT NULL,
    signature           TEXT    NOT NULL DEFAULT '',
    verified            INTEGER NOT NULL
                        CHECK (verified IN (0, 1)),
    received_at         TEXT    NOT NULL,
    processed_at        TEXT
);

CREATE TABLE payment_ledger (
    id                  TEXT    PRIMARY KEY,
    intent_id           TEXT    NOT NULL,
    event_type          TEXT    NOT NULL,
    detail              TEXT    NOT NULL DEFAULT '',
    actor               TEXT    NOT NULL DEFAULT 'system',
    amount_minor        INTEGER,
    occurred_at         TEXT    NOT NULL
);

CREATE INDEX payment_ledger_intent_idx
    ON payment_ledger (intent_id, occurred_at DESC);

CREATE TABLE manual_payments (
    id                  TEXT    PRIMARY KEY,
    intent_id           TEXT    NOT NULL UNIQUE,
    method              TEXT    NOT NULL
                        CHECK (method IN ('cash', 'upi', 'bank_transfer', 'other')),
    reference           TEXT    NOT NULL DEFAULT '',
    amount_minor        INTEGER NOT NULL CHECK (amount_minor >= 0),
    currency            TEXT    NOT NULL,
    approved_by         TEXT    NOT NULL,
    note                TEXT    NOT NULL DEFAULT '',
    approved_at         TEXT    NOT NULL
);
