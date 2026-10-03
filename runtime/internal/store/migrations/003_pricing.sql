-- Merchant pricing configuration only. A row records what the merchant charges
-- for a print combination; it is never evidence that a printer supports that
-- combination. Paper/media identifiers stay merchant-entered so the future
-- driver-discovered capability contract is not pre-empted by a catalog here.
CREATE TABLE pricing_book (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    currency TEXT NOT NULL CHECK (length(currency) = 3),
    currency_minor_units INTEGER NOT NULL CHECK (currency_minor_units BETWEEN 0 AND 3),
    price_unit TEXT NOT NULL CHECK (price_unit = 'sheet'),
    updated_at INTEGER NOT NULL
);

CREATE TABLE pricing_rules (
    id TEXT PRIMARY KEY NOT NULL,
    paper_size TEXT NOT NULL CHECK (length(paper_size) BETWEEN 1 AND 256),
    paper_key TEXT NOT NULL,
    colour_mode TEXT NOT NULL CHECK (colour_mode IN ('monochrome', 'colour')),
    sides TEXT NOT NULL CHECK (sides IN ('one-sided', 'two-sided-long-edge', 'two-sided-short-edge')),
    unit_price_minor INTEGER NOT NULL CHECK (unit_price_minor BETWEEN 0 AND 100000000),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX pricing_rules_combination_idx
ON pricing_rules (paper_key, colour_mode, sides);
