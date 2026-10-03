-- 020_business_settings.sql — central merchant configuration that
-- was previously scattered across ad-hoc tables. The single-row
-- "business_settings" table is the source of truth for:
--
--   auto_print_mode  — controls when the runtime auto-dispatches a
--                       paid order to the printer. Three values:
--                         "off"                — merchant must hit
--                                               Print manually.
--                         "on_payment_captured" — print the moment the
--                                               intent reaches
--                                               StatusCaptured (this is
--                                               the documented default).
--                         "all_documents"      — print every line
--                                               immediately when the
--                                               order is submitted, even
--                                               before payment. Use this
--                       on a counter-only kiosk.
--   shop_name, address, gstin, phone, email, receipt_footer,
--   logo_path — surfaced on every printed receipt and on the customer
--                portal header.
--   primary_printer_id — when set, all auto-printed orders are
--                         dispatched to this printer unless the order
--                         carries an explicit printer assignment. NULL
--                         means "use the first enabled printer".
--
-- The table is created with the singleton=1 row pattern used
-- elsewhere in the schema so future migrations can ALTER its
-- columns without rewriting existing data.
CREATE TABLE IF NOT EXISTS business_settings (
    singleton              INTEGER NOT NULL DEFAULT 1 CHECK (singleton = 1),
    shop_name              TEXT    NOT NULL DEFAULT '',
    address                TEXT    NOT NULL DEFAULT '',
    gstin                  TEXT    NOT NULL DEFAULT '',
    phone                  TEXT    NOT NULL DEFAULT '',
    email                  TEXT    NOT NULL DEFAULT '',
    receipt_footer         TEXT    NOT NULL DEFAULT '',
    logo_path              TEXT    NOT NULL DEFAULT '',
    auto_print_mode        TEXT    NOT NULL DEFAULT 'on_payment_captured'
                                       CHECK (auto_print_mode IN
                                              ('off','on_payment_captured','all_documents')),
    primary_printer_id     TEXT    NOT NULL DEFAULT '',
    auto_delete_enabled    INTEGER NOT NULL DEFAULT 1,
    auto_delete_minutes    INTEGER NOT NULL DEFAULT 60,
    cash_on_counter_label  TEXT    NOT NULL DEFAULT 'Cash on Counter',
    razorpay_label         TEXT    NOT NULL DEFAULT 'Pay Online (Razorpay)',
    updated_at             INTEGER NOT NULL DEFAULT 0
);

-- Seed the singleton row so dashboard reads always succeed. Without
-- the seed every SELECT needs an OR IS NULL guard; with the seed
-- the dashboard can treat the row as always present.
INSERT OR IGNORE INTO business_settings (singleton, updated_at)
VALUES (1, CAST(strftime('%s','now') AS INTEGER));

-- Service catalogue: the merchant enables a row per service (A4 B&W,
-- A4 colour, ID card 35x45mm, passport 35x45mm, etc.) and assigns it
-- to one or more printers. Every enabled service produces a row in
-- the dashboard's "Services" panel. The price_book_id is nullable
-- because the merchant might enable a service before pricing it.
CREATE TABLE IF NOT EXISTS services (
    id              TEXT    PRIMARY KEY,
    code            TEXT    NOT NULL UNIQUE,
    display_name    TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    enabled         INTEGER NOT NULL DEFAULT 1,
    price_book_id   TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- Many-to-many between services and printers. A row says "this
-- service can be printed on this printer". The runtime refuses to
-- accept an order whose lines reference a (service, printer) pair
-- not present here.
CREATE TABLE IF NOT EXISTS service_printers (
    service_id      TEXT    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    printer_id      TEXT    NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (service_id, printer_id)
);

CREATE INDEX IF NOT EXISTS idx_service_printers_printer
    ON service_printers (printer_id);

-- Discount codes the merchant can issue. The discount_type enum is
-- enforced by the CHECK constraint. Validity is bounded by
-- valid_from / valid_until; the dashboard hides expired rows but the
-- runtime still re-validates them when an order is placed.
CREATE TABLE IF NOT EXISTS discounts (
    id              TEXT    PRIMARY KEY,
    code            TEXT    NOT NULL UNIQUE,
    display_name    TEXT    NOT NULL,
    discount_type   TEXT    NOT NULL CHECK (discount_type IN ('percent','flat')),
    discount_value  INTEGER NOT NULL CHECK (discount_value >= 0),
    min_order_minor INTEGER NOT NULL DEFAULT 0,
    max_uses        INTEGER NOT NULL DEFAULT 0, -- 0 = unlimited
    times_used      INTEGER NOT NULL DEFAULT 0,
    valid_from      INTEGER NOT NULL DEFAULT 0,
    valid_until     INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_discounts_enabled
    ON discounts (enabled, valid_from, valid_until);
