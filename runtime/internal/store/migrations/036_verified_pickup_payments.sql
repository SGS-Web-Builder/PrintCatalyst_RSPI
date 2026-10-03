-- Durable work queue populated only by a server-verified merchant capture.
-- Manual approval / generic paid status never insert here.
CREATE TABLE kiosk_verified_payments (
 order_id TEXT PRIMARY KEY REFERENCES orders(id) ON DELETE RESTRICT,
 intent_id TEXT NOT NULL UNIQUE REFERENCES payment_intents(id),
 payment_reference TEXT NOT NULL UNIQUE,
 verified_at INTEGER NOT NULL,
 last_error TEXT NOT NULL DEFAULT ''
);
