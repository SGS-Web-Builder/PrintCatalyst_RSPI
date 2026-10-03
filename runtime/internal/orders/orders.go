// Package orders stores customer print orders and computes authoritative quotes
// server-side from the persisted pricing book and per-document page counts.
// A client-submitted total is never trusted: every line total is derived from
// sheets × unit_price_minor inside the service.
package orders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"regexp"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/currency"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
)

const (
	StatusPendingPayment = "pending_payment"
	StatusPaid           = "paid"
	StatusDispatched     = "dispatched"
	StatusCompleted      = "completed"
	StatusFailed         = "failed"
	StatusCancelled      = "cancelled"
)

// Order is a customer print order. Currency and its exponent are snapshotted at
// creation so existing orders are never affected by later pricing or currency
// corrections.
type Order struct {
	SubtotalMinor int64
	DiscountMinor int64
	DiscountCode  string
	ID            string
	ShareToken    string
	Status        string
	PaymentMethod string // "razorpay" or "cash_on_counter" — captured at order creation
	Currency      string
	CurrencyMU    int   // minor units exponent
	TotalMinor    int64 // total in minor units
	CustomerName  string
	CustomerPhone string
	CustomerEmail string
	CustomerNotes string
	CreatedAt     int64
	UpdatedAt     int64
	Lines         []OrderLine
	// MergeLines tells the dispatcher to compose every line of this
	// order into one print job. When false the dispatcher submits one
	// job per line (the historical behaviour).
	MergeLines bool
	// PrimaryPrinterID, when non-empty, asks the dispatcher to send
	// this order's bytes to a specific printer instead of using the
	// merchant's default.
	PrimaryPrinterID string
}

// OrderLine is one print configuration for one uploaded document.
type OrderLine struct {
	PagesJSON      string
	ID             string
	OrderID        string
	DocumentID     string
	PaperSize      string
	PaperKey       string
	ColourMode     string
	Sides          string
	Copies         int
	PageRangeStart int
	PageRangeEnd   int
	Orientation    string // "auto" / "portrait" / "landscape"
	PagesPerSheet  int    // 1, 2 or 4
	UnitPriceMinor int64
	LineTotalMinor int64
}

// QuoteRequest carries the customer's requested print configuration for one file.
type QuoteRequest struct {
	ServiceID    string             `json:"serviceId"`
	DiscountCode string             `json:"discountCode"`
	Lines        []QuoteLineRequest `json:"lines"`
	// MergeLines lets the customer ask the dispatcher to compose every
	// line into one print job. Defaults to false (legacy behaviour).
	MergeLines bool `json:"mergeLines"`
	// PrimaryPrinterID, when set, asks the dispatcher to print this
	// order on a specific printer. Empty means "use the merchant's
	// default".
	PrimaryPrinterID string `json:"primaryPrinterId,omitempty"`
}

// Orientation values recognised by the order service. "auto" lets the
// runtime pick the orientation per page based on the document's dominant
// width×height ratio; the other two force every page of the line to that
// orientation.
const (
	OrientationAuto      = "auto"
	OrientationPortrait  = "portrait"
	OrientationLandscape = "landscape"
)

// normalizeOrientation returns the canonical orientation token for the
// supplied user input. Empty strings and unrecognised values map to
// "auto" so a client that omits the field keeps working.
func normalizeOrientation(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case OrientationPortrait:
		return OrientationPortrait
	case OrientationLandscape:
		return OrientationLandscape
	default:
		return OrientationAuto
	}
}

// normalizePagesPerSheet returns the supported n-up value for the
// supplied user input. The runtime only honours 1, 2 and 4 — any
// other value (including 0 and negative) maps to 1, the legacy
// behaviour of one page per sheet.
func normalizePagesPerSheet(raw int) int {
	switch raw {
	case 2, 4:
		return raw
	default:
		return 1
	}
}

// QuoteLineRequest describes a single print line from the customer portal.
// Orientation and PagesPerSheet are optional; absent values default to
// "auto" and 1 respectively so older portal clients keep working.
type QuoteLineRequest struct {
	Pages          []int  `json:"pages,omitempty"`
	DocumentID     string `json:"documentId"`
	PaperSize      string `json:"paperSize"`
	ColourMode     string `json:"colourMode"`
	Sides          string `json:"sides"`
	Copies         int    `json:"copies"`
	PageRangeStart int    `json:"pageRangeStart"`
	PageRangeEnd   int    `json:"pageRangeEnd"`
	Orientation    string `json:"orientation,omitempty"`
	PagesPerSheet  int    `json:"pagesPerSheet,omitempty"`
	Merge          bool   `json:"merge,omitempty"`
}

