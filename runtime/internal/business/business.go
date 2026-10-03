// Package business owns the merchant-facing business configuration that
// is not a payment provider or a license: the print policy, the shop
// identity, the service catalogue and the discount codes. The single
// Service struct is the constructor for every dashboard panel in the
// "Business" tab; each public method is a thin wrapper over the local
// SQLite store. No network calls, no payment-provider calls.
package business

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"strings"
	"sync"
	"time"
)

// Sentinel errors the HTTP layer maps to HTTP status codes.
var (
	ErrInvalid    = errors.New("invalid business settings")
	ErrNotFound   = errors.New("business settings not configured")
	ErrDuplicate  = errors.New("a record with the same key already exists")
	ErrTransition = errors.New("requested change violates a state rule")
)

// AutoPrintMode is the merchant's chosen auto-print policy. The runtime
// translates the value into a dispatch decision on every order
// transition:
//
//   off                  — never auto-dispatch; the merchant hits Print
//                          from the dashboard.
//   on_payment_captured  — dispatch the moment the intent reaches
//                          StatusCaptured. The documented default.
//   all_documents        — dispatch every line as soon as the order is
//                          submitted (suitable for counter-only kiosks).
type AutoPrintMode string

const (
	AutoPrintOff               AutoPrintMode = "off"
	AutoPrintOnPaymentCaptured AutoPrintMode = "on_payment_captured"
	AutoPrintAllDocuments      AutoPrintMode = "all_documents"
)

func (m AutoPrintMode) Valid() bool {
	switch m {
	case AutoPrintOff, AutoPrintOnPaymentCaptured, AutoPrintAllDocuments:
		return true
	}
	return false
}

// Settings is the persisted business_settings row.
type Settings struct {
	ShopName           string        `json:"shopName"`
	Address            string        `json:"address"`
	GSTIN              string        `json:"gstin"`
	Phone              string        `json:"phone"`
	Email              string        `json:"email"`
	ReceiptFooter      string        `json:"receiptFooter"`
	LogoPath           string        `json:"logoPath"`
	AutoPrintMode      AutoPrintMode `json:"autoPrintMode"`
	PrimaryPrinterID   string        `json:"primaryPrinterId"`
	AutoDeleteEnabled  bool          `json:"autoDeleteEnabled"`
	AutoDeleteMinutes  int           `json:"autoDeleteMinutes"`
	CashOnCounterLabel string        `json:"cashOnCounterLabel"`
	RazorpayLabel      string        `json:"razorpayLabel"`
	UpdatedAt          int64         `json:"updatedAt"`
}

// Service is the business settings service. The constructor takes the
// shared SQLite handle; the runtime wires it into localserver the same
// way it wires pricing or notifications.
type Service struct {
	db  *sql.DB
	now func() time.Time
	mu  sync.Mutex
}

// New constructs a Service. The clock is overridable so tests can pin
// UpdatedAt to a deterministic value.
func New(database *sql.DB) *Service {
	return &Service{db: database, now: time.Now}
}

// WithClock overrides the clock; tests use this to assert on timestamps.
func (s *Service) WithClock(now func() time.Time) {
	s.now = now
}

// Get returns the current business settings. The row is seeded by the
// migration so Get always succeeds after the database is opened.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT shop_name, address, gstin, phone, email, receipt_footer, logo_path,
       auto_print_mode, primary_printer_id, auto_delete_enabled, auto_delete_minutes,
       cash_on_counter_label, razorpay_label, updated_at
