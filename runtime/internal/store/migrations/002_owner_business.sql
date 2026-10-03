CREATE TABLE local_owner (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 username TEXT NOT NULL UNIQUE,
 password_hash TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE owner_sessions (
 token_hash TEXT PRIMARY KEY NOT NULL,
 owner_id INTEGER NOT NULL REFERENCES local_owner(singleton),
 expires_at INTEGER NOT NULL
);
CREATE TABLE owner_login_throttle (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 failures INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO owner_login_throttle(singleton) VALUES(1);
CREATE TABLE business_profile (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 profile_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
);