// QuoteLine is the server's authoritative pricing for one print line.
type QuoteLine struct {
	Pages          []int  `json:"pages"`
	DocumentID     string `json:"documentId"`
	PaperSize      string `json:"paperSize"`
	ColourMode     string `json:"colourMode"`
	Sides          string `json:"sides"`
	Copies         int    `json:"copies"`
	PageRangeStart int    `json:"pageRangeStart"`
	PageRangeEnd   int    `json:"pageRangeEnd"`
	Orientation    string `json:"orientation"`
	PagesPerSheet  int    `json:"pagesPerSheet"`
	Sheets         int64  `json:"sheets"`
	UnitPriceMinor int64  `json:"unitPriceMinor"`
	LineTotalMinor int64  `json:"lineTotalMinor"`
}

// Quote is the server's authoritative pricing response.
type Quote struct {
	SubtotalMinor      int64       `json:"subtotalMinor"`
	DiscountMinor      int64       `json:"discountMinor"`
	DiscountCode       string      `json:"discountCode"`
	Currency           string      `json:"currency"`
	CurrencyMinorUnits int         `json:"currencyMinorUnits"`
	Lines              []QuoteLine `json:"lines"`
	TotalMinor         int64       `json:"totalMinor"`
}

// PaymentMethod values recognised by the order service. The portal
// form offers a choice between these two; the merchant dashboard's
// settings can disable Razorpay so only "cash_on_counter" remains.
const (
	PaymentMethodCashOnCounter = "cash_on_counter"
	PaymentMethodRazorpay      = "razorpay"
)

// CreateOrderRequest mirrors the QuoteResponse that the customer accepted.
type CreateOrderRequest struct {
	Lines         []QuoteLineRequest `json:"lines"`
	CustomerName  string             `json:"customerName"`
	CustomerPhone string             `json:"customerPhone"`
	CustomerEmail string             `json:"customerEmail"`
	CustomerNotes string             `json:"customerNotes"`
	PaymentMethod string             `json:"paymentMethod"`
}

// Service handles order creation, quote computation and retrieval.
type Service struct {
	db      *sql.DB
	now     func() time.Time
	pricing *pricing.Service
}

func New(database *sql.DB, pricingService *pricing.Service) *Service {
	return &Service{db: database, now: time.Now, pricing: pricingService}
}

// loadCurrency returns the business profile's currency code and derived minor units.
func (s *Service) loadCurrency(ctx context.Context) (string, int, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT profile_json FROM business_profile WHERE singleton=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", 0, errors.New("business profile not found")
	}
	if err != nil {
		return "", 0, err
	}
	var p struct{ Currency string }
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "", 0, err
	}
	code := strings.ToUpper(strings.TrimSpace(p.Currency))
	mu := currency.MinorUnits(code)
	return code, mu, nil
}

// loadPricingMap returns a map from (paperKey|colourMode|sides) -> (basePrice, tiers)
// for use in quote computation.
func (s *Service) loadPricingMap(ctx context.Context, printerID string) (map[string]pricingEntry, error) {
	book, err := s.pricing.Effective(ctx, printerID)
	if err != nil {
		return nil, fmt.Errorf("load pricing book: %w", err)
	}
	if book.CurrencyMismatch || book.PrecisionMismatch || !book.CurrencySupported {
		return nil, errors.New("pricing currency changed; ask the shop to review its prices")
	}
	m := make(map[string]pricingEntry, len(book.Entries))
	for _, e := range book.Entries {
		key := pricingPaperKey(e.PaperSize) + "|" + e.ColourMode + "|" + e.Sides
		m[key] = pricingEntry{base: e.UnitPriceMinor, tiers: e.Tiers}
	}
	return m, nil
}

type pricingEntry struct {
	base  int64
	tiers []pricing.Tier
}