FROM business_settings WHERE singleton = 1`)
	var settings Settings
	var autoPrint string
	var autoDelete int
	if err := row.Scan(
		&settings.ShopName, &settings.Address, &settings.GSTIN, &settings.Phone, &settings.Email,
		&settings.ReceiptFooter, &settings.LogoPath,
		&autoPrint, &settings.PrimaryPrinterID, &autoDelete, &settings.AutoDeleteMinutes,
		&settings.CashOnCounterLabel, &settings.RazorpayLabel, &settings.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Settings{}, ErrNotFound
		}
		return Settings{}, fmt.Errorf("read business settings: %w", err)
	}
	settings.AutoPrintMode = AutoPrintMode(autoPrint)
	settings.AutoDeleteEnabled = autoDelete == 1
	return settings, nil
}

// UpdateSettingsInput is the partial-update payload. Empty strings are
// preserved as-is (the dashboard uses a separate "clear" checkbox
// rather than empty-string-as-clear); only AutoPrintMode is replaced
// unconditionally.
type UpdateSettingsInput struct {
	ShopName           string        `json:"shopName"`
	Address            string        `json:"address"`
	GSTIN              string        `json:"gstin"`
	Phone              string        `json:"phone"`
	Email              string        `json:"email"`
	ReceiptFooter      string        `json:"receiptFooter"`
	LogoPath           string        `json:"logoPath"`
	AutoPrintMode      AutoPrintMode `json:"autoPrintMode"`
	PrimaryPrinterID   string        `json:"primaryPrinterId"`
	AutoDeleteEnabled  *bool         `json:"autoDeleteEnabled"`
	AutoDeleteMinutes  int           `json:"autoDeleteMinutes"`
	CashOnCounterLabel string        `json:"cashOnCounterLabel"`
	RazorpayLabel      string        `json:"razorpayLabel"`
}

// UpdateSettings replaces the singleton row. Validation is deliberately
// strict because the settings drive dispatch behaviour: a typo here
// would silently fail to print paid orders.
func (s *Service) UpdateSettings(ctx context.Context, input UpdateSettingsInput) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !input.AutoPrintMode.Valid() {
		return Settings{}, fmt.Errorf("%w: autoPrintMode must be off, on_payment_captured, or all_documents", ErrInvalid)
	}
	if input.AutoDeleteMinutes < 1 || input.AutoDeleteMinutes > 24*60*30 {
		return Settings{}, fmt.Errorf("%w: autoDeleteMinutes must be between 1 and 43200 (30 days)", ErrInvalid)
	}
	if input.CashOnCounterLabel == "" {
		input.CashOnCounterLabel = "Cash on Counter"
	}
	if input.RazorpayLabel == "" {
		input.RazorpayLabel = "Pay Online (Razorpay)"
	}
	autoDelete := 0
	if input.AutoDeleteEnabled != nil && *input.AutoDeleteEnabled {
		autoDelete = 1
	}
	now := s.now().Unix()
	if _, err := s.db.ExecContext(ctx, `
UPDATE business_settings SET
    shop_name=?, address=?, gstin=?, phone=?, email=?,
    receipt_footer=?, logo_path=?,
    auto_print_mode=?, primary_printer_id=?,
    auto_delete_enabled=?, auto_delete_minutes=?,
    cash_on_counter_label=?, razorpay_label=?,
    updated_at=?
WHERE singleton=1`,
		strings.TrimSpace(input.ShopName), strings.TrimSpace(input.Address),
		strings.TrimSpace(input.GSTIN), strings.TrimSpace(input.Phone), strings.TrimSpace(input.Email),
		strings.TrimSpace(input.ReceiptFooter), strings.TrimSpace(input.LogoPath),
		string(input.AutoPrintMode), strings.TrimSpace(input.PrimaryPrinterID),
		autoDelete, input.AutoDeleteMinutes,
		strings.TrimSpace(input.CashOnCounterLabel), strings.TrimSpace(input.RazorpayLabel),
		now); err != nil {
		return Settings{}, fmt.Errorf("update business settings: %w", err)
	}
	return s.Get(ctx)
}

// ---------- Service catalogue ----------

// ServiceRecord is one row in the services table.
type ServiceRecord struct {
	PricingCurrency   string          `json:"pricingCurrency"`
	PricingMinorUnits int             `json:"pricingMinorUnits"`
	ID                string          `json:"id"`
	Code              string          `json:"code"`
	DisplayName       string          `json:"displayName"`
	Description       string          `json:"description"`
	Enabled           bool            `json:"enabled"`
	PriceBookID       string          `json:"priceBookId"`
	Pricing           []pricing.Entry `json:"pricing"`
	PrinterIDs        []string        `json:"printerIds"`
	CreatedAt         int64           `json:"createdAt"`
	UpdatedAt         int64           `json:"updatedAt"`
}

// CreateServiceInput is the new-row payload.
type CreateServiceInput struct {
	Code        string          `json:"code"`
	DisplayName string          `json:"displayName"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	PriceBookID string          `json:"priceBookId"`
	Pricing     []pricing.Entry `json:"pricing"`
	PrinterIDs  []string        `json:"printerIds"`
}

