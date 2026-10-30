-- Owner policy permits unpaid CASH only; it never records payment.
CREATE VIEW kiosk_eligible_orders AS
 SELECT o.id FROM orders o WHERE o.status='paid' OR (
 o.status='pending_payment' AND o.payment_method='cash_on_counter' AND o.submitted_at>0 AND (
 EXISTS(SELECT 1 FROM business_settings WHERE singleton=1 AND auto_print_mode='all_documents')
 OR EXISTS(SELECT 1 FROM kiosk_releases r WHERE r.order_id=o.id)));
CREATE VIEW kiosk_release_authorizations AS
 SELECT order_id,payment_reference,verified_at FROM kiosk_authorized_payments
 UNION ALL
 SELECT o.id,'unpaid-cash:'||o.id,o.submitted_at FROM orders o
 WHERE o.status='pending_payment' AND o.payment_method='cash_on_counter' AND o.submitted_at>0
 AND o.id IN (SELECT id FROM kiosk_eligible_orders);
