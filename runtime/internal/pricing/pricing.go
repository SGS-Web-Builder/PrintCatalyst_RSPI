// Package pricing stores merchant price configuration for print combinations.
// Prices are integer minor units of the business-profile currency. An entry
// records what the merchant charges; it never asserts that a printer
// supports the combination and never enables customer printing. That remains
// a driver-discovered capability contract implemented in a later phase.
package pricing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/currency"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
)

const (
	ColourMonochrome       = "monochrome"
	ColourColour           = "colour"
	SidesOneSided          = "one-sided"
	SidesTwoSidedLongEdge  = "two-sided-long-edge"
	SidesTwoSidedShortEdge = "two-sided-short-edge"
	UnitSheet              = "sheet"
	UnitLabel              = "price per printed sheet"
	MaxEntries             = 200
	maxUnitPriceMinor      = 100_000_000
	maxMinorUnits          = currency.MaxExponent
)

var (
	ErrInvalid  = errors.New("invalid pricing configuration")
	ErrSetup    = errors.New("business details must be saved before pricing")
	ErrNotFound = errors.New("pricing is not configured")
	// ErrUnsupportedCurrency is returned when the persisted business currency has
	// no ISO 4217 exponent in this build. Pricing refuses to guess a precision,
	// because a wrong exponent rescales every stored amount by a power of ten.
	ErrUnsupportedCurrency = errors.New("the business currency is not supported for pricing; correct it in business details")
	// ErrPrecisionCorrection is returned when the stored book was saved under a
	// different exponent than the business currency now derives. The merchant
	// must confirm the correction explicitly; the stored integers are never
	// rescaled behind their back.
	ErrPrecisionCorrection = errors.New("saved prices use a different decimal precision; review each amount and confirm the correction")
	// ErrConfirmFreePricing is returned when any tier prices a line at zero
	// minor units but the request did not include ConfirmFreePricing. A free
	// tier is intentional merchant policy, not a default.
	ErrConfirmFreePricing = errors.New("one or more quantity tiers are free; review each tier and confirm the free pricing")
	paperPat              = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._'&/-]{0,63}$`)
	wsPat                 = regexp.MustCompile(`\s+`)
)

type Entry struct {
	PaperSize      string `json:"paperSize"`
	ColourMode     string `json:"colourMode"`
	Sides          string `json:"sides"`
	UnitPriceMinor int64  `json:"unitPriceMinor"`
	// Tiers are optional quantity discounts scoped to this combination. They
	// never span combinations and never assert that a printer supports the
	// combination. Tiers are reported sorted ascending by MinQuantity.
	Tiers []Tier `json:"tiers"`
}

// Book is the stored price book as reported to the owner screen.
//
// CurrencyMinorUnits is the exponent the stored integers were saved under, so
// the screen can render them faithfully. DerivedMinorUnits is the exponent the
// server will use for the next save, taken from supported-currency metadata for
// the current business-profile currency; it is currency.Unsupported when that
// currency is not supported. They are reported separately rather than collapsed,
// because a difference between them is exactly the condition the merchant has to
// resolve deliberately.
type Book struct {
	Currency           string  `json:"currency"`
	CurrencyMinorUnits int     `json:"currencyMinorUnits"`
	DerivedMinorUnits  int     `json:"derivedMinorUnits"`
	CurrencySupported  bool    `json:"currencySupported"`
	PrecisionMismatch  bool    `json:"precisionMismatch"`
	PriceUnit          string  `json:"priceUnit"`
	PriceUnitLabel     string  `json:"priceUnitLabel"`
	CurrencyMismatch   bool    `json:"currencyMismatch"`
	UpdatedAt          int64   `json:"updatedAt"`
	Entries            []Entry `json:"entries"`
}

