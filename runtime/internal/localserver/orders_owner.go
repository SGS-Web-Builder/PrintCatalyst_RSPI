package localserver

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
)

// hexIDPattern matches a 32-character hex string (our order and document IDs).
var hexIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// registerOwnerOrders wires the owner-facing order list and detail endpoints.
// Both routes require a valid owner/operator session and CanViewOrders
// permission (owner and operator may see the queue).
func (s *Server) registerOwnerOrders(mux *http.ServeMux) {
	protect := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.localOwnerRequest(w, r) {
				return
			}
			if s.owner == nil {
				http.Error(w, "owner service unavailable", 503)
				return
			}
			_, subject, ok := s.ownerSession(w, r)
			if !ok {
				return
			}
			if !owner.RequirePermission(w, subject, owner.CanViewOrders) {
				return
			}
			fn(w, r)
		}
	}

	mux.HandleFunc("GET /api/v1/owner/orders", protect(func(w http.ResponseWriter, r *http.Request) {
		orderList, err := s.orders.List(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		views := make([]orderListView, 0, len(orderList))
		for _, o := range orderList {
			views = append(views, orderListView{
				ID:            o.ID,
				Status:        o.Status,
				TotalMinor:    o.TotalMinor,
				CurrencyMU:    o.CurrencyMU,
				Currency:      o.Currency,
				CustomerName:  o.CustomerName,
				CustomerPhone: o.CustomerPhone,
				PaymentMethod: o.PaymentMethod,
				CreatedAt:     o.CreatedAt,
			})
		}
		if err := s.enrichOrderQueue(r.Context(), views); err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"orders": views})
	}))

	mux.HandleFunc("GET /api/v1/owner/orders/{id}", protect(func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("id")
		if orderID == "" || !hexIDPattern.MatchString(orderID) {
			http.Error(w, "order id must be a valid hex identifier", 400)
			return
		}
		order, err := s.orders.Get(r.Context(), orderID)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toOrderView(order))
	}))
}

// orderListView is a compact summary for the order queue list.
type orderListView struct {
	Separators     []queueSeparator `json:"separators"`
	PaymentMethod  string           `json:"paymentMethod"`
	PrintRequested bool             `json:"printRequested"`
	Documents      []queueDocument  `json:"documents"`

	CurrencyMU    int    `json:"currencyMinorUnits"`
	ID            string `json:"id"`
	Status        string `json:"status"`
	TotalMinor    int64  `json:"totalMinor"`
	Currency      string `json:"currency"`
	CustomerName  string `json:"customerName"`
	CustomerPhone string `json:"customerPhone"`
	CreatedAt     int64  `json:"createdAt"`
}

// orderOwnerView is the full order detail returned to the owner dashboard.
type orderOwnerView struct {
	PaymentMethod string               `json:"paymentMethod"`
	ID            string               `json:"id"`
	Status        string               `json:"status"`
	TotalMinor    int64                `json:"totalMinor"`
	Currency      string               `json:"currency"`
	CurrencyMU    int                  `json:"currencyMinorUnits"`
	CustomerName  string               `json:"customerName"`
	CustomerPhone string               `json:"customerPhone"`
	CustomerEmail string               `json:"customerEmail"`
	CustomerNotes string               `json:"customerNotes"`
	CreatedAt     int64                `json:"createdAt"`
	UpdatedAt     int64                `json:"updatedAt"`
	Lines         []orderLineOwnerView `json:"lines"`
}

type orderLineOwnerView struct {
	Orientation   string `json:"orientation"`
	PagesPerSheet int    `json:"pagesPerSheet"`

	Pages          json.RawMessage `json:"pages"`
	ID             string          `json:"id"`
	DocumentID     string          `json:"documentId"`
	PaperSize      string          `json:"paperSize"`
	ColourMode     string          `json:"colourMode"`
	Sides          string          `json:"sides"`
	Copies         int             `json:"copies"`
	PageRangeStart int             `json:"pageRangeStart"`
	PageRangeEnd   int             `json:"pageRangeEnd"`
	UnitPriceMinor int64           `json:"unitPriceMinor"`
	LineTotalMinor int64           `json:"lineTotalMinor"`
}

