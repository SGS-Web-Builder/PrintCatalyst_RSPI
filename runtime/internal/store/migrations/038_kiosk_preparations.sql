-- Prepared plans freeze order lines before expensive rendering, and survive restarts.
CREATE TABLE kiosk_preparations (
 order_id TEXT PRIMARY KEY REFERENCES orders(id) ON DELETE RESTRICT,
 plan_json TEXT NOT NULL,
 prepared_digest TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ready','failed')),
 updated_at INTEGER NOT NULL,
 error TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER kiosk_frozen_line_update BEFORE UPDATE ON order_lines
WHEN EXISTS(SELECT 1 FROM kiosk_preparations WHERE order_id=OLD.order_id OR order_id=NEW.order_id)
BEGIN SELECT RAISE(ABORT,'prepared order lines are frozen'); END;
CREATE TRIGGER kiosk_frozen_line_delete BEFORE DELETE ON order_lines
WHEN EXISTS(SELECT 1 FROM kiosk_preparations WHERE order_id=OLD.order_id)
BEGIN SELECT RAISE(ABORT,'prepared order lines are frozen'); END;
CREATE TRIGGER kiosk_frozen_line_insert BEFORE INSERT ON order_lines
WHEN EXISTS(SELECT 1 FROM kiosk_preparations WHERE order_id=NEW.order_id)
BEGIN SELECT RAISE(ABORT,'prepared order lines are frozen'); END;
CREATE TRIGGER kiosk_frozen_order BEFORE UPDATE OF primary_printer_id,service_id,total_minor,currency,currency_minor_units,customer_name,customer_phone,customer_email,customer_notes,discount_minor ON orders
WHEN EXISTS(SELECT 1 FROM kiosk_preparations WHERE order_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'prepared order details are frozen'); END;
CREATE TRIGGER kiosk_frozen_document BEFORE UPDATE OF storage_path,sha256,page_count,mime_type,size_bytes ON documents
WHEN EXISTS(SELECT 1 FROM order_lines l JOIN kiosk_preparations p ON p.order_id=l.order_id JOIN orders o ON o.id=l.order_id WHERE l.document_id=OLD.id AND o.status NOT IN ('completed','cancelled'))
BEGIN SELECT RAISE(ABORT,'prepared source is frozen'); END;