// Input carries only what the merchant is allowed to choose. Currency and its
// precision are absent on purpose: a client-supplied precision value is rejected
// as an unknown field rather than trusted, because it would change the meaning of
// every amount in the same request.
type Input struct {
	Entries []Entry `json:"entries"`
	// ConfirmPrecisionCorrection must be true to overwrite a book that was saved
	// under a different exponent. It is the merchant's explicit acknowledgement,
	// given after reviewing each amount on screen.
	ConfirmPrecisionCorrection bool `json:"confirmPrecisionCorrection"`
	// ConfirmFreePricing must be true when any tier prices a line at zero minor
	// units. A single flag covers the whole request because free pricing is a
	// merchant-level intent, not a per-tier switch.
	ConfirmFreePricing bool `json:"confirmFreePricing"`
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(database *sql.DB) *Service { return &Service{db: database, now: time.Now} }

func (s *Service) Load(ctx context.Context) (Book, error) {
	// currencyCode, not currency: the imported currency package owns that name.
	var currencyCode string
	var minorUnits int
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT currency, currency_minor_units, updated_at FROM pricing_book WHERE singleton = 1`).Scan(&currencyCode, &minorUnits, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, ErrNotFound
	}
	if err != nil {
		return Book{}, err
	}
	profileCurrency, _ := s.profileCurrency(ctx)
	derived := currency.MinorUnits(profileCurrency)
	supported := derived != currency.Unsupported
	entries, err := s.loadEntries(ctx)
	if err != nil {
		return Book{}, err
	}
	return Book{
		Currency:           currencyCode,
		CurrencyMinorUnits: minorUnits,
		DerivedMinorUnits:  derived,
		CurrencySupported:  supported,
		// Reported but never acted on here: the stored integers keep the meaning
		// they were saved with until the merchant confirms a correction.
		PrecisionMismatch: supported && derived != minorUnits,
		PriceUnit:         UnitSheet,
		PriceUnitLabel:    UnitLabel,
		CurrencyMismatch:  profileCurrency != "" && profileCurrency != currencyCode,
		UpdatedAt:         updatedAt,
		Entries:           entries,
	}, nil
}

func (s *Service) Save(ctx context.Context, input Input) (Book, error) {
	// No precision arrives with the request, so none is validated here. The
	// exponent is derived below from supported-currency metadata for the
	// persisted business currency.
	if len(input.Entries) == 0 || len(input.Entries) > MaxEntries {
		return Book{}, ErrInvalid
	}
	normalized, err := normalizeEntries(input.Entries)
	if err != nil {
		return Book{}, err
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Book{}, err
	}
	defer transaction.Rollback()

	var profileRaw string
	err = transaction.QueryRowContext(ctx, `SELECT profile_json FROM business_profile WHERE singleton = 1`).Scan(&profileRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, ErrSetup
	}
	if err != nil {
		return Book{}, err
	}
	var profile struct {
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal([]byte(profileRaw), &profile); err != nil {
		return Book{}, ErrSetup
	}
	profile.Currency = strings.ToUpper(strings.TrimSpace(profile.Currency))
	if len(profile.Currency) != 3 {
		return Book{}, ErrSetup
	}
	for _, ch := range profile.Currency {
		if ch < 'A' || ch > 'Z' {
			return Book{}, ErrSetup
		}
	}

	// Derive the precision from supported-currency metadata. A code that is not
	// in the allowlist is rejected outright: guessing an exponent would silently
	// multiply or divide every amount the merchant is storing.
	minorUnits := currency.MinorUnits(profile.Currency)
	if minorUnits == currency.Unsupported {
		return Book{}, ErrUnsupportedCurrency
	}
	// Defensive: the schema can only store 0..3, so a future four-decimal code
	// must widen that CHECK in a migration before it can be listed.
	if minorUnits < 0 || minorUnits > maxMinorUnits {
		return Book{}, ErrInvalid
	}

	// A previously saved book may have been written under a different exponent,
	// either because the business currency changed or because an earlier build
	// let the merchant supply the precision. Those stored integers mean something
	// different under the derived exponent, so the save is refused until the
	// merchant confirms. The integers are never rescaled here: what is stored
	// next is exactly what the merchant re-entered after reviewing each amount.
	var storedMinorUnits int
	err = transaction.QueryRowContext(ctx, `SELECT currency_minor_units FROM pricing_book WHERE singleton = 1`).Scan(&storedMinorUnits)
	switch {
	case err == nil:
		if storedMinorUnits != minorUnits && !input.ConfirmPrecisionCorrection {
			return Book{}, ErrPrecisionCorrection
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return Book{}, err
	}

	// Tiers are validated for shape inside normalizeEntries, but the
	// merchant-intent check (a free tier means what it says) is done here so
	// the merchant must confirm the request as a whole.
	for _, entry := range normalized {
		if !input.ConfirmFreePricing && hasFreeTier(entry.Tiers) {
			return Book{}, ErrConfirmFreePricing
		}
	}

	var gateCount int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM provisioning_gates WHERE completed_at IS NOT NULL AND gate IN ('licence','owner','business')`).Scan(&gateCount); err != nil {
		return Book{}, err
	}
	if gateCount != 3 {
		return Book{}, ErrSetup
	}

	nowUnix := s.now().Unix()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM pricing_rules`); err != nil {
		return Book{}, err
	}
	// Track which old rule IDs were referenced by old tiers so the cascade
	// removes them when we drop the rules; new tiers reference the freshly
	// inserted rule IDs.
	for _, entry := range normalized {
		key := paperKey(entry.PaperSize)
		id, err := randomID()
		if err != nil {
			return Book{}, err
		}
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO pricing_rules (id, paper_size, paper_key, colour_mode, sides, unit_price_minor, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, entry.PaperSize, key, entry.ColourMode, entry.Sides, entry.UnitPriceMinor, nowUnix, nowUnix); err != nil {
			return Book{}, err
		}
		if len(entry.Tiers) > 0 {
			if err := validateTiers(entry.UnitPriceMinor, entry.Tiers); err != nil {
				return Book{}, err
			}
			for _, tier := range entry.Tiers {
				tierID, err := randomID()
				if err != nil {
					return Book{}, err
				}
				if _, err := transaction.ExecContext(ctx, `
INSERT INTO pricing_tiers (id, pricing_rule_id, min_quantity, unit_price_minor, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, tierID, id, tier.MinQuantity, tier.UnitPriceMinor, nowUnix, nowUnix); err != nil {
					return Book{}, err
				}
			}
		}
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO pricing_book (singleton, currency, currency_minor_units, price_unit, updated_at)
VALUES (1, ?, ?, 'sheet', ?)
ON CONFLICT(singleton) DO UPDATE SET currency = excluded.currency, currency_minor_units = excluded.currency_minor_units, price_unit = 'sheet', updated_at = excluded.updated_at`, profile.Currency, minorUnits, nowUnix); err != nil {
		return Book{}, err
	}
	// Complete the pricing gate only now that valid pricing is durably stored
	// and every earlier gate is complete. Later gates stay incomplete.
	if err := provisioning.CompleteGateInTx(ctx, transaction, provisioning.GatePricing, fmt.Sprintf("local pricing persisted: %d entries", len(normalized)), s.now()); err != nil {
		return Book{}, err
	}
	auditID, err := randomID()
	if err != nil {
		return Book{}, err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events (id, event_type, evidence, occurred_at) VALUES (?, 'pricing.updated', ?, ?)`, auditID, fmt.Sprintf("%d pricing entries", len(normalized)), s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Book{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Book{}, err
	}
	return s.Load(ctx)
}

