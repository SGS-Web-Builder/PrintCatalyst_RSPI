CREATE TABLE permanent_license_device(singleton INTEGER PRIMARY KEY CHECK(singleton=1),private_key BLOB NOT NULL,public_key BLOB NOT NULL);
CREATE TABLE permanent_license_state(singleton INTEGER PRIMARY KEY CHECK(singleton=1),envelope BLOB NOT NULL,last_seen INTEGER NOT NULL);
