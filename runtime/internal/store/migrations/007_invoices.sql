-- Phase 3F: order status actions and invoice generation.
--
-- An invoice is a durable, customer-readable record of a paid (or otherwise
-- settled) order. Each invoice has a unique sequence number per installation
-- that starts at 1 and never reuses a value, even if the order it referred
-- to was later cancelled. The invoice row stores the merchant identity
-- snapshotted at issue time (name, address, phone, country, currency, locale,
-- time zone) so the rendered invoice does not depend on the business profile
-- row staying constant. Order lines are duplicated into invoice_lines for the
-- same reason.
--
-- Status transitions on orders are recorded in audit_events (id, event_type,
-- evidence, occurred_at). That table already exists from Phase 1 and is the
-- canonical source of "what happened and when". No additional status-history
-- table is needed.

CREATE TABLE invoice_sequence (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    next_value INTEGER NOT NULL CHECK (next_value >= 1)
);
INSERT INTO invoice_sequence (singleton, next_value) VALUES (1, 1);

CREATE TABLE invoices (
    id                  TEXT    PRIMARY KEY NOT NULL,
    number              INTEGER NOT NULL UNIQUE,
    order_id            TEXT    NOT NULL UNIQUE REFERENCES orders(id) ON DELETE RESTRICT,
    issued_at           INTEGER NOT NULL,
    issued_by           TEXT    NOT NULL,
    currency            TEXT    NOT NULL,
    currency_minor_units INTEGER NOT NULL CHECK (currency_minor_units BETWEEN 0 AND 3),
    total_minor         INTEGER NOT NULL CHECK (total_minor >= 0),
    merchant_name       TEXT    NOT NULL,
    merchant_address    TEXT    NOT NULL DEFAULT '',
    merchant_phone      TEXT    NOT NULL DEFAULT '',
    merchant_country    TEXT    NOT NULL DEFAULT '',
    merchant_currency   TEXT    NOT NULL,
    merchant_locale     TEXT    NOT NULL DEFAULT '',
    merchant_time_zone  TEXT    NOT NULL DEFAULT '',
    customer_name       TEXT    NOT NULL,
    customer_phone      TEXT    NOT NULL,
    customer_email      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX invoices_order_id_idx ON invoices (order_id);
CREATE INDEX invoices_issued_at_idx ON invoices (issued_at DESC);

CREATE TABLE invoice_lines (
    id              TEXT    PRIMARY KEY NOT NULL,
    invoice_id      TEXT    NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    paper_size      TEXT    NOT NULL,
    colour_mode     TEXT    NOT NULL CHECK (colour_mode IN ('monochrome', 'colour')),
    sides           TEXT    NOT NULL CHECK (sides IN ('one-sided', 'two-sided-long-edge', 'two-sided-short-edge')),
    copies          INTEGER NOT NULL CHECK (copies >= 1),
    page_range_start INTEGER NOT NULL DEFAULT 1 CHECK (page_range_start >= 1),
    page_range_end   INTEGER NOT NULL CHECK (page_range_end >= page_range_start),
    unit_price_minor INTEGER NOT NULL CHECK (unit_price_minor >= 0),
    line_total_minor INTEGER NOT NULL CHECK (line_total_minor >= 0),
    document_original_filename TEXT NOT NULL DEFAULT ''
);

CREATE INDEX invoice_lines_invoice_id_idx ON invoice_lines (invoice_id);