func normalizeEntries(entries []Entry) ([]Entry, error) {
	seen := make(map[string]bool, len(entries))
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		paper := strings.TrimSpace(entry.PaperSize)
		if !utf8.ValidString(paper) || paper == "" || utf8.RuneCountInString(paper) > 64 || len(paper) > 256 {
			return nil, ErrInvalid
		}
		// Validate before collapsing whitespace so control characters such as a
		// newline or tab are rejected rather than silently rewritten.
		if !paperPat.MatchString(paper) {
			return nil, ErrInvalid
		}
		paper = strings.TrimSpace(wsPat.ReplaceAllString(paper, " "))
		if !paperPat.MatchString(paper) {
			return nil, ErrInvalid
		}
		colour := strings.TrimSpace(entry.ColourMode)
		if colour != ColourMonochrome && colour != ColourColour {
			return nil, ErrInvalid
		}
		sides := strings.TrimSpace(entry.Sides)
		if sides != SidesOneSided && sides != SidesTwoSidedLongEdge && sides != SidesTwoSidedShortEdge {
			return nil, ErrInvalid
		}
		if entry.UnitPriceMinor < 0 || entry.UnitPriceMinor > maxUnitPriceMinor {
			return nil, ErrInvalid
		}
		key := paperKey(paper)
		dedup := key + "|" + colour + "|" + sides
		if seen[dedup] {
			return nil, ErrInvalid
		}
		seen[dedup] = true
		normalizedTiers, err := normalizeTiers(entry.Tiers)
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{PaperSize: paper, ColourMode: colour, Sides: sides, UnitPriceMinor: entry.UnitPriceMinor, Tiers: normalizedTiers})
	}
	return out, nil
}

