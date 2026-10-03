-- Retain checkout totals and recover the same gateway link after a lost response.
ALTER TABLE orders ADD COLUMN discount_code TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN discount_minor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN subtotal_minor INTEGER NOT NULL DEFAULT 0;
UPDATE orders SET subtotal_minor=total_minor;
ALTER TABLE payment_intents ADD COLUMN redirect_url TEXT NOT NULL DEFAULT '';
ALTER TABLE payment_intents ADD COLUMN link_expires_at INTEGER NOT NULL DEFAULT 0;
-- Empty service pricing means use the standard price book. Explicit entries
-- replace the book for that service, so unpriced combinations cannot be sold.
ALTER TABLE services ADD COLUMN pricing_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE services ADD COLUMN pricing_currency TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN pricing_minor_units INTEGER NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN discount_code TEXT NOT NULL DEFAULT '';
ALTER TABLE invoices ADD COLUMN discount_minor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN subtotal_minor INTEGER NOT NULL DEFAULT 0;
UPDATE invoices SET subtotal_minor=total_minor;
