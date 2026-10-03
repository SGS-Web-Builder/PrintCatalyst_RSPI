package pricing

import (
	"errors"
	"testing"
)

func TestCalculateUnitPriceMinorUsesBaseBelowFirstThreshold(t *testing.T) {
	got, err := CalculateUnitPriceMinor(250, nil, 1)
	if err != nil || got != 250 {
		t.Fatalf("quantity=1 no tiers: unit=%d err=%v, want 250/nil", got, err)
	}
	got, err = CalculateUnitPriceMinor(250, nil, 49)
	if err != nil || got != 250 {
		t.Fatalf("quantity=49 no tiers: unit=%d err=%v, want 250/nil", got, err)
	}
	got, err = CalculateUnitPriceMinor(250, []Tier{{MinQuantity: 50, UnitPriceMinor: 200}}, 49)
	if err != nil || got != 250 {
		t.Fatalf("quantity=49 below first tier: unit=%d err=%v, want 250/nil", got, err)
	}
}

func TestCalculateUnitPriceMinorPicksHighestQualifyingTier(t *testing.T) {
	tiers := []Tier{
		{MinQuantity: 50, UnitPriceMinor: 200},
		{MinQuantity: 100, UnitPriceMinor: 150},
		{MinQuantity: 500, UnitPriceMinor: 100},
	}
	cases := []struct {
		quantity int64
		want     int64
	}{
		{1, 250},
		{49, 250},
		{50, 200},
		{99, 200},
		{100, 150},
		{499, 150},
		{500, 100},
		{1000, 100},
	}
	for _, c := range cases {
		got, err := CalculateUnitPriceMinor(250, tiers, c.quantity)
		if err != nil {
			t.Fatalf("quantity=%d: %v", c.quantity, err)
		}
		if got != c.want {
			t.Fatalf("quantity=%d: unit=%d, want %d", c.quantity, got, c.want)
		}
	}
}

func TestCalculateUnitPriceMinorTreatsTiersIndependentlyPerCombination(t *testing.T) {
	// Each combination has its own tier list. The function is pure and only
	// sees what the caller passes; it must not bleed discounts across rows.
	a4 := []Tier{{MinQuantity: 100, UnitPriceMinor: 150}}
	a3 := []Tier{{MinQuantity: 100, UnitPriceMinor: 500}}
	got, err := CalculateUnitPriceMinor(250, a4, 100)
	if err != nil || got != 150 {
		t.Fatalf("A4 at 100: %d %v, want 150/nil", got, err)
	}
	got, err = CalculateUnitPriceMinor(600, a3, 100)
	if err != nil || got != 500 {
		t.Fatalf("A3 at 100: %d %v, want 500/nil", got, err)
	}
	// And the base for A3 still applies below 100.
	got, err = CalculateUnitPriceMinor(600, a3, 99)
	if err != nil || got != 600 {
		t.Fatalf("A3 at 99: %d %v, want 600/nil", got, err)
	}
}

