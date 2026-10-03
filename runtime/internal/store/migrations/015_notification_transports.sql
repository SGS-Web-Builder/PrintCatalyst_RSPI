-- Phase 8c: Notification transports.
--
-- This slice introduces the email and WhatsApp transport delivery
-- audit and adds the missing SMTP password column the Phase 3G
-- settings row already promised to persist. The Phase 3G settings
-- row already carries the SMTP host/port/username/from and WhatsApp
-- URL/token/recipient the merchant configures; this migration also
-- records every delivery attempt so the merchant can see which
-- customer orders triggered an email or WhatsApp alert, what was
-- sent, and whether the transport succeeded.
--
--   1. email_password — the SMTP password, persisted alongside the
--      other email fields. The Phase 3G schema defined `email_token`
--      in the Settings struct but the underlying column was never
--      added; this migration adds it so the merchant-configured
--      credentials round-trip through Save and Load. The column is
--      added with an empty default so existing installations do not
--      lose their settings.
--
--   2. notification_deliveries — append-only log of every transport
--      attempt (email, whatsapp) with the order id, the subject/body
--      sent, the recipient, the transport kind, the outcome
--      (delivered, failed, skipped), the failure detail when one is
--      recorded, and the timestamp. Indexed newest-first on
--      `occurred_at` so the dashboard can render the recent delivery
--      list without a table scan.
--
-- The migration is additive. No earlier table is altered except for
-- the email_password column addition described above. A
-- pre-migration snapshot is taken via the same VACUUM INTO procedure
-- used by 002-014 so the operator can roll back to a verified state
-- without losing earlier payments, licensing, tunnel, passport,
-- ID-card, printer, notification, order or pricing data.

ALTER TABLE notification_settings ADD COLUMN email_password TEXT NOT NULL DEFAULT '';

CREATE TABLE notification_deliveries (
    id              TEXT    PRIMARY KEY,
    transport       TEXT    NOT NULL
                    CHECK (transport IN ('email', 'whatsapp')),
    order_id        TEXT    NOT NULL,
    recipient       TEXT    NOT NULL,
    subject         TEXT    NOT NULL DEFAULT '',
    body            TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL
                    CHECK (status IN ('delivered', 'failed', 'skipped')),
    failure_detail  TEXT    NOT NULL DEFAULT '',
    occurred_at     TEXT    NOT NULL
);

CREATE INDEX notification_deliveries_recent_idx
    ON notification_deliveries (occurred_at DESC);

CREATE INDEX notification_deliveries_order_idx
    ON notification_deliveries (order_id, occurred_at DESC);
