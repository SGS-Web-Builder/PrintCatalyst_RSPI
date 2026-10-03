CREATE TABLE paper_stock(printer_id TEXT PRIMARY KEY REFERENCES printers(id));
CREATE TABLE paper_loads(id TEXT PRIMARY KEY,printer_id TEXT NOT NULL REFERENCES paper_stock(printer_id),sheets INTEGER NOT NULL CHECK(sheets>0),created_at INTEGER NOT NULL);
CREATE TABLE paper_usage(printer_id TEXT NOT NULL REFERENCES paper_stock(printer_id),line_id TEXT NOT NULL,sheets INTEGER NOT NULL CHECK(sheets>=0),PRIMARY KEY(printer_id,line_id));
