-- 017_order_submitted_at.sql — record the first time a portal order was
-- fully submitted (lines populated, share token issued). The status column
-- stays at 'pending_payment' throughout the customer-facing payment flow;
-- submitted_at is the marker the portal uses to reject a duplicate
-- submission that would otherwise re-append lines and rotate the share
-- token.
ALTER TABLE orders ADD COLUMN submitted_at INTEGER NOT NULL DEFAULT 0;
