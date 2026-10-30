ALTER TABLE kiosk_control ADD COLUMN otp_enabled INTEGER NOT NULL DEFAULT 1 CHECK(otp_enabled IN(0,1));
ALTER TABLE kiosk_control ADD COLUMN countdown_enabled INTEGER NOT NULL DEFAULT 1 CHECK(countdown_enabled IN(0,1));
ALTER TABLE kiosk_releases ADD COLUMN release_mode TEXT NOT NULL DEFAULT 'otp' CHECK(release_mode IN('otp','automatic'));
CREATE TABLE kiosk_screen_finished(order_id TEXT PRIMARY KEY REFERENCES orders(id),finished_at INTEGER NOT NULL);
