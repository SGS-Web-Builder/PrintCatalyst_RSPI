package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/currency"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// Effective applies a printer-specific book over shop-wide defaults.
func (s *Service) Effective(ctx context.Context, id string) (Book, error) {
	book, err := s.Load(ctx)
	if err != nil {
		return book, err
	}
	if id == "" {
		return book, nil
	}
	var code, raw string
	var mu int
	err = s.db.QueryRowContext(ctx, "SELECT currency,minor_units,entries_json FROM printer_price_books WHERE printer_id=?", id).Scan(&code, &mu, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return book, nil
	}
	if err != nil {
		return book, err
	}
	if code != book.Currency || mu != book.CurrencyMinorUnits {
		return book, fmt.Errorf("printer prices need review after currency change")
	}
	var entries []Entry
	if err = json.Unmarshal([]byte(raw), &entries); err != nil {
		return book, err
	}
	indexes := map[string]int{}
	for i, e := range book.Entries {
		indexes[entryKey(e)] = i
	}
	for _, e := range entries {
		if i, ok := indexes[entryKey(e)]; ok {
			book.Entries[i] = e
		} else {
			book.Entries = append(book.Entries, e)
		}
	}
	return book, nil
}
func entryKey(e Entry) string { return paperKey(e.PaperSize) + "|" + e.ColourMode + "|" + e.Sides }
func (s *Service) HasPrinterPrices(ctx context.Context) (bool, error) {
	var yes bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM printer_price_books b JOIN printers p ON p.id=b.printer_id WHERE p.enabled=1 AND p.removed_at IS NULL)`).Scan(&yes)
	return yes, err
}
func (s *Service) GridBook(ctx context.Context, id string) (Book, error) {
	book, err := s.Load(ctx)
	if errors.Is(err, ErrNotFound) {
		code, e := s.profileCurrency(ctx)
		if e != nil {
			return book, e
		}
		mu := currency.MinorUnits(code)
		book = Book{Currency: code, CurrencyMinorUnits: mu, DerivedMinorUnits: mu, CurrencySupported: mu >= 0, Entries: []Entry{}, PriceUnit: UnitSheet, PriceUnitLabel: UnitLabel}
		err = nil
	}
	if err != nil {
		return book, err
	}
	if id == "" {
		return book, nil
	}
	// Show just overrides in the editor; blank rows inherit the shared rate.
	var raw string
	var code string
	var mu int
	err = s.db.QueryRowContext(ctx, "SELECT currency,minor_units,entries_json FROM printer_price_books WHERE printer_id=?", id).Scan(&code, &mu, &raw)
	book.Entries = []Entry{}
	if errors.Is(err, sql.ErrNoRows) {
		return book, nil
	}
	if err != nil {
		return book, err
	}
	book.CurrencyMismatch = code != book.Currency
	book.PrecisionMismatch = mu != book.DerivedMinorUnits
	book.Currency = code
	book.CurrencyMinorUnits = mu
	err = json.Unmarshal([]byte(raw), &book.Entries)
	return book, err
}
func (s *Service) SaveGrid(ctx context.Context, id string, input Input) (Book, error) {
	fleet, err := printers.Eligible(ctx, s.db, "", id)
	if err != nil {
		return Book{}, err
	}
	entries, err := normalizeEntries(input.Entries)
	if err != nil {
		return Book{}, err
	}
	if len(entries) > MaxEntries {
		return Book{}, ErrInvalid
	}
	for _, e := range entries {
		supported := false
		for _, p := range fleet {
			if p.Supports(e.PaperSize, e.ColourMode, e.Sides) {
				supported = true
				break
			}
		}
		if !supported {
			return Book{}, fmt.Errorf("%w: enable a printer and paper size supporting %s / %s / %s", ErrInvalid, e.PaperSize, e.ColourMode, e.Sides)
		}
		if err = validateTiers(e.UnitPriceMinor, e.Tiers); err != nil {
			return Book{}, err
		}
		if !input.ConfirmFreePricing && (e.UnitPriceMinor == 0 || hasFreeTier(e.Tiers)) {
			return Book{}, ErrConfirmFreePricing
		}
	}
	if id == "" {
		return s.Save(ctx, input)
	}
	if len(fleet) == 0 {
		return Book{}, fmt.Errorf("%w: selected printer has no enabled paper sizes", ErrInvalid)
	}
	book, err := s.Load(ctx)
	if err != nil {
		return book, err
	}
	if book.CurrencyMismatch || book.PrecisionMismatch || !book.CurrencySupported {
		return book, fmt.Errorf("review and save shared prices before printer prices")
	}
	old, err := s.GridBook(ctx, id)
	if err != nil {
		return book, err
	}
	if old.PrecisionMismatch && !input.ConfirmPrecisionCorrection {
		return book, ErrPrecisionCorrection
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return book, err
	}
	if len(entries) == 0 {
		_, err = s.db.ExecContext(ctx, "DELETE FROM printer_price_books WHERE printer_id=?", id)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO printer_price_books(printer_id,currency,minor_units,entries_json,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(printer_id) DO UPDATE SET currency=excluded.currency,minor_units=excluded.minor_units,entries_json=excluded.entries_json,updated_at=excluded.updated_at`, id, book.Currency, book.CurrencyMinorUnits, string(raw), s.now().Unix())
	}
	if err != nil {
		return book, err
	}
	return s.GridBook(ctx, id)
}
