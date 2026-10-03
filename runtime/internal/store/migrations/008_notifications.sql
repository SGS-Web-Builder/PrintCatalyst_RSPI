-- Phase 3G: notification settings (singleton) and a composite index on orders
-- to accelerate the reports queries (date range + status filter).
--
-- Notification settings are intentionally minimal for Phase 3G: in-process
-- desktop/audio alerts fire on every new order if enabled. Email and WhatsApp
-- adapters are adapter-stubs with credentials stored encrypted (Phase 3G does
-- not implement the actual transport; it provides the settings UI hook and a
-- clear "not configured" state).
--
-- The orders status+created_at index below is the only schema addition for
-- reports: summaries and top-combinations are computed from existing tables.

CREATE INDEX IF NOT EXISTS orders_status_created_idx ON orders (status, created_at DESC);

-- notification_settings is a singleton: exactly one row exists at any time.
-- The row is created by the migration so the service never needs to INSERT.
CREATE TABLE notification_settings (
    singleton               INTEGER PRIMARY KEY CHECK (singleton = 1),
    desktop_alerts_enabled INTEGER NOT NULL DEFAULT 1,
    audio_alerts_enabled   INTEGER NOT NULL DEFAULT 1,
    email_enabled          INTEGER NOT NULL DEFAULT 0,
    email_host             TEXT    NOT NULL DEFAULT '',
    email_port             INTEGER NOT NULL DEFAULT 587,
    email_username         TEXT    NOT NULL DEFAULT '',
    email_from             TEXT    NOT NULL DEFAULT '',
    whatsapp_enabled       INTEGER NOT NULL DEFAULT 0,
    whatsapp_api_url       TEXT    NOT NULL DEFAULT '',
    whatsapp_api_token     TEXT    NOT NULL DEFAULT '',
    whatsapp_to            TEXT    NOT NULL DEFAULT '',
    updated_at             INTEGER NOT NULL
);
INSERT INTO notification_settings (singleton, desktop_alerts_enabled, audio_alerts_enabled, updated_at)
VALUES (1, 1, 1, UNIXEPOCH());