func toOrderView(o orders.Order) orderOwnerView {
	lines := make([]orderLineOwnerView, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, orderLineOwnerView{
			ID:          l.ID,
			Orientation: l.Orientation, PagesPerSheet: l.PagesPerSheet,
			DocumentID:     l.DocumentID,
			PaperSize:      l.PaperSize,
			ColourMode:     l.ColourMode,
			Sides:          l.Sides,
			Copies:         l.Copies,
			PageRangeStart: l.PageRangeStart,
			Pages:          json.RawMessage(l.PagesJSON),
			PageRangeEnd:   l.PageRangeEnd,
			UnitPriceMinor: l.UnitPriceMinor,
			LineTotalMinor: l.LineTotalMinor,
		})
	}
	return orderOwnerView{
		ID:            o.ID,
		Status:        o.Status,
		TotalMinor:    o.TotalMinor,
		Currency:      o.Currency,
		CurrencyMU:    o.CurrencyMU,
		CustomerName:  o.CustomerName,
		CustomerPhone: o.CustomerPhone,
		PaymentMethod: o.PaymentMethod,
		CustomerEmail: o.CustomerEmail,
		CustomerNotes: o.CustomerNotes,
		CreatedAt:     o.CreatedAt,
		UpdatedAt:     o.UpdatedAt,
		Lines:         lines,
	}
}

// registerOwnerStatusAndInvoices wires the additional order-level routes
// (status transitions, invoice retrieval/issue, invoice list).
func (s *Server) registerOwnerStatusAndInvoices(mux *http.ServeMux) {
	statusHandler := s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {})
	_ = statusHandler
	issueHandler := s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {})
	_ = issueHandler

	// PUT /api/v1/owner/orders/{id}/status — status transition (owner only)
	mux.HandleFunc("PUT /api/v1/owner/orders/{id}/status", s.statusTransitionHandler())

	// POST /api/v1/owner/orders/{id}/invoice — issue invoice (owner only)
	mux.HandleFunc("POST /api/v1/owner/orders/{id}/invoice", s.issueInvoiceHandler())

	// GET /api/v1/owner/orders/{id}/invoice — retrieve invoice (owner + operator)
	mux.HandleFunc("GET /api/v1/owner/orders/{id}/invoice", s.invoiceForOrderHandler())

	// GET /api/v1/owner/invoices — list every issued invoice (owner + operator)
	mux.HandleFunc("GET /api/v1/owner/invoices", s.listInvoicesHandler())
}

// protectOwnerView and protectOwnerEdit return owner-loopback + CSRF guards
// scoped to the requested permission.
func (s *Server) protectOwnerView(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.localOwnerRequest(w, r) {
			return
		}
		if s.owner == nil {
			http.Error(w, "owner service unavailable", 503)
			return
		}
		_, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		if !owner.RequirePermission(w, subject, owner.CanViewOrders) {
			return
		}
		next(w, r)
	}
}

func (s *Server) protectOwnerEdit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.localOwnerRequest(w, r) {
			return
		}
		if s.owner == nil {
			http.Error(w, "owner service unavailable", 503)
			return
		}
		_, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		if !owner.RequirePermission(w, subject, owner.CanEditOrders) {
			return
		}
		next(w, r)
	}
}

// statusTransitionHandler changes the status of an existing order.
func (s *Server) statusTransitionHandler() http.HandlerFunc {
	return s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("id")
		if orderID == "" || !hexIDPattern.MatchString(orderID) {
			http.Error(w, "order id must be a valid hex identifier", 400)
			return
		}
		var input struct {
			Status string `json:"status"`
		}
		if !decodeOwnerJSON(w, r, &input, 4<<10) {
			return
		}
		target := strings.TrimSpace(input.Status)
		if target == "" {
			http.Error(w, "status is required", 400)
			return
		}
		// Resolved subject Name comes from ownerSession's return — re-derive
		// it from the cookie path here via the owner service for the audit row.
		actor, _ := s.subjectName(w, r)
		if target == "print_completed" {
			if err := s.orders.ConfirmPrintDone(r.Context(), orderID, actor); err != nil {
				ownerError(w, err)
				return
			}
			updated, err := s.orders.Get(r.Context(), orderID)
			if err != nil {
				ownerError(w, err)
				return
			}
			writeJSON(w, toOrderView(updated))
			return
		}
		updated, err := s.orders.SetStatus(r.Context(), orderID, target, actor)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toOrderView(updated))
	})
}