// CreateService persists a new service row plus the (service, printer)
// assignment rows.
func (s *Service) CreateService(ctx context.Context, input CreateServiceInput) (ServiceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := strings.TrimSpace(input.Code)
	if code == "" || strings.TrimSpace(input.DisplayName) == "" {
		return ServiceRecord{}, fmt.Errorf("%w: code and displayName are required", ErrInvalid)
	}
	var entries []pricing.Entry
	if len(input.Pricing) > 0 {
		var err error
		entries, err = pricing.ValidateEntries(input.Pricing)
		if err != nil {
			return ServiceRecord{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	rawPricing, _ := json.Marshal(entries)
	id := randomID()
	now := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceRecord{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO services (id, code, display_name, description, enabled, price_book_id, created_at, updated_at, pricing_json,pricing_currency,pricing_minor_units)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT currency FROM pricing_book WHERE singleton=1),''),COALESCE((SELECT currency_minor_units FROM pricing_book WHERE singleton=1),0))`,
		id, code, strings.TrimSpace(input.DisplayName),
		strings.TrimSpace(input.Description), boolToInt(input.Enabled),
		strings.TrimSpace(input.PriceBookID), now, now, string(rawPricing))
	if err != nil {
		if isUniqueViolation(err) {
			return ServiceRecord{}, ErrDuplicate
		}
		return ServiceRecord{}, fmt.Errorf("insert service: %w", err)
	}
	if err := upsertServicePrinters(ctx, tx, id, input.PrinterIDs, now); err != nil {
		return ServiceRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ServiceRecord{}, fmt.Errorf("commit service: %w", err)
	}
	return s.loadService(ctx, id)
}

// UpdateServiceInput is the partial-update payload. The printer list
// is replaced atomically: missing printers are unassigned.
type UpdateServiceInput struct {
	DisplayName string
	Description string
	Enabled     *bool
	PriceBookID string
	Pricing     *[]pricing.Entry `json:"pricing"`
	PrinterIDs  []string
}

// UpdateService replaces the editable columns of a service row plus
// the printer assignment rows.
func (s *Service) UpdateService(ctx context.Context, id string, input UpdateServiceInput) (ServiceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return ServiceRecord{}, ErrInvalid
	}
	now := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceRecord{}, err
	}
	defer tx.Rollback()
	enabledExpr := "enabled"
	args := []any{strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Description), strings.TrimSpace(input.PriceBookID)}
	if input.Enabled != nil {
		enabledExpr = "?"
		args = append(args, boolToInt(*input.Enabled))
	}
	args = append(args, now, id)
	if _, err := tx.ExecContext(ctx, "UPDATE services SET display_name=?, description=?, price_book_id=?, enabled="+enabledExpr+", updated_at=? WHERE id=?", args...); err != nil {
		return ServiceRecord{}, fmt.Errorf("update service: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_printers WHERE service_id = ?`, id); err != nil {
		return ServiceRecord{}, err
	}
	if input.Pricing != nil {
		entries := *input.Pricing
		if len(entries) > 0 {
			var err error
			entries, err = pricing.ValidateEntries(entries)
			if err != nil {
				return ServiceRecord{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
		}
		raw, _ := json.Marshal(entries)
		if _, err := tx.ExecContext(ctx, "UPDATE services SET pricing_json=?,pricing_currency=COALESCE((SELECT currency FROM pricing_book WHERE singleton=1),''),pricing_minor_units=COALESCE((SELECT currency_minor_units FROM pricing_book WHERE singleton=1),0) WHERE id=?", string(raw), id); err != nil {
			return ServiceRecord{}, err
		}
	}
	if err := upsertServicePrinters(ctx, tx, id, input.PrinterIDs, now); err != nil {
		return ServiceRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ServiceRecord{}, err
	}
	return s.loadService(ctx, id)
}

// DeleteService removes the service and cascades to service_printers
// (the FK action was declared CASCADE in migration 020).
func (s *Service) DeleteService(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// ListServices returns every service row with its assigned printers.
func (s *Service) ListServices(ctx context.Context) ([]ServiceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM services ORDER BY display_name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ServiceRecord, 0, len(ids))
	for _, id := range ids {
		svc, err := s.loadService(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, nil
}

func (s *Service) loadService(ctx context.Context, id string) (ServiceRecord, error) {
	var svc ServiceRecord
	var rawPricing string
	var enabled int
	row := s.db.QueryRowContext(ctx, `
SELECT id, code, display_name, description, enabled, price_book_id, created_at, updated_at, pricing_json,pricing_currency,pricing_minor_units
FROM services WHERE id = ?`, id)
	if err := row.Scan(&svc.ID, &svc.Code, &svc.DisplayName, &svc.Description, &enabled, &svc.PriceBookID, &svc.CreatedAt, &svc.UpdatedAt, &rawPricing, &svc.PricingCurrency, &svc.PricingMinorUnits); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return svc, ErrNotFound
		}
		return svc, fmt.Errorf("read service: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPricing), &svc.Pricing); err != nil {
		return svc, err
	}
	svc.Enabled = enabled == 1
	printerRows, err := s.db.QueryContext(ctx, `SELECT printer_id FROM service_printers WHERE service_id = ? ORDER BY printer_id`, id)
	if err != nil {
		return svc, fmt.Errorf("list service printers: %w", err)
	}
	defer printerRows.Close()
	for printerRows.Next() {
		var pid string
		if err := printerRows.Scan(&pid); err != nil {
			return svc, err
		}
		svc.PrinterIDs = append(svc.PrinterIDs, pid)
	}
	return svc, printerRows.Err()
}

func upsertServicePrinters(ctx context.Context, tx *sql.Tx, serviceID string, printerIDs []string, now int64) error {
	// Deduplicate printer ids to avoid PK collisions on the (service_id,
	// printer_id) pair.
	seen := make(map[string]bool, len(printerIDs))
	for _, pid := range printerIDs {
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		if _, err := tx.ExecContext(ctx, `
INSERT INTO service_printers (service_id, printer_id, created_at) VALUES (?, ?, ?)`,
			serviceID, pid, now); err != nil {
			return fmt.Errorf("insert service_printer: %w", err)
		}
	}
	return nil
}

// ---------- Discounts ----------

// Discount is one discount row.
type Discount struct {
	ID            string `json:"id"`
	Code          string `json:"code"`
	DisplayName   string `json:"displayName"`
	Type          string `json:"type"` // "percent" or "flat"
	Value         int64  `json:"value"`
	MinOrderMinor int64  `json:"minOrderMinor"`
	MaxUses       int    `json:"maxUses"`
	TimesUsed     int    `json:"timesUsed"`
	ValidFrom     int64  `json:"validFrom"`
	ValidUntil    int64  `json:"validUntil"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// CreateDiscountInput is the new-row payload.
type CreateDiscountInput struct {
	Code          string `json:"code"`
	DisplayName   string `json:"displayName"`
	Type          string `json:"type"`
	Value         int64  `json:"value"`
	MinOrderMinor int64  `json:"minOrderMinor"`
	MaxUses       int    `json:"maxUses"`
	ValidFrom     int64  `json:"validFrom"`
	ValidUntil    int64  `json:"validUntil"`
	Enabled       bool   `json:"enabled"`
}

// CreateDiscount persists a new discount row.
func (s *Service) CreateDiscount(ctx context.Context, input CreateDiscountInput) (Discount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDiscountType(input.Type) {
		return Discount{}, fmt.Errorf("%w: type must be percent or flat", ErrInvalid)
	}
	if input.Value <= 0 || (input.Type == "percent" && input.Value > 100) || input.Value > 100_000_000 || input.MinOrderMinor < 0 || input.MinOrderMinor > 100_000_000 || input.MaxUses < 0 || input.ValidFrom < 0 || input.ValidUntil < 0 || (input.ValidUntil > 0 && input.ValidFrom > input.ValidUntil) {
		return Discount{}, fmt.Errorf("%w: value must be positive", ErrInvalid)
	}
	if strings.TrimSpace(input.Code) == "" {
		return Discount{}, fmt.Errorf("%w: code is required", ErrInvalid)
	}
	id := randomID()
	now := s.now().Unix()
	var err error
	_, err = s.db.ExecContext(ctx, `
INSERT INTO discounts (id, code, display_name, discount_type, discount_value, min_order_minor,
	max_uses, times_used, valid_from, valid_until, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		id, strings.ToUpper(strings.TrimSpace(input.Code)), strings.TrimSpace(input.DisplayName),
		input.Type, input.Value, input.MinOrderMinor, input.MaxUses,
		input.ValidFrom, input.ValidUntil, boolToInt(input.Enabled), now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return Discount{}, ErrDuplicate
		}
		return Discount{}, fmt.Errorf("insert discount: %w", err)
	}
	return s.loadDiscount(ctx, id)
}

// UpdateDiscountInput is the partial-update payload.
type UpdateDiscountInput struct {
	DisplayName   string
	Type          string
	Value         int64
	MinOrderMinor int64
	MaxUses       int
	ValidFrom     int64
	ValidUntil    int64
	Enabled       *bool
}

// UpdateDiscount replaces the editable columns.
func (s *Service) UpdateDiscount(ctx context.Context, id string, input UpdateDiscountInput) (Discount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return Discount{}, ErrInvalid
	}
	if !validDiscountType(input.Type) {
		return Discount{}, fmt.Errorf("%w: type must be percent or flat", ErrInvalid)
	}
	if input.Value <= 0 || (input.Type == "percent" && input.Value > 100) || input.Value > 100_000_000 || input.MinOrderMinor < 0 || input.MinOrderMinor > 100_000_000 || input.MaxUses < 0 || input.ValidFrom < 0 || input.ValidUntil < 0 || (input.ValidUntil > 0 && input.ValidFrom > input.ValidUntil) {
		return Discount{}, fmt.Errorf("%w: value must be positive", ErrInvalid)
	}
	enabled := 1
	if input.Enabled != nil && !*input.Enabled {
		enabled = 0
	}
	now := s.now().Unix()
	if _, err := s.db.ExecContext(ctx, `
UPDATE discounts SET display_name=?, discount_type=?, discount_value=?, min_order_minor=?,
	max_uses=?, valid_from=?, valid_until=?, enabled=?, updated_at=? WHERE id=?`,
		strings.TrimSpace(input.DisplayName), input.Type, input.Value, input.MinOrderMinor,
		input.MaxUses, input.ValidFrom, input.ValidUntil, enabled, now, id); err != nil {
		return Discount{}, fmt.Errorf("update discount: %w", err)
	}
	return s.loadDiscount(ctx, id)
}

// DeleteDiscount removes the discount row.
func (s *Service) DeleteDiscount(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM discounts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete discount: %w", err)
	}
	return nil
}

