-- Payment verification, preparation and physical pickup are independent states.
CREATE TABLE kiosk_pickups (
 order_id TEXT PRIMARY KEY REFERENCES orders(id) ON DELETE RESTRICT,
 payment_reference TEXT NOT NULL,
 code_hash BLOB NOT NULL,
 code_cipher BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','claimed','expired')),
 preparation TEXT NOT NULL DEFAULT 'pending' CHECK(preparation IN ('pending','ready','failed')),
 prepared_digest TEXT NOT NULL DEFAULT '',
 expires_at INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX kiosk_active_code ON kiosk_pickups(code_hash) WHERE state='active';
CREATE TABLE kiosk_releases (
 order_id TEXT PRIMARY KEY REFERENCES kiosk_pickups(order_id) ON DELETE RESTRICT,
 prepared_digest TEXT NOT NULL,
 claimed_at INTEGER NOT NULL
);