// issueInvoiceHandler creates an invoice for a paid order.
func (s *Server) issueInvoiceHandler() http.HandlerFunc {
	return s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("id")
		if orderID == "" || !hexIDPattern.MatchString(orderID) {
			http.Error(w, "order id must be a valid hex identifier", 400)
			return
		}
		actor, _ := s.subjectName(w, r)
		inv, err := s.orders.IssueInvoice(r.Context(), orderID, actor)
		if err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(toInvoiceView(inv))
	})
}

// invoiceForOrderHandler returns the invoice for the given order.
func (s *Server) invoiceForOrderHandler() http.HandlerFunc {
	return s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("id")
		if orderID == "" || !hexIDPattern.MatchString(orderID) {
			http.Error(w, "order id must be a valid hex identifier", 400)
			return
		}
		inv, err := s.orders.GetInvoiceForOrder(r.Context(), orderID)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toInvoiceView(inv))
	})
}

// listInvoicesHandler returns every issued invoice summary.
func (s *Server) listInvoicesHandler() http.HandlerFunc {
	return s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.orders.ListInvoices(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		views := make([]invoiceSummaryView, 0, len(items))
		for _, i := range items {
			views = append(views, invoiceSummaryView{
				ID:           i.ID,
				Number:       i.Number,
				OrderID:      i.OrderID,
				IssuedAt:     i.IssuedAt,
				Currency:     i.Currency,
				CurrencyMU:   i.CurrencyMU,
				TotalMinor:   i.TotalMinor,
				CustomerName: i.CustomerName,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"invoices": views})
	})
}

// subjectName returns the resolved Subject name for an authenticated owner
// session. Used to label audit events. Returns ("", false) if no subject can
// be derived (in which case handlers must not allow the action).
func (s *Server) subjectName(w http.ResponseWriter, r *http.Request) (string, bool) {
	_, subject, ok := s.ownerSession(w, r)
	if !ok {
		return "", false
	}
	return subject.Name, true
}

type invoiceSummaryView struct {
	ID           string `json:"id"`
	Number       int64  `json:"number"`
	OrderID      string `json:"orderId"`
	IssuedAt     int64  `json:"issuedAt"`
	Currency     string `json:"currency"`
	CurrencyMU   int    `json:"currencyMinorUnits"`
	TotalMinor   int64  `json:"totalMinor"`
	CustomerName string `json:"customerName"`
}

type invoiceView struct {
	SubtotalMinor int64                `json:"subtotalMinor"`
	DiscountMinor int64                `json:"discountMinor"`
	DiscountCode  string               `json:"discountCode"`
	ID            string               `json:"id"`
	Number        int64                `json:"number"`
	OrderID       string               `json:"orderId"`
	IssuedAt      int64                `json:"issuedAt"`
	IssuedBy      string               `json:"issuedBy"`
	Currency      string               `json:"currency"`
	CurrencyMU    int                  `json:"currencyMinorUnits"`
	TotalMinor    int64                `json:"totalMinor"`
	Merchant      merchantSnapshotView `json:"merchant"`
	CustomerName  string               `json:"customerName"`
	CustomerPhone string               `json:"customerPhone"`
	CustomerEmail string               `json:"customerEmail"`
	Lines         []invoiceLineView    `json:"lines"`
}

type merchantSnapshotView struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Phone    string `json:"phone"`
	Country  string `json:"country"`
	Currency string `json:"currency"`
	Locale   string `json:"locale"`
	TimeZone string `json:"timeZone"`
}

type invoiceLineView struct {
	Pages                    json.RawMessage `json:"pages"`
	ID                       string          `json:"id"`
	PaperSize                string          `json:"paperSize"`
	ColourMode               string          `json:"colourMode"`
	Sides                    string          `json:"sides"`
	Copies                   int             `json:"copies"`
	PageRangeStart           int             `json:"pageRangeStart"`
	PageRangeEnd             int             `json:"pageRangeEnd"`
	UnitPriceMinor           int64           `json:"unitPriceMinor"`
	LineTotalMinor           int64           `json:"lineTotalMinor"`
	DocumentOriginalFilename string          `json:"documentOriginalFilename"`
}

