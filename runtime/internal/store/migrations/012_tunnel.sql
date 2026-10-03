-- Phase 7: Tunnel, custom domain and branded QR.
--
-- The tunnel supervisor owns the public-facing HTTPS route to the local
-- service. The local Go service binds only to loopback, so every customer
-- upload reaches the server through an outbound tunnel (Cloudflare-compatible
-- today, replaceable later) terminated at a custom domain the merchant owns.
-- This migration records the durable tunnel state, the configured public
-- origin, the verification result, and the QR branding the dashboard uses
-- to display the customer-facing link.
--
--   1. tunnel_state — singleton row carrying the current tunnel configuration
--      and verification result. The supervisor updates it on every transition
--      and the owner dashboard reads it on every status poll.
--
--   2. tunnel_events — append-only audit trail of every state change. The
--      audit lets the merchant see when the tunnel came up, when it failed
--      and which probe last succeeded without keeping unbounded state in the
--      singleton.
--
-- All tables are additive. No earlier table is altered. A pre-migration
-- snapshot is taken via the same VACUUM INTO procedure used by 002-011 so
-- the operator can roll back to a verified state without losing earlier
-- business, pricing, orders, notifications, printers, ID-card or passport
-- data.

CREATE TABLE tunnel_state (
    singleton                 INTEGER PRIMARY KEY CHECK (singleton = 1),
    provider                  TEXT    NOT NULL DEFAULT 'cloudflared'
                              CHECK (provider IN ('cloudflared', 'stub')),
    public_origin             TEXT    NOT NULL DEFAULT '',
    tunnel_token_ref          TEXT    NOT NULL DEFAULT '',
    status                    TEXT    NOT NULL DEFAULT 'unconfigured'
                              CHECK (status IN ('unconfigured', 'starting', 'verifying',
                                                'online', 'degraded', 'offline', 'error')),
    last_verified_at          INTEGER,
    last_verified_status      INTEGER,
    last_verified_error       TEXT    NOT NULL DEFAULT '',
    last_attempt_at           INTEGER,
    last_error                TEXT    NOT NULL DEFAULT '',
    qr_target_path            TEXT    NOT NULL DEFAULT '/portal/',
    shop_route                TEXT    NOT NULL DEFAULT '',
    updated_at                INTEGER NOT NULL
);

CREATE TABLE tunnel_events (
    id              TEXT    PRIMARY KEY NOT NULL,
    occurred_at     INTEGER NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('unconfigured', 'starting', 'verifying',
                                                       'online', 'degraded', 'offline', 'error')),
    detail          TEXT    NOT NULL DEFAULT '',
    http_status     INTEGER,
    round_trip_ms   INTEGER
);

CREATE INDEX tunnel_events_time_idx ON tunnel_events (occurred_at DESC);
