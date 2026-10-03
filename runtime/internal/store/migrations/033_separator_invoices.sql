CREATE TABLE printer_invoice_settings (
 printer_id TEXT PRIMARY KEY REFERENCES printers(id),
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 threshold INTEGER NOT NULL DEFAULT 4 CHECK(threshold BETWEEN 0 AND 10000),
 paper TEXT NOT NULL DEFAULT '', tray TEXT NOT NULL DEFAULT ''
);
-- Freeze routing and the threshold decision before any document leaves a group.
CREATE TABLE print_routes(line_id TEXT PRIMARY KEY REFERENCES order_lines(id),queue_name TEXT NOT NULL);
CREATE TABLE separator_invoices (
 order_id TEXT NOT NULL REFERENCES orders(id), queue_name TEXT NOT NULL,
 paper TEXT NOT NULL, tray TEXT NOT NULL, queue_count INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('skipped','waiting','submitting','submitted','failed')),
 job_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', sheets INTEGER NOT NULL DEFAULT 0,
 progress TEXT NOT NULL DEFAULT '', progress_detail TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL, PRIMARY KEY(order_id,queue_name)
);
