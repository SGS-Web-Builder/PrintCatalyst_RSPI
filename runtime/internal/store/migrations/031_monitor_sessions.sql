-- Remote monitoring tokens cannot be used as local administration tokens.
CREATE TABLE monitor_sessions (
 token_hash TEXT PRIMARY KEY NOT NULL,
 owner_session_hash TEXT NOT NULL,
 expires_at INTEGER NOT NULL
);
