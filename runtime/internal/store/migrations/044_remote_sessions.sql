CREATE TABLE remote_sessions (token_hash TEXT PRIMARY KEY, owner_session_hash TEXT NOT NULL, expires_at INTEGER NOT NULL);