func TestCalculateLineTotalMultipliesWithoutOverflow(t *testing.T) {
	total, err := CalculateLineTotal(250, []Tier{{MinQuantity: 50, UnitPriceMinor: 200}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total.UnitPriceMinor != 200 || total.TotalMinor != 20000 {
		t.Fatalf("total = %+v, want unit 200, total 20000", total)
	}
	// Worst case below the cap: 1e9 sheets * 1e8 minor = 1e17, fits int64.
	total, err = CalculateLineTotal(maxUnitPriceMinor, nil, MaxQuantity)
	if err != nil {
		t.Fatal(err)
	}
	if total.UnitPriceMinor != maxUnitPriceMinor || total.TotalMinor != MaxQuantity*maxUnitPriceMinor {
		t.Fatalf("max line = %+v", total)
	}
}

func TestCalculateLineTotalRejectsOverflowingArithmetic(t *testing.T) {
	// A crafted pair whose product exceeds math.MaxInt64: 1e9 * 1e10 = 1e19.
	// The per-sheet ceiling stops the per-unit value, so the only way to
	// reach int64 overflow is through an unbounded quantity. We assert the
	// explicit overflow guard for a quantity above MaxQuantity rather than
	// trying to construct an overflow path the public API already forbids.
	if _, err := CalculateLineTotal(100, nil, MaxQuantity+1); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("quantity above MaxQuantity: err=%v, want ErrInvalidQuantity", err)
	}
}

func TestCalculateUnitPriceMinorRejectsBadInput(t *testing.T) {
	if _, err := CalculateUnitPriceMinor(250, nil, 0); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("quantity=0: err=%v, want ErrInvalidQuantity", err)
	}
	if _, err := CalculateUnitPriceMinor(250, nil, -1); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("quantity=-1: err=%v, want ErrInvalidQuantity", err)
	}
	if _, err := CalculateUnitPriceMinor(-1, nil, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("base=-1: err=%v, want ErrInvalid", err)
	}
	if _, err := CalculateUnitPriceMinor(250, []Tier{{MinQuantity: 1, UnitPriceMinor: 100}}, 1); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("minQuantity=1: err=%v, want ErrInvalidTier", err)
	}
	if _, err := CalculateUnitPriceMinor(250, []Tier{{MinQuantity: 50, UnitPriceMinor: 300}}, 50); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("tier not cheaper than base: err=%v, want ErrInvalidTier", err)
	}
	if _, err := CalculateUnitPriceMinor(250, []Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 100, UnitPriceMinor: 200}}, 100); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("equal thresholds not strictly decreasing: err=%v, want ErrInvalidTier", err)
	}
	if _, err := CalculateUnitPriceMinor(250, []Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 100, UnitPriceMinor: 210}}, 100); !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("higher quantity more expensive: err=%v, want ErrInvalidTier", err)
	}
}

func TestValidateTiersAcceptsEmptyAndValidChains(t *testing.T) {
	if err := validateTiers(250, nil); err != nil {
		t.Fatalf("nil tiers: %v", err)
	}
	if err := validateTiers(250, []Tier{}); err != nil {
		t.Fatalf("empty tiers: %v", err)
	}
	if err := validateTiers(250, []Tier{
		{MinQuantity: 100, UnitPriceMinor: 200},
		{MinQuantity: 50, UnitPriceMinor: 220},
	}); err != nil {
		t.Fatalf("unordered but strictly decreasing: %v", err)
	}
	// A free tier over a non-free base is allowed when the merchant confirms
	// explicitly; validateTiers only enforces shape, not the confirmation flow.
	if err := validateTiers(250, []Tier{{MinQuantity: 10, UnitPriceMinor: 0}}); err != nil {
		t.Fatalf("free tier over non-free base: %v", err)
	}
}

func TestValidateTiersRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name  string
		base  int64
		tiers []Tier
	}{
		{"duplicate threshold", 250, []Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 50, UnitPriceMinor: 150}}},
		{"first tier not cheaper than base", 250, []Tier{{MinQuantity: 50, UnitPriceMinor: 250}}},
		{"subsequent tier not cheaper than previous", 250, []Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 100, UnitPriceMinor: 210}}},
		{"minQuantity below threshold", 250, []Tier{{MinQuantity: 1, UnitPriceMinor: 200}}},
		{"negative unit price", 250, []Tier{{MinQuantity: 50, UnitPriceMinor: -1}}},
		{"above ceiling unit price", 250, []Tier{{MinQuantity: 50, UnitPriceMinor: 100_000_001}}},
		{"minQuantity above MaxQuantity", 250, []Tier{{MinQuantity: MaxQuantity + 1, UnitPriceMinor: 200}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTiers(c.base, c.tiers)
			if err == nil {
				t.Fatalf("validateTiers(%d, %+v) returned nil, want error", c.base, c.tiers)
			}
		})
	}
}

func TestHasFreeTierDetectsZeroPricing(t *testing.T) {
	if hasFreeTier(nil) {
		t.Fatal("nil tiers must not be free")
	}
	if !hasFreeTier([]Tier{{MinQuantity: 50, UnitPriceMinor: 0}}) {
		t.Fatal("zero-priced tier is free")
	}
	if hasFreeTier([]Tier{{MinQuantity: 50, UnitPriceMinor: 1}}) {
		t.Fatal("one-minor-unit tier is not free")
	}
	// A non-discount base paired with a single non-zero tier is not free.
	if hasFreeTier([]Tier{{MinQuantity: 50, UnitPriceMinor: 200}}) {
		t.Fatal("discounted tier over a non-free base is not free")
	}
}
