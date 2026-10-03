CREATE TABLE printer_price_books (
 printer_id TEXT PRIMARY KEY REFERENCES printers(id),
 currency TEXT NOT NULL,
 minor_units INTEGER NOT NULL,
 entries_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
);
