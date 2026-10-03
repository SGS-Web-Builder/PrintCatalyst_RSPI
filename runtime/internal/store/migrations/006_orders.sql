-- Phase 3E: customer upload, document storage, order intake.
--
-- Documents are stored in the protected data directory under a per-installation
-- sub-tree keyed by order_id. Each document record holds the original filename,
-- MIME type, size in bytes, server-counted page count, SHA-256 digest and the
-- relative path to the blob. The blob directory is created on demand.
--
-- Retention is a server-side policy: retention_until is set at insert time from
-- the configured document_retention_days (default 7). Documents with a past
-- retention_until are eligible for server-initiated deletion by a background job
-- (a later phase). During order cancellation, documents are deleted immediately
-- via ON DELETE CASCADE so the blob and row always stay consistent.
--
-- Orders are created by customers through the portal. Their status starts as
-- pending_payment and advances through a defined lifecycle. The currency and
-- minor units are snapshotted at creation time from the business profile so
-- existing orders are never affected by later pricing or currency corrections.
--
-- Order lines reference both the order and the document, making each line
-- traceable to the customer's original file. Line total_minor is computed
-- server-side from sheets * unit_price_minor so a client-submitted total is
-- never trusted.
--
-- A share token lets the customer retrieve their order status without a session:
-- a random 32-byte hex string stored on the order row. It is not secret (the
-- customer already has the order ID from the confirmation screen), but it
-- prevents accidental enumeration.
--
-- The portal_gate column on the orders table records whether the order was
-- created when the provisioning order_intake gate was open. It is informational
-- only; changing the gate later does not retroactively invalidate prior orders.

-- Customer-uploaded document blobs.
CREATE TABLE documents (
    id               TEXT    PRIMARY KEY NOT NULL,
    order_id         TEXT    REFERENCES orders(id) ON DELETE CASCADE,
    original_filename TEXT   NOT NULL,
    mime_type        TEXT    NOT NULL CHECK (mime_type IN ('application/pdf', 'image/jpeg', 'image/png')),
    size_bytes       INTEGER NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 52428800),
    page_count       INTEGER NOT NULL CHECK (page_count >= 1),
    sha256           TEXT    NOT NULL,
    storage_path     TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    retention_until  INTEGER NOT NULL
);

CREATE INDEX documents_order_id_idx ON documents (order_id);

-- Print order created by a customer through the portal. The share_token
-- allows order status retrieval without a session cookie; it is not used for
-- authentication in this phase.
CREATE TABLE orders (
    id                    TEXT    PRIMARY KEY NOT NULL,
    share_token           TEXT    NOT NULL UNIQUE,
    status                TEXT    NOT NULL DEFAULT 'pending_payment'
                               CHECK (status IN ('pending_payment', 'paid', 'dispatched', 'completed', 'failed', 'cancelled')),
    currency              TEXT    NOT NULL,
    currency_minor_units  INTEGER NOT NULL CHECK (currency_minor_units BETWEEN 0 AND 3),
    total_minor           INTEGER NOT NULL CHECK (total_minor >= 0),
    customer_name         TEXT    NOT NULL,
    customer_phone        TEXT    NOT NULL,
    customer_email        TEXT    NOT NULL DEFAULT '',
    customer_notes        TEXT    NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    portal_gate           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX orders_status_idx ON orders (status);
CREATE INDEX orders_created_at_idx ON orders (created_at DESC);

-- Each print line references a document and records the pricing combination,
-- page range, copies and the server-computed unit price and line total.
CREATE TABLE order_lines (
    id              TEXT    PRIMARY KEY NOT NULL,
    order_id        TEXT    NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    document_id    TEXT    NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    paper_size      TEXT    NOT NULL,
    paper_key       TEXT    NOT NULL,
    colour_mode     TEXT    NOT NULL CHECK (colour_mode IN ('monochrome', 'colour')),
    sides           TEXT    NOT NULL CHECK (sides IN ('one-sided', 'two-sided-long-edge', 'two-sided-short-edge')),
    copies          INTEGER NOT NULL CHECK (copies >= 1),
    page_range_start INTEGER NOT NULL DEFAULT 1 CHECK (page_range_start >= 1),
    page_range_end  INTEGER NOT NULL CHECK (page_range_end >= page_range_start),
    unit_price_minor INTEGER NOT NULL CHECK (unit_price_minor >= 0),
    line_total_minor INTEGER NOT NULL CHECK (line_total_minor >= 0)
);

CREATE INDEX order_lines_order_id_idx ON order_lines (order_id);
CREATE INDEX order_lines_document_id_idx ON order_lines (document_id);
