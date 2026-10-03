-- 018_documents_dispatched_at.sql — record the moment the print
-- dispatcher reports a successful spool submission. The autodelete
-- sweeper treats a row with dispatched_at > 0 as eligible for purge
-- once the order is in a terminal state, even before the
-- retention_until window expires. This is the production "auto-delete
-- after print" behaviour the merchant requested: the moment the OS
-- confirms the print job is queued, the customer file is removed.
ALTER TABLE documents ADD COLUMN dispatched_at INTEGER NOT NULL DEFAULT 0;

-- The retention_until column already exists; no further schema
-- change is needed for the auto-delete feature to work end-to-end.
