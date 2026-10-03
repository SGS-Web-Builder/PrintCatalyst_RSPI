CREATE TABLE portal_customer_details (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 settings TEXT NOT NULL
);
INSERT INTO portal_customer_details VALUES(1,'{"enabled":true,"fields":{"customerName":"required","customerPhone":"required","customerEmail":"optional","customerNotes":"optional"}}');
