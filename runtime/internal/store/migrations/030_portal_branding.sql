CREATE TABLE portal_branding(singleton INTEGER PRIMARY KEY CHECK(singleton=1), settings TEXT NOT NULL);
INSERT INTO portal_branding VALUES(1,'{"title":"PrintCatalyst","subtitle":"Your local print shop · Secure document upload","logo":"","icon":""}');