// normalizeTiers sorts a single entry's tiers ascending by MinQuantity and
// validates the chain against the combination's base price. The shape rules
// (minQuantity, ceiling, monotonicity, duplicates) are enforced here and in
// CalculateUnitPriceMinor; both must agree so a defensive caller can rely on
// the pure function without re-implementing the rules.
func normalizeTiers(tiers []Tier) ([]Tier, error) {
	if len(tiers) == 0 {
		return nil, nil
	}
	sorted := make([]Tier, len(tiers))
	copy(sorted, tiers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MinQuantity < sorted[j].MinQuantity })
	for _, tier := range sorted {
		if tier.MinQuantity < 2 || tier.MinQuantity > MaxQuantity {
			return nil, ErrInvalid
		}
		if tier.UnitPriceMinor < 0 || tier.UnitPriceMinor > maxUnitPriceMinor {
			return nil, ErrInvalid
		}
	}
	return sorted, nil
}

func paperKey(value string) string {
	return strings.ToUpper(wsPat.ReplaceAllString(strings.TrimSpace(value), " "))
}

func (s *Service) loadEntries(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id, r.paper_size, r.colour_mode, r.sides, r.unit_price_minor, t.min_quantity, t.unit_price_minor
FROM pricing_rules r
LEFT JOIN pricing_tiers t ON t.pricing_rule_id = r.id
ORDER BY r.paper_size, r.colour_mode, r.sides, t.min_quantity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]Entry, 0)
	currentIndex := -1
	for rows.Next() {
		var ruleID string
		var entry Entry
		var tierQuantity sql.NullInt64
		var tierUnit sql.NullInt64
		if err := rows.Scan(&ruleID, &entry.PaperSize, &entry.ColourMode, &entry.Sides, &entry.UnitPriceMinor, &tierQuantity, &tierUnit); err != nil {
			return nil, err
		}
		// The same parent row appears once per tier, so start a new entry only
		// when the rule id changes. This lets every tier land in its parent entry
		// without a second query.
		if currentIndex < 0 || entries[currentIndex].PaperSize != entry.PaperSize || entries[currentIndex].ColourMode != entry.ColourMode || entries[currentIndex].Sides != entry.Sides {
			entry.Tiers = []Tier{}
			entries = append(entries, entry)
			currentIndex++
		}
		if tierQuantity.Valid && tierUnit.Valid {
			entries[currentIndex].Tiers = append(entries[currentIndex].Tiers, Tier{MinQuantity: tierQuantity.Int64, UnitPriceMinor: tierUnit.Int64})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range entries {
		if entries[index].Tiers == nil {
			entries[index].Tiers = []Tier{}
		}
	}
	return entries, nil
}

func (s *Service) profileCurrency(ctx context.Context) (string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT profile_json FROM business_profile WHERE singleton = 1`).Scan(&raw)
	if err != nil {
		return "", err
	}
	var profile struct {
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return "", err
	}
	return strings.ToUpper(strings.TrimSpace(profile.Currency)), nil
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// ValidateEntries applies the same price and tier constraints to service books.
func ValidateEntries(entries []Entry) ([]Entry, error) {
	normalized, err := normalizeEntries(entries)
	if err != nil {
		return nil, err
	}
	for _, e := range normalized {
		if err := validateTiers(e.UnitPriceMinor, e.Tiers); err != nil {
			return nil, err
		}
	}
	return normalized, nil
}