// ComputeQuote is the authoritative server-side quote calculation. It looks up
// each line's pricing combination, computes physical sheets accounting for
// simplex/duplex and the customer's page range, and returns a deterministic
// quote. The caller can present this quote to the customer; the actual
// CreateOrder call must submit the same line configuration.
func (s *Service) ComputeQuote(ctx context.Context, docPages map[string]int, req QuoteRequest) (Quote, error) {
	if len(req.Lines) == 0 {
		return Quote{}, errors.New("at least one print line is required")
	}
	currencyCode, mu, err := s.loadCurrency(ctx)
	if err != nil {
		return Quote{}, err
	}
	selectedPrinter := req.PrimaryPrinterID
	if selectedPrinter == "" {
		hasOverrides, e := s.pricing.HasPrinterPrices(ctx)
		if e != nil {
			return Quote{}, e
		}
		if hasOverrides {
			return Quote{}, errors.New("choose a printer to calculate its price")
		}
	}
	pricingMap, err := s.loadPricingMap(ctx, selectedPrinter)
	if err != nil {
		return Quote{}, err
	}

	if req.ServiceID != "" {
		var enabled bool
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM services WHERE id=? AND enabled=1)", req.ServiceID).Scan(&enabled); err != nil || !enabled {
			return Quote{}, errors.New("selected service is unavailable")
		}
		// The pricing grid is authoritative. Legacy service JSON must not
		// silently replace shared rates or printer overrides shown to owners.
	}

	if selectedPrinter != "" {
		overrides, e := s.pricing.GridBook(ctx, selectedPrinter)
		if e != nil {
			return Quote{}, e
		}
		if overrides.CurrencyMismatch || overrides.PrecisionMismatch {
			return Quote{}, errors.New("printer prices need review after currency change")
		}
		for _, entry := range overrides.Entries {
			pricingMap[pricingPaperKey(entry.PaperSize)+"|"+entry.ColourMode+"|"+entry.Sides] = pricingEntry{base: entry.UnitPriceMinor, tiers: entry.Tiers}
		}
	}
	lines := make([]QuoteLine, 0, len(req.Lines))
	var totalMinor int64
	for i, lr := range req.Lines {
		if lr.PaperSize == "" {
			return Quote{}, fmt.Errorf("line %d: paper size is required", i+1)
		}
		if lr.Copies < 1 || lr.Copies > 100 {
			return Quote{}, fmt.Errorf("line %d: copies must be at least 1", i+1)
		}
		if lr.ColourMode != pricing.ColourMonochrome && lr.ColourMode != pricing.ColourColour {
			return Quote{}, fmt.Errorf("line %d: colour mode must be monochrome or colour", i+1)
		}
		if lr.Sides != pricing.SidesOneSided && lr.Sides != pricing.SidesTwoSidedLongEdge && lr.Sides != pricing.SidesTwoSidedShortEdge {
			return Quote{}, fmt.Errorf("line %d: sides must be one-sided, two-sided-long-edge or two-sided-short-edge", i+1)
		}
		orientation := normalizeOrientation(lr.Orientation)
		pagesPerSheet := normalizePagesPerSheet(lr.PagesPerSheet)
		totalPages := docPages[lr.DocumentID]
		if totalPages == 0 {
			return Quote{}, fmt.Errorf("line %d: document %q has no page count", i+1, lr.DocumentID)
		}
		start := lr.PageRangeStart
		if start < 1 {
			start = 1
		}
		end := lr.PageRangeEnd
		if end < 1 || end > totalPages {
			end = totalPages
		}
		if start > end {
			return Quote{}, fmt.Errorf("line %d: page range start %d exceeds end %d", i+1, start, end)
		}
		selected, err := pageselection.Resolve(lr.Pages, start, end, totalPages)
		if err != nil {
			return Quote{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		start, end = selected[0], selected[len(selected)-1]
		pagesInRange := int64(len(selected))
		pagesPerPhysicalSheet := int64(pagesPerSheet)
		if lr.Sides != pricing.SidesOneSided {
			pagesPerPhysicalSheet *= 2
		}
		sheets := ((pagesInRange + pagesPerPhysicalSheet - 1) / pagesPerPhysicalSheet) * int64(lr.Copies)

		if sheets < 1 {
			sheets = 1
		}

		key := pricingPaperKey(lr.PaperSize) + "|" + lr.ColourMode + "|" + lr.Sides
		entry, ok := pricingMap[key]
		if !ok {
			return Quote{}, fmt.Errorf("line %d: no pricing found for %s / %s / %s", i+1, lr.PaperSize, lr.ColourMode, lr.Sides)
		}
		unitPrice, err := pricing.CalculateUnitPriceMinor(entry.base, entry.tiers, sheets)
		if err != nil {
			return Quote{}, fmt.Errorf("line %d: price calculation: %w", i+1, err)
		}
		lineTotal, err := pricing.CalculateLineTotal(entry.base, entry.tiers, sheets)
		if err != nil {
			return Quote{}, fmt.Errorf("line %d: total calculation: %w", i+1, err)
		}
		lines = append(lines, QuoteLine{
			DocumentID:     lr.DocumentID,
			Pages:          selected,
			PaperSize:      lr.PaperSize,
			ColourMode:     lr.ColourMode,
			Sides:          lr.Sides,
			Copies:         lr.Copies,
			PageRangeStart: start,
			PageRangeEnd:   end,
			Orientation:    orientation,
			PagesPerSheet:  pagesPerSheet,
			Sheets:         sheets,
			UnitPriceMinor: unitPrice,
			LineTotalMinor: lineTotal.TotalMinor,
		})
		totalMinor += lineTotal.TotalMinor
	}
	discount, record, err := business.DiscountFor(ctx, s.db, req.DiscountCode, totalMinor, s.now().Unix())
	if err != nil {
		return Quote{}, fmt.Errorf("discount: %w", err)
	}
	return Quote{
		Currency:           currencyCode,
		CurrencyMinorUnits: mu,
		Lines:              lines,
		TotalMinor:         totalMinor - discount,
		SubtotalMinor:      totalMinor, DiscountMinor: discount, DiscountCode: record.Code,
	}, nil
}

// CreateOrder persists a new customer order with its lines. The order status
// starts as pending_payment. Caller is responsible for validating the request
// fields and that the line configuration matches a prior ComputeQuote result.
func (s *Service) CreateOrder(ctx context.Context, req CreateOrderRequest, quoteLines []QuoteLine) (Order, error) {
	if len(req.Lines) == 0 {
		return Order{}, errors.New("at least one print line is required")
	}
	if len(req.CustomerName) < 1 || len(req.CustomerName) > 200 {
		return Order{}, errors.New("customer name is required")
	}
	if len(req.CustomerPhone) < 1 || len(req.CustomerPhone) > 40 {
		return Order{}, errors.New("customer phone is required")
	}
	currencyCode, mu, err := s.loadCurrency(ctx)
	if err != nil {
		return Order{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	orderID, err := randomID()
	if err != nil {
		return Order{}, fmt.Errorf("generate order id: %w", err)
	}
	shareToken, err := randomID()
	if err != nil {
		return Order{}, fmt.Errorf("generate share token: %w", err)
	}
	now := s.now().Unix()

	// Total is the sum of quote line totals. We recompute from the passed-in
	// quote lines for consistency with ComputeQuote.
	var totalMinor int64
	for _, ql := range quoteLines {
		totalMinor += ql.LineTotalMinor
	}
	if totalMinor < 0 {
		totalMinor = 0
	}

	paymentMethod := strings.TrimSpace(req.PaymentMethod)
	if paymentMethod == "" {
		paymentMethod = PaymentMethodCashOnCounter
	}
	if paymentMethod != PaymentMethodCashOnCounter && paymentMethod != PaymentMethodRazorpay {
		return Order{}, fmt.Errorf("unsupported payment method %q", req.PaymentMethod)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, payment_method,
	created_at, updated_at, portal_gate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		orderID, shareToken, StatusPendingPayment, currencyCode, mu, totalMinor,
		strings.TrimSpace(req.CustomerName),
		strings.TrimSpace(req.CustomerPhone),
		strings.TrimSpace(req.CustomerEmail),
		strings.TrimSpace(req.CustomerNotes),
		paymentMethod,
		now, now)
	if err != nil {
		return Order{}, fmt.Errorf("insert order: %w", err)
	}

	for i, ql := range quoteLines {
		lineID, err := randomID()
		if err != nil {
			return Order{}, fmt.Errorf("generate line id: %w", err)
		}
		reqLine := req.Lines[i]
		if ql.PageRangeStart == 0 {
			ql.PageRangeStart = reqLine.PageRangeStart
		}
		if ql.PageRangeEnd == 0 {
			ql.PageRangeEnd = reqLine.PageRangeEnd
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, orientation, pages_per_sheet,
	unit_price_minor, line_total_minor,selected_pages_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			lineID, orderID, reqLine.DocumentID,
			reqLine.PaperSize, pricingPaperKey(reqLine.PaperSize),
			reqLine.ColourMode, reqLine.Sides,
			reqLine.Copies, ql.PageRangeStart, ql.PageRangeEnd,
			normalizeOrientation(ql.Orientation),
			normalizePagesPerSheet(ql.PagesPerSheet),
			ql.UnitPriceMinor, ql.LineTotalMinor, pageselection.Encode(ql.Pages))
		if err != nil {
			return Order{}, fmt.Errorf("insert order line: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Order{}, fmt.Errorf("commit order: %w", err)
	}

	return Order{
		ID:            orderID,
		ShareToken:    shareToken,
		Status:        StatusPendingPayment,
		PaymentMethod: paymentMethod,
		Currency:      currencyCode,
		CurrencyMU:    mu,
		TotalMinor:    totalMinor,
		CustomerName:  req.CustomerName,
		CustomerPhone: req.CustomerPhone,
		CustomerEmail: req.CustomerEmail,
		CustomerNotes: req.CustomerNotes,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// Get retrieves an order by ID, loading its lines.
func (s *Service) Get(ctx context.Context, orderID string) (Order, error) {
	var o Order
	err := s.db.QueryRowContext(ctx, `
SELECT id, share_token, status, payment_method, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at,subtotal_minor,discount_minor,discount_code
FROM orders WHERE id=?`, orderID).Scan(
		&o.ID, &o.ShareToken, &o.Status, &o.PaymentMethod, &o.Currency, &o.CurrencyMU, &o.TotalMinor,
		&o.CustomerName, &o.CustomerPhone, &o.CustomerEmail, &o.CustomerNotes, &o.CreatedAt, &o.UpdatedAt, &o.SubtotalMinor, &o.DiscountMinor, &o.DiscountCode)
	if err == sql.ErrNoRows {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	lines, err := s.loadLines(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	o.Lines = lines
	return o, nil
}

// List returns all orders ordered by creation time descending.
func (s *Service) List(ctx context.Context) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, share_token, status, payment_method, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at,subtotal_minor,discount_minor,discount_code
FROM orders ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(
			&o.ID, &o.ShareToken, &o.Status, &o.PaymentMethod, &o.Currency, &o.CurrencyMU, &o.TotalMinor,
			&o.CustomerName, &o.CustomerPhone, &o.CustomerEmail, &o.CustomerNotes, &o.CreatedAt, &o.UpdatedAt, &o.SubtotalMinor, &o.DiscountMinor, &o.DiscountCode); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func (s *Service) loadLines(ctx context.Context, orderID string) ([]OrderLine, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, orientation, pages_per_sheet,
	unit_price_minor, line_total_minor,selected_pages_json
FROM order_lines WHERE order_id=?`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []OrderLine
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(
			&l.ID, &l.OrderID, &l.DocumentID, &l.PaperSize, &l.PaperKey,
			&l.ColourMode, &l.Sides, &l.Copies, &l.PageRangeStart, &l.PageRangeEnd,
			&l.Orientation, &l.PagesPerSheet,
			&l.UnitPriceMinor, &l.LineTotalMinor, &l.PagesJSON); err != nil {
			return nil, err
		}
		// Old rows pre-021 may have empty orientation/pages_per_sheet; the
		// migration defaults fill them, but be defensive for callers that
		// loaded from a backup or rolled back.
		l.Orientation = normalizeOrientation(l.Orientation)
		l.PagesPerSheet = normalizePagesPerSheet(l.PagesPerSheet)
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// TransitionEvent names the audit event recorded by each status transition.
const (
	EventPaid       = "order.paid"
	EventDispatched = "order.dispatched"
	EventCompleted  = "order.completed"
	EventCancelled  = "order.cancelled"
)

// allowedTransitions is the directed graph of valid status transitions.
// Any transition outside this map returns ErrInvalidTransition so a stray
// request (e.g., cancelled → paid) is refused with a 409.
var allowedTransitions = map[string]map[string]bool{
	StatusPendingPayment: {StatusPaid: true, StatusCancelled: true},
	StatusPaid:           {StatusDispatched: true, StatusCancelled: true},
	StatusDispatched:     {StatusCompleted: true, StatusCancelled: true},
	// terminal states intentionally have no outgoing transitions.
	StatusCompleted: {},
	StatusFailed:    {},
	StatusCancelled: {},
}

// ErrInvalidTransition is returned when a caller asks for a status change the
// order's current state does not allow (e.g., completed → paid).
var ErrInvalidTransition = errors.New("the requested order status transition is not allowed")

// ErrInvoiceAlreadyExists is returned when an order is asked to take a second
// invoice. Each order has exactly one invoice row.
var ErrInvoiceAlreadyExists = errors.New("an invoice already exists for this order")

// SetStatus changes an order's status with a server-side state-machine guard.
// It records an audit event in the same transaction so an audit row is never
// present without the matching status change. Returns ErrInvalidTransition
// when the requested transition is not allowed by allowedTransitions, or
// ErrNotFound when the order does not exist.
func (s *Service) SetStatus(ctx context.Context, orderID string, target string, actor string) (Order, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Order{}, errors.New("actor is required for status changes")
	}
	event, ok := statusToEvent[target]
	if !ok {
		return Order{}, errors.New("unknown target status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT status FROM orders WHERE id=?`, orderID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	if next, ok := allowedTransitions[current]; !ok || !next[target] {
		return Order{}, ErrInvalidTransition
	}
	now := s.now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET status=?, updated_at=? WHERE id=?`, target, now, orderID); err != nil {
		return Order{}, err
	}
	if err := writeAuditEvent(ctx, tx, event, fmt.Sprintf("order %s: %s -> %s by %s", orderID, current, target, actor), now); err != nil {
		return Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return s.Get(ctx, orderID)
}

// statusToEvent maps a target status to its audit event_type.
var statusToEvent = map[string]string{
	StatusPaid:       EventPaid,
	StatusDispatched: EventDispatched,
	StatusCompleted:  EventCompleted,
	StatusCancelled:  EventCancelled,
}

// writeAuditEvent records an audit event in the caller's transaction using the
// same envelope as the existing owner service (random hex id + RFC3339Nano
// timestamp). This keeps the audit format consistent across the runtime.
func writeAuditEvent(ctx context.Context, tx *sql.Tx, event, evidence string, nowUnix int64) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events (id, event_type, evidence, occurred_at) VALUES (?, ?, ?, ?)`,
		id, event, evidence, time.Unix(nowUnix, 0).UTC().Format(time.RFC3339Nano))
	return err
}

// Invoice is the durable record of a paid/settled order. Merchant fields are
// snapshotted from the business profile at issue time so changing business
// details later does not rewrite old invoices.
type Invoice struct {
	SubtotalMinor int64
	DiscountMinor int64
	DiscountCode  string
	ID            string
	Number        int64
	OrderID       string
	IssuedAt      int64
	IssuedBy      string
	Currency      string
	CurrencyMU    int
	TotalMinor    int64
	Merchant      MerchantSnapshot
	CustomerName  string
	CustomerPhone string
	CustomerEmail string
	Lines         []InvoiceLine
}

// MerchantSnapshot is the merchant identity as captured on the invoice row.
type MerchantSnapshot struct {
	Name     string
	Address  string
	Phone    string
	Country  string
	Currency string
	Locale   string
	TimeZone string
}

// InvoiceLine mirrors an order line at the moment the invoice was issued.
type InvoiceLine struct {
	PagesJSON                string
	ID                       string
	PaperSize                string
	ColourMode               string
	Sides                    string
	Copies                   int
	PageRangeStart           int
	PageRangeEnd             int
	UnitPriceMinor           int64
	LineTotalMinor           int64
	DocumentOriginalFilename string
}

// IssueInvoice creates an invoice for the given paid (or otherwise manually
// approved) order, allocating the next sequence number atomically. The order
// must currently be in the `paid` state — an unpaid order has no settled
// total to invoice. Returns ErrInvalidTransition if the order is not paid
// or if the order already has an invoice (use the existing invoice).
func (s *Service) IssueInvoice(ctx context.Context, orderID string, actor string) (Invoice, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Invoice{}, errors.New("actor is required for invoice issue")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invoice{}, err
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM invoices WHERE order_id=?`, orderID).Scan(&existing)
	if err == nil {
		return Invoice{}, ErrInvoiceAlreadyExists
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Invoice{}, err
	}

	// Load the order with its lines.
	order, lines, err := s.loadOrderWithLinesTx(ctx, tx, orderID)
	if err != nil {
		return Invoice{}, err
	}
	if order.Status != StatusPaid {
		return Invoice{}, ErrInvalidTransition
	}

	// Snapshot merchant identity.
	merchant, err := loadMerchantSnapshotTx(ctx, tx)
	if err != nil {
		return Invoice{}, err
	}

	// Allocate next invoice number atomically.
	var next int64
	if err := tx.QueryRowContext(ctx, `UPDATE invoice_sequence SET next_value = next_value + 1 WHERE singleton=1 RETURNING next_value - 1`).Scan(&next); err != nil {
		return Invoice{}, fmt.Errorf("allocate invoice number: %w", err)
	}

	invID, err := randomID()
	if err != nil {
		return Invoice{}, err
	}
	nowUnix := s.now().Unix()
	_, err = tx.ExecContext(ctx, `
INSERT INTO invoices (id, number, order_id, issued_at, issued_by, currency, currency_minor_units,
	total_minor, merchant_name, merchant_address, merchant_phone, merchant_country, merchant_currency,
	merchant_locale, merchant_time_zone, customer_name, customer_phone, customer_email,subtotal_minor,discount_minor,discount_code)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		invID, next, orderID, nowUnix, actor, order.Currency, order.CurrencyMU, order.TotalMinor,
		merchant.Name, merchant.Address, merchant.Phone, merchant.Country, merchant.Currency,
		merchant.Locale, merchant.TimeZone, order.CustomerName, order.CustomerPhone, order.CustomerEmail, order.TotalMinor+order.DiscountMinor, order.DiscountMinor, order.DiscountCode)
	if err != nil {
		return Invoice{}, fmt.Errorf("insert invoice: %w", err)
	}

	// Insert invoice lines. The document filename is loaded by joining
	// order_lines with documents inside the same transaction.
	for _, l := range lines {
		lineID, err := randomID()
		if err != nil {
			return Invoice{}, err
		}
		var docFile string
		if err := tx.QueryRowContext(ctx, `SELECT original_filename FROM documents WHERE id=?`, l.DocumentID).Scan(&docFile); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Invoice{}, fmt.Errorf("load document filename: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO invoice_lines (id, invoice_id, paper_size, colour_mode, sides, copies,
	page_range_start, page_range_end, unit_price_minor, line_total_minor, document_original_filename,selected_pages_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			lineID, invID, l.PaperSize, l.ColourMode, l.Sides, l.Copies,
			l.PageRangeStart, l.PageRangeEnd, l.UnitPriceMinor, l.LineTotalMinor, docFile, l.PagesJSON)
		if err != nil {
			return Invoice{}, fmt.Errorf("insert invoice line: %w", err)
		}
	}

	if err := writeAuditEvent(ctx, tx, "order.invoice.issued", fmt.Sprintf("invoice %d for order %s by %s", next, orderID, actor), nowUnix); err != nil {
		return Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return Invoice{}, err
	}

	return s.GetInvoice(ctx, invID)
}

// loadOrderWithLinesTx loads an order and its lines inside an existing
// transaction so IssueInvoice can use it together with the invoice INSERTs.
func (s *Service) loadOrderWithLinesTx(ctx context.Context, tx *sql.Tx, orderID string) (Order, []OrderLine, error) {
	var o Order
	err := tx.QueryRowContext(ctx, `
SELECT id, share_token, status, payment_method, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at,subtotal_minor,discount_minor,discount_code
FROM orders WHERE id=?`, orderID).Scan(
		&o.ID, &o.ShareToken, &o.Status, &o.PaymentMethod, &o.Currency, &o.CurrencyMU, &o.TotalMinor,
		&o.CustomerName, &o.CustomerPhone, &o.CustomerEmail, &o.CustomerNotes, &o.CreatedAt, &o.UpdatedAt, &o.SubtotalMinor, &o.DiscountMinor, &o.DiscountCode)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, nil, ErrNotFound
	}
	if err != nil {
		return Order{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, unit_price_minor, line_total_minor,selected_pages_json
FROM order_lines WHERE order_id=?`, orderID)
	if err != nil {
		return Order{}, nil, err
	}
	defer rows.Close()
	var lines []OrderLine
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(
			&l.ID, &l.OrderID, &l.DocumentID, &l.PaperSize, &l.PaperKey,
			&l.ColourMode, &l.Sides, &l.Copies, &l.PageRangeStart, &l.PageRangeEnd,
			&l.UnitPriceMinor, &l.LineTotalMinor, &l.PagesJSON); err != nil {
			return Order{}, nil, err
		}
		lines = append(lines, l)
	}
	return o, lines, rows.Err()
}

// loadMerchantSnapshotTx reads the business profile JSON and produces a typed
// MerchantSnapshot. The migration 002 already enforces field-level validation
// so any pre-existing row is well-formed.
func loadMerchantSnapshotTx(ctx context.Context, tx *sql.Tx) (MerchantSnapshot, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT profile_json FROM business_profile WHERE singleton=1`).Scan(&raw); err != nil {
		return MerchantSnapshot{}, err
	}
	var p struct {
		Name     string `json:"name"`
		Address  string `json:"address"`
		Phone    string `json:"phone"`
		Country  string `json:"country"`
		Currency string `json:"currency"`
		Locale   string `json:"locale"`
		TimeZone string `json:"timeZone"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return MerchantSnapshot{}, err
	}
	return MerchantSnapshot{
		Name: p.Name, Address: p.Address, Phone: p.Phone, Country: p.Country,
		Currency: p.Currency, Locale: p.Locale, TimeZone: p.TimeZone,
	}, nil
}

// GetInvoice returns an invoice with lines for a single invoice id.
func (s *Service) GetInvoice(ctx context.Context, invoiceID string) (Invoice, error) {
	var inv Invoice
	err := s.db.QueryRowContext(ctx, `
SELECT id, number, order_id, issued_at, issued_by, currency, currency_minor_units, total_minor,
	merchant_name, merchant_address, merchant_phone, merchant_country, merchant_currency,
	merchant_locale, merchant_time_zone, customer_name, customer_phone, customer_email,subtotal_minor,discount_minor,discount_code
FROM invoices WHERE id=?`, invoiceID).Scan(
		&inv.ID, &inv.Number, &inv.OrderID, &inv.IssuedAt, &inv.IssuedBy, &inv.Currency, &inv.CurrencyMU, &inv.TotalMinor,
		&inv.Merchant.Name, &inv.Merchant.Address, &inv.Merchant.Phone, &inv.Merchant.Country, &inv.Merchant.Currency,
		&inv.Merchant.Locale, &inv.Merchant.TimeZone, &inv.CustomerName, &inv.CustomerPhone, &inv.CustomerEmail, &inv.SubtotalMinor, &inv.DiscountMinor, &inv.DiscountCode)
	if errors.Is(err, sql.ErrNoRows) {
		return Invoice{}, ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}
	lines, err := s.loadInvoiceLines(ctx, invoiceID)
	if err != nil {
		return Invoice{}, err
	}
	inv.Lines = lines
	return inv, nil
}

// GetInvoiceForOrder returns the invoice attached to a given order, or
// ErrNotFound if the order has no invoice yet.
func (s *Service) GetInvoiceForOrder(ctx context.Context, orderID string) (Invoice, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM invoices WHERE order_id=?`, orderID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invoice{}, ErrNotFound
		}
		return Invoice{}, err
	}
	return s.GetInvoice(ctx, id)
}

// ListInvoices returns every invoice ordered by issue time descending.
func (s *Service) ListInvoices(ctx context.Context) ([]InvoiceSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, number, order_id, issued_at, currency, currency_minor_units, total_minor, customer_name
FROM invoices ORDER BY issued_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvoiceSummary
	for rows.Next() {
		var inv InvoiceSummary
		if err := rows.Scan(&inv.ID, &inv.Number, &inv.OrderID, &inv.IssuedAt, &inv.Currency, &inv.CurrencyMU, &inv.TotalMinor, &inv.CustomerName); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// InvoiceSummary is the compact shape used by the invoice list endpoint.
type InvoiceSummary struct {
	ID           string
	Number       int64
	OrderID      string
	IssuedAt     int64
	Currency     string
	CurrencyMU   int
	TotalMinor   int64
	CustomerName string
}

func (s *Service) loadInvoiceLines(ctx context.Context, invoiceID string) ([]InvoiceLine, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, paper_size, colour_mode, sides, copies,
	page_range_start, page_range_end, unit_price_minor, line_total_minor, document_original_filename,selected_pages_json
FROM invoice_lines WHERE invoice_id=?`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []InvoiceLine
	for rows.Next() {
		var l InvoiceLine
		if err := rows.Scan(
			&l.ID, &l.PaperSize, &l.ColourMode, &l.Sides, &l.Copies,
			&l.PageRangeStart, &l.PageRangeEnd, &l.UnitPriceMinor, &l.LineTotalMinor,
			&l.DocumentOriginalFilename, &l.PagesJSON); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

var ErrNotFound = errors.New("order not found")

var wsPat = regexp.MustCompile(`\s+`)

func pricingPaperKey(value string) string {
	return strings.ToUpper(strings.TrimSpace(wsPat.ReplaceAllString(value, " ")))
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
