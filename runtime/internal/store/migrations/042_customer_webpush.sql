CREATE TABLE webpush_keys (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), private_key TEXT NOT NULL, public_key TEXT NOT NULL
);
CREATE TABLE webpush_orders (
 order_id TEXT PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
 subscription TEXT NOT NULL, created_at INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 sent_at INTEGER
);
