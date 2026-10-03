CREATE TABLE portal_payment_buttons (
 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
 cash_enabled INTEGER NOT NULL DEFAULT 1 CHECK (cash_enabled IN (0,1)),
 online_enabled INTEGER NOT NULL DEFAULT 1 CHECK (online_enabled IN (0,1)),
 CHECK (cash_enabled = 1 OR online_enabled = 1)
);
INSERT INTO portal_payment_buttons(singleton) VALUES(1);
