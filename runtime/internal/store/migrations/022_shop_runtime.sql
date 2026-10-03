-- Durable line-level dispatch journal and retained metadata after blob removal.
ALTER TABLE orders ADD COLUMN print_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN service_id TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN purged_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE payment_intents ADD COLUMN polled_at INTEGER NOT NULL DEFAULT 0;
CREATE TABLE print_submissions (
    line_id TEXT PRIMARY KEY REFERENCES order_lines(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK(state IN ('submitting','submitted','failed')),
    queue_name TEXT NOT NULL,
    job_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);
CREATE TABLE qr_theme (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    dark TEXT NOT NULL DEFAULT '#0a0a0a',
    light TEXT NOT NULL DEFAULT '#ffffff',
    frame TEXT NOT NULL DEFAULT '',
    logo BLOB
);
INSERT INTO qr_theme(singleton) VALUES(1);
-- No tables reference tunnel_state. Preserve every setting while adding direct IP access.
CREATE TABLE portal_access_state (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    provider TEXT NOT NULL DEFAULT 'direct' CHECK(provider IN ('direct','cloudflared','stub')),
    public_origin TEXT NOT NULL DEFAULT '', tunnel_token_ref TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unconfigured', last_verified_at INTEGER,
    last_verified_status INTEGER, last_verified_error TEXT NOT NULL DEFAULT '',
    last_attempt_at INTEGER, last_error TEXT NOT NULL DEFAULT '',
    qr_target_path TEXT NOT NULL DEFAULT '/portal/', shop_route TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);
INSERT INTO portal_access_state SELECT * FROM tunnel_state;
DROP TABLE tunnel_state;
ALTER TABLE portal_access_state RENAME TO tunnel_state;
