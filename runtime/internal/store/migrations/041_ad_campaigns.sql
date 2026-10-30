CREATE TABLE ad_campaigns (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, filename TEXT NOT NULL UNIQUE,
 media_type TEXT NOT NULL, size INTEGER NOT NULL, schedule TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE ad_admin_sessions (
 token_hash TEXT PRIMARY KEY, owner_session_hash TEXT NOT NULL, expires_at INTEGER NOT NULL
);