// ListDiscounts returns every discount row.
func (s *Service) ListDiscounts(ctx context.Context) ([]Discount, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM discounts ORDER BY code ASC`)
	if err != nil {
		return nil, fmt.Errorf("list discounts: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Discount, 0, len(ids))
	for _, id := range ids {
		d, err := s.loadDiscount(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) loadDiscount(ctx context.Context, id string) (Discount, error) {
	var d Discount
	var typ string
	var enabled int
	row := s.db.QueryRowContext(ctx, `
SELECT id, code, display_name, discount_type, discount_value, min_order_minor,
	max_uses, times_used, valid_from, valid_until, enabled, created_at, updated_at
FROM discounts WHERE id = ?`, id)
	if err := row.Scan(&d.ID, &d.Code, &d.DisplayName, &typ, &d.Value, &d.MinOrderMinor,
		&d.MaxUses, &d.TimesUsed, &d.ValidFrom, &d.ValidUntil, &enabled, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return d, ErrNotFound
		}
		return d, fmt.Errorf("read discount: %w", err)
	}
	d.Type = typ
	d.Enabled = enabled == 1
	return d, nil
}

// ApplyDiscount validates a discount code against the order total and
// returns the minor-unit discount to subtract. The check enforces
// enabled, valid_from, valid_until, max_uses, and min_order_minor in
// one place so the runtime never has to repeat the validation at every
// call site.
func (s *Service) ApplyDiscount(ctx context.Context, code string, orderTotalMinor int64, now int64) (int64, Discount, error) {
	return DiscountFor(ctx, s.db, code, orderTotalMinor, now)
}

type discountReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// DiscountFor can run in the order transaction, closing the max-use race.
func DiscountFor(ctx context.Context, reader discountReader, code string, orderTotalMinor, now int64) (int64, Discount, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return 0, Discount{}, nil
	}
	row := reader.QueryRowContext(ctx, `
SELECT id, code, display_name, discount_type, discount_value, min_order_minor,
	max_uses, times_used, valid_from, valid_until, enabled
FROM discounts WHERE code = ?`, code)
	var d Discount
	var typ string
	var enabled int
	if err := row.Scan(&d.ID, &d.Code, &d.DisplayName, &typ, &d.Value, &d.MinOrderMinor,
		&d.MaxUses, &d.TimesUsed, &d.ValidFrom, &d.ValidUntil, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, Discount{}, ErrNotFound
		}
		return 0, Discount{}, err
	}
	d.Type = typ
	d.Enabled = enabled == 1
	if !d.Enabled {
		return 0, d, fmt.Errorf("%w: discount is disabled", ErrTransition)
	}
	if d.ValidFrom > 0 && now < d.ValidFrom {
		return 0, d, fmt.Errorf("%w: discount is not yet active", ErrTransition)
	}
	if d.ValidUntil > 0 && now > d.ValidUntil {
		return 0, d, fmt.Errorf("%w: discount has expired", ErrTransition)
	}
	if d.MaxUses > 0 && d.TimesUsed >= d.MaxUses {
		return 0, d, fmt.Errorf("%w: discount usage limit reached", ErrTransition)
	}
	if d.MinOrderMinor > 0 && orderTotalMinor < d.MinOrderMinor {
		return 0, d, fmt.Errorf("%w: order total is below minimum", ErrTransition)
	}
	var discount int64
	switch d.Type {
	case "percent":
		discount = orderTotalMinor * d.Value / 100
	case "flat":
		discount = d.Value
	}
	if discount > orderTotalMinor {
		discount = orderTotalMinor
	}
	return discount, d, nil
}

// RecordDiscountUse increments the times_used counter after a successful
// order submission.
func (s *Service) RecordDiscountUse(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `UPDATE discounts SET times_used = times_used + 1, updated_at = ? WHERE id = ?`,
		s.now().Unix(), id); err != nil {
		return fmt.Errorf("record discount use: %w", err)
	}
	return nil
}

// ---------- helpers ----------

func validDiscountType(t string) bool {
	return t == "percent" || t == "flat"
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("random id: %v", err))
	}
	return hex.EncodeToString(buf)
}
