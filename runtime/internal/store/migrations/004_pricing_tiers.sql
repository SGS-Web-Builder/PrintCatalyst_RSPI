-- Phase 3C: optional quantity-based discounts, scoped per pricing combination.
--
-- A tier never asserts that a printer supports a combination: it attaches to
-- the merchant's existing pricing_rules row by id and inherits that row's
-- paper_key/colour_mode/sides identity. Tiers are deleted with their parent
-- row, so removing a combination can never leave orphaned discounts that
-- silently apply to a different one.
--
-- The merchant's stored integers are never rescaled; the base price the tier
-- refers to lives in pricing_rules.unit_price_minor under the exponent the
-- merchant saved with, and a precision correction reuses the existing 003
-- confirmation flow without changing tier amounts behind the merchant's back.
CREATE TABLE pricing_tiers (
    id TEXT PRIMARY KEY NOT NULL,
    pricing_rule_id TEXT NOT NULL REFERENCES pricing_rules(id) ON DELETE CASCADE,
    min_quantity INTEGER NOT NULL CHECK (min_quantity >= 2),
    unit_price_minor INTEGER NOT NULL CHECK (unit_price_minor BETWEEN 0 AND 100000000),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX pricing_tiers_per_rule_threshold_idx
ON pricing_tiers (pricing_rule_id, min_quantity);