func toInvoiceView(inv orders.Invoice) invoiceView {
	lines := make([]invoiceLineView, 0, len(inv.Lines))
	for _, l := range inv.Lines {
		lines = append(lines, invoiceLineView{
			ID:                       l.ID,
			PaperSize:                l.PaperSize,
			ColourMode:               l.ColourMode,
			Sides:                    l.Sides,
			Copies:                   l.Copies,
			PageRangeStart:           l.PageRangeStart,
			Pages:                    json.RawMessage(l.PagesJSON),
			PageRangeEnd:             l.PageRangeEnd,
			UnitPriceMinor:           l.UnitPriceMinor,
			LineTotalMinor:           l.LineTotalMinor,
			DocumentOriginalFilename: l.DocumentOriginalFilename,
		})
	}
	return invoiceView{
		ID:            inv.ID,
		Number:        inv.Number,
		OrderID:       inv.OrderID,
		IssuedAt:      inv.IssuedAt,
		IssuedBy:      inv.IssuedBy,
		Currency:      inv.Currency,
		CurrencyMU:    inv.CurrencyMU,
		TotalMinor:    inv.TotalMinor,
		SubtotalMinor: inv.SubtotalMinor, DiscountMinor: inv.DiscountMinor, DiscountCode: inv.DiscountCode,
		Merchant: merchantSnapshotView{
			Name: inv.Merchant.Name, Address: inv.Merchant.Address,
			Phone: inv.Merchant.Phone, Country: inv.Merchant.Country,
			Currency: inv.Merchant.Currency, Locale: inv.Merchant.Locale,
			TimeZone: inv.Merchant.TimeZone,
		},
		CustomerName:  inv.CustomerName,
		CustomerPhone: inv.CustomerPhone,
		CustomerEmail: inv.CustomerEmail,
		Lines:         lines,
	}
}

// Read all queue documents in one query so polling does not open a query per order.
func (s *Server) enrichOrderQueue(ctx context.Context, views []orderListView) error {
	indexes := map[string]int{}
	for i := range views {
		indexes[views[i].ID] = i
		views[i].Documents = []queueDocument{}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.order_id,l.document_id,d.original_filename,d.mime_type,d.page_count,d.size_bytes,d.purged_at,
 l.paper_size,l.colour_mode,l.sides,l.copies,l.page_range_start,l.page_range_end,l.orientation,l.pages_per_sheet,l.selected_pages_json,l.line_total_minor,l.unit_price_minor,
 COALESCE(NULLIF(j.progress,''),j.state,''),COALESCE(NULLIF(j.progress_detail,''),j.error,''),o.print_requested
 FROM order_lines l JOIN documents d ON d.id=l.document_id JOIN orders o ON o.id=l.order_id
 LEFT JOIN print_submissions j ON j.line_id=l.id WHERE o.submitted_at>0 ORDER BY l.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d queueDocument
		var purged int64
		var requested bool
		if err = rows.Scan(&id, &d.ID, &d.Filename, &d.MIME, &d.PageCount, &d.SizeBytes, &purged, &d.PaperSize, &d.ColourMode, &d.Sides, &d.Copies, &d.PageStart, &d.PageEnd, &d.Orientation, &d.PagesPerSheet, &d.Pages, &d.TotalMinor, &d.UnitPriceMinor, &d.PrintState, &d.PrintError, &requested); err != nil {
			return err
		}
		d.Purged = purged > 0
		if i, ok := indexes[id]; ok {
			views[i].Documents = append(views[i].Documents, d)
			views[i].PrintRequested = requested
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return s.enrichSeparators(ctx, views, indexes)
}

type queueDocument struct {
	UnitPriceMinor int64  `json:"unitPriceMinor"`
	ID             string `json:"id"`
	Filename       string `json:"filename"`
	MIME           string `json:"mime"`
	PageCount      int    `json:"pageCount"`
	SizeBytes      int64  `json:"sizeBytes"`
	Purged         bool   `json:"purged"`
	PaperSize      string `json:"paperSize"`
	ColourMode     string `json:"colourMode"`
	Sides          string `json:"sides"`
	Copies         int    `json:"copies"`
	PageStart      int    `json:"pageStart"`
	PageEnd        int    `json:"pageEnd"`
	Orientation    string `json:"orientation"`
	PagesPerSheet  int    `json:"pagesPerSheet"`
	Pages          string `json:"pagesJSON"`
	TotalMinor     int64  `json:"totalMinor"`
	PrintState     string `json:"printState"`
	PrintError     string `json:"printError"`
}
