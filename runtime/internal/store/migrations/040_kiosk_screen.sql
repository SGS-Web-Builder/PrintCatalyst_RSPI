CREATE TABLE kiosk_screen_session (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 token_hash BLOB NOT NULL, credential_hash BLOB NOT NULL, expires_at INTEGER NOT NULL
);
CREATE TABLE kiosk_screen_receipts (
 token_hash BLOB PRIMARY KEY, order_id TEXT NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX kiosk_receipt_expiry ON kiosk_screen_receipts(expires_at);
