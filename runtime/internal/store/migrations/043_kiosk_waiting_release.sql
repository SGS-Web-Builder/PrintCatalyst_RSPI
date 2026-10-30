-- A valid physical OTP entry can authorize waiting for preparation. This is
-- distinct from a release: dispatch still requires a claimed, ready digest.
CREATE TABLE kiosk_waiting_releases (
 order_id TEXT PRIMARY KEY REFERENCES orders(id),
 code_hash BLOB NOT NULL,
 pickup_created_at INTEGER NOT NULL
);
