package pricing

import (
	"errors"
	"math"
	"sort"
)

// Tier is a discounted per-sheet price that applies once an order line has at
// least MinQuantity physical sheets of the parent combination. Tiers are
// scoped to a single pricing combination (paper size + colour mode + sides)
// and never span combinations.
type Tier struct {
	// MinQuantity is the inclusive sheet threshold at which UnitPriceMinor
	// becomes the entire line's unit price. The base price applies below it.
	MinQuantity int64 `json:"minQuantity"`
	// UnitPriceMinor is integer minor units of the business-profile currency,
	// using the same exponent the parent Entry was saved under. It must be
	// strictly less than every lower-quantity tier (including the base) and
	// at most MaxUnitPriceMinor.
	UnitPriceMinor int64 `json:"unitPriceMinor"`
}

// MaxQuantity bounds a single line so Quantity * UnitPriceMinor cannot
// overflow int64 even at the per-sheet ceiling. One billion sheets is well
// above any commercial print run and keeps the product under 2**63.
const MaxQuantity int64 = 1_000_000_000

// ErrInvalidDiscount is returned by Calculate* for inputs that the pure
// function cannot accept. Service-layer validation produces the same errors
// from Save so HTTP callers see a stable 400 with no partial write.
var (
	ErrInvalidQuantity = errors.New("quantity must be between 1 and 1000000000")
	ErrInvalidTier     = errors.New("tier minQuantity must be at least 2 and strictly less than every lower-quantity tier")
)

// CalculateUnitPriceMinor returns the per-sheet minor-unit price for a single
// line of Quantity sheets, given the parent combination's base price and its
// optional quantity tiers. The highest qualifying minimum-quantity threshold
// wins; quantities below every threshold use the base price. The function is
// defensive: even if a caller passes an unsorted, duplicate, or non-monotonic
// tier list, it refuses with ErrInvalidTier rather than silently picking a
// tier the merchant did not intend.
func CalculateUnitPriceMinor(baseMinor int64, tiers []Tier, quantity int64) (int64, error) {
	if quantity < 1 || quantity > MaxQuantity {
		return 0, ErrInvalidQuantity
	}
	if baseMinor < 0 || baseMinor > maxUnitPriceMinor {
		return 0, ErrInvalid
	}
	previous := baseMinor
	for _, tier := range tiers {
		if tier.MinQuantity < 2 || tier.MinQuantity > MaxQuantity {
			return 0, ErrInvalidTier
		}
		if tier.UnitPriceMinor < 0 || tier.UnitPriceMinor > maxUnitPriceMinor {
			return 0, ErrInvalid
		}
		if tier.UnitPriceMinor >= previous {
			return 0, ErrInvalidTier
		}
		previous = tier.UnitPriceMinor
	}
	unit := baseMinor
	for _, tier := range tiers {
		if quantity >= tier.MinQuantity && tier.UnitPriceMinor < unit {
			unit = tier.UnitPriceMinor
		}
	}
	return unit, nil
}

// LineTotal is the result of pricing a single line. UnitPriceMinor is what
// CalculateUnitPriceMinor returned; TotalMinor is UnitPriceMinor * Quantity
// with explicit overflow detection so a defensive caller can refuse to
// invoice a value that does not fit an int64.
type LineTotal struct {
	UnitPriceMinor int64
	TotalMinor     int64
}

// CalculateLineTotal returns the per-sheet unit price and the line total for
// Quantity sheets of the parent combination. The function refuses silently
// overflowing arithmetic: 1e9 sheets * 1e8 minor units = 1e17, which fits in
// int64 (max ~9.2e18), but the explicit check lets callers return a clear
// error rather than a wrapped-around number.
func CalculateLineTotal(baseMinor int64, tiers []Tier, quantity int64) (LineTotal, error) {
	unit, err := CalculateUnitPriceMinor(baseMinor, tiers, quantity)
	if err != nil {
		return LineTotal{}, err
	}
	if quantity > math.MaxInt64/unit {
		return LineTotal{}, ErrInvalid
	}
	return LineTotal{UnitPriceMinor: unit, TotalMinor: unit * quantity}, nil
}

// validateTiers returns nil when every tier satisfies the documented rules.
// The combination's base price is the implicit "quantity 1" tier, so the
// first tier must already be strictly cheaper than the base, and each
// subsequent tier strictly cheaper than the previous one. Tiers are sorted
// ascending by MinQuantity so duplicate thresholds surface as adjacent
// equals before any monotonicity check. The function returns ErrInvalid
// (rather than the more specific ErrInvalidTier the pure calculation uses)
// so the service-layer HTTP mapping gives the merchant a single 400.
func validateTiers(baseMinor int64, tiers []Tier) error {
	if len(tiers) == 0 {
		return nil
	}
	if baseMinor < 0 || baseMinor > maxUnitPriceMinor {
		return ErrInvalid
	}
	sorted := make([]Tier, len(tiers))
	copy(sorted, tiers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MinQuantity < sorted[j].MinQuantity })
	previous := baseMinor
	lastQuantity := int64(-1)
	for _, tier := range sorted {
		if tier.MinQuantity == lastQuantity {
			return ErrInvalid
		}
		lastQuantity = tier.MinQuantity
		if tier.MinQuantity < 2 || tier.MinQuantity > MaxQuantity {
			return ErrInvalid
		}
		if tier.UnitPriceMinor < 0 || tier.UnitPriceMinor > maxUnitPriceMinor {
			return ErrInvalid
		}
		if tier.UnitPriceMinor >= previous {
			return ErrInvalid
		}
		previous = tier.UnitPriceMinor
	}
	return nil
}

// hasFreeTier reports whether any tier prices a line at zero minor units. A
// free tier is allowed only when the merchant confirms the request
// explicitly, so Save returns 400 otherwise. This helper exists so the
// service and any future UI hint share the same rule.
func hasFreeTier(tiers []Tier) bool {
	for _, tier := range tiers {
		if tier.UnitPriceMinor == 0 {
			return true
		}
	}
	return false
}
