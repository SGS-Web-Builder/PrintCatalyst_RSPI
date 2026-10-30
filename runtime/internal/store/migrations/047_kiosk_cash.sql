-- Staff confirmation is distinct from gateway verification.
CREATE TABLE kiosk_cash_payments (
 order_id TEXT PRIMARY KEY REFERENCES orders(id),
 amount_minor INTEGER NOT NULL,
 currency TEXT NOT NULL,
 currency_minor_units INTEGER NOT NULL,
 approved_by TEXT NOT NULL CHECK(length(approved_by)>0),
 approved_at INTEGER NOT NULL,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE VIEW kiosk_authorized_payments AS
 SELECT v.order_id,v.payment_reference,v.verified_at FROM kiosk_verified_payments v
 JOIN payment_intents i ON i.id=v.intent_id JOIN orders o ON o.id=v.order_id
 WHERE i.status='captured' AND i.gateway_payment_id=v.payment_reference
 AND i.amount_minor=o.total_minor AND i.currency=o.currency AND i.currency_minor_units=o.currency_minor_units
 UNION ALL
 SELECT c.order_id,'cash:'||c.order_id,c.approved_at FROM kiosk_cash_payments c JOIN orders o ON o.id=c.order_id
 WHERE o.payment_method='cash_on_counter' AND o.submitted_at>0 AND c.amount_minor=o.total_minor
 AND c.currency=o.currency AND c.currency_minor_units=o.currency_minor_units;
