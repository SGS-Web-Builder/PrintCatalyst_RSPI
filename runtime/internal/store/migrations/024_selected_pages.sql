ALTER TABLE order_lines ADD COLUMN selected_pages_json TEXT NOT NULL DEFAULT 'null';
ALTER TABLE invoice_lines ADD COLUMN selected_pages_json TEXT NOT NULL DEFAULT 'null';
