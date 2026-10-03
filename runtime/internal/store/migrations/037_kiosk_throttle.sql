CREATE TABLE kiosk_throttle (
 scope TEXT PRIMARY KEY CHECK(scope IN ('installation','physical-kiosk')),
 window_start INTEGER NOT NULL,
 attempts INTEGER NOT NULL,
 blocked_until INTEGER NOT NULL,
 strikes INTEGER NOT NULL,
 last_seen INTEGER NOT NULL
);
