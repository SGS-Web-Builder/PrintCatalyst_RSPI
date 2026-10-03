package localserver

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
)

// portalGuard accepts local-network customers and enforces same-origin writes.
// Forwarded host/protocol headers are trusted only from a loopback proxy when
// a customer origin is configured; owner routes use a separate local guard.
func (s *Server) portalGuard(w http.ResponseWriter, r *http.Request, requireJSON bool) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Direct LAN requests use their actual host and scheme. Forwarded requests
	// are accepted only from the local connector for the configured public host.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	expectedOrigin := scheme + "://" + r.Host
	forwarded := false
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "CF-Connecting-IP", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if r.Header.Get(h) != "" {
			forwarded = true
		}
	}
	if forwarded {
		origin := s.PublicOrigin()
		public, err := url.Parse(origin)
		if err != nil || public.Host == "" || public.Scheme != "https" || !isLoopbackClient(r.RemoteAddr) {
			portalJSONError(w, "forwarded requests are not accepted on the portal", 403)
			return false
		}
		host := r.Host
		if values := r.Header.Values("X-Forwarded-Host"); len(values) > 0 {
			if len(values) != 1 {
				portalJSONError(w, "invalid forwarded host", 403)
				return false
			}
			host = values[0]
		}
		if !strings.EqualFold(host, public.Host) {
			portalJSONError(w, "forwarded host does not match the configured portal domain", 403)
			return false
		}
		if values := r.Header.Values("X-Forwarded-Proto"); len(values) > 0 && (len(values) != 1 || values[0] != public.Scheme) {
			portalJSONError(w, "forwarded protocol does not match the configured portal domain", 403)
			return false
		}
		// Client-IP headers are metadata only, never authentication or origin input.
		expectedOrigin = origin
	}

	// Require same-origin for write methods to prevent cross-site form submissions.
	if r.Method != "GET" && r.Method != "HEAD" {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != expectedOrigin {
			portalJSONError(w, "same-origin access required", 403)
			return false
		}
		if requireJSON {
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" {
				portalJSONError(w, "JSON required", 415)
				return false
			}
		}
	}
	return true
}

// portalDecode parses a JSON body with a small limit, rejecting unknown fields so
// a future field addition does not silently change the meaning of a present one.
func portalDecode(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		portalJSONError(w, "invalid JSON request", 400)
		return false
	}
	// Ensure exactly one JSON object.
	if err := decoder.Decode(new(any)); err != io.EOF {
		portalJSONError(w, "one JSON object required", 400)
		return false
	}
	return true
}

func portalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, orders.ErrNotFound):
		portalJSONError(w, err.Error(), 404)
	default:
		portalJSONError(w, err.Error(), 400)
	}
}

// portalJSONError sends a JSON-formatted error response to match what the
// portal frontend expects. Every portal endpoint must return JSON, never
// plain text, because the frontend always calls JSON.parse() on responses.
func portalJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// portalPageAssets serves the customer-facing portal UI. The portal is embedded in
// the binary and served without authentication — customers access it through the
// tunnel without a session.
//
//go:embed web/portal/*
var portalPageAssets embed.FS

func (s *Server) registerPortal(mux *http.ServeMux) {
	docs := documents.New(s.files, s.db)

	// portalReady is the production-ready gate the portal write endpoints
	// must pass before they accept a customer request. The portal page
	// itself is still served so the customer can see a friendly error,
	// but the upload, quote and order endpoints reject any request that
	// arrives before every provisioning gate is satisfied. This matches
	// the rule the literal `POST /api/v1/orders` endpoint in server.go
	// already enforces, and stops a half-configured installation from
	// silently accepting customer uploads.
	portalReady := func(w http.ResponseWriter, r *http.Request) bool {
		reason := s.notReadyReason(r.Context())
		if reason == "" {
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "reason": reason, "error": reason})
		return false
	}

	mux.HandleFunc("GET /portal/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sub, err := fs.Sub(portalPageAssets, "web/portal")
		if err != nil {
			portalJSONError(w, "portal unavailable", 500)
			return
		}
		// FileServer is rooted at web/portal; strip the URL prefix so requests
		// for /portal/portal.css map to portal.css inside the FS root.
		http.StripPrefix("/portal", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
	})

	// Public options — the portal exposes the intersection of the merchant's
	// price book and the verified printer fleet's capabilities. A paper size
	// or colour mode that is priced but cannot be fulfilled by an enabled,
	// verified printer must not appear in the customer UI. When no printer
	// has been verified yet, the projection collapses to an empty set so the
	// customer never sees a combination the merchant cannot actually print.
	mux.HandleFunc("GET /api/v1/portal/options", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		if !portalReady(w, r) {
			return
		}
		if s.db == nil {
			portalJSONError(w, "options unavailable", 503)
			return
		}

		result, err := s.portalOptions(r.Context(), r.URL.Query().Get("serviceId"), r.URL.Query().Get("printerId"))
		if err != nil {
			portalJSONError(w, err.Error(), 400)
			return
		}
		writeJSON(w, result)
	})
	s.registerPortalDocuments(mux)

	// Document upload — multipart form.
	mux.HandleFunc("POST /api/v1/portal/uploads", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		if !portalReady(w, r) {
			return
		}
		if r.ContentLength > 600<<20 {
			portalJSONError(w, "request too large; total upload must be under 600 MB", 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 600<<20)
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			portalJSONError(w, "parse multipart form: "+err.Error(), 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		files := r.MultipartForm.File["files"]
		if len(files) == 0 {
			portalJSONError(w, "at least one file is required", 400)
			return
		}
		if len(files) > 10 {
			portalJSONError(w, "at most 10 files may be uploaded per request", 400)
			return
		}

		currencyCode, mu, err := s.loadCurrency(r.Context())
		if err != nil {
			portalJSONError(w, "business currency unavailable", 503)
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			portalJSONError(w, "upload storage unavailable", 503)
			return
		}
		defer tx.Rollback()
		batchDocs := documents.NewWithTransaction(s.files, tx)
		committed := false
		savedPaths := []string{}
		defer func() {
			if !committed {
				for _, path := range savedPaths {
					_ = docs.Delete(context.Background(), path)
				}
			}
		}()
		orderID := r.URL.Query().Get("orderId")
		token := ""
		if orderID != "" {
			var status string
			var submitted int64
			if err := tx.QueryRowContext(r.Context(), "SELECT share_token,status,submitted_at FROM orders WHERE id=?", orderID).Scan(&token, &status, &submitted); err != nil || token != r.Header.Get("X-Upload-Token") {
				portalJSONError(w, "upload session not found", 404)
				return
			}
			if submitted != 0 || status != orders.StatusPendingPayment {
				portalJSONError(w, "order has already been submitted", 409)
				return
			}
		} else {
			orderID, err = portalRandomID()
			if err != nil {
				portalJSONError(w, "internal error", 500)
				return
			}
			token, err = portalRandomID()
			if err != nil {
				portalJSONError(w, "internal error", 500)
				return
			}
			now := time.Now().Unix()
			_, err = tx.ExecContext(r.Context(), `INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,customer_email,customer_notes,created_at,updated_at,portal_gate,submitted_at) VALUES(?,?,'pending_payment',?,?,0,'','','','',?,?,0,0)`, orderID, token, currencyCode, mu, now, now)
			if err != nil {
				portalJSONError(w, "create upload session failed", 500)
				return
			}
		}

		type uploadedFile struct {
			DocumentID       string `json:"documentId"`
			OriginalFilename string `json:"originalFilename"`
			MIMEType         string `json:"mimeType"`
			SizeBytes        int64  `json:"sizeBytes"`
			PageCount        int    `json:"pageCount"`
			SHA256           string `json:"sha256"`
		}
		var uploaded []uploadedFile
		for _, hdr := range files {
			// Validate per-file size before parsing.
			if hdr.Size > documents.MaxFileSize {
				portalJSONError(w, fmt.Sprintf("file %q exceeds the %d byte limit", hdr.Filename, documents.MaxFileSize), 400)
				return
			}
			doc, err := batchDocs.Save(r.Context(), orderID, hdr)
			if err != nil {
				portalJSONError(w, err.Error(), 400)
				return
			}
			savedPaths = append(savedPaths, doc.StoragePath)
			// A retried batch or repeated file selection reuses the existing file.
			var previous string
			if tx.QueryRowContext(r.Context(), "SELECT id FROM documents WHERE order_id=? AND sha256=? AND original_filename=? AND id<>? AND purged_at=0 LIMIT 1", orderID, doc.SHA256, doc.OriginalFilename, doc.ID).Scan(&previous) == nil {
				if _, err := tx.ExecContext(r.Context(), "DELETE FROM documents WHERE id=?", doc.ID); err != nil {
					portalJSONError(w, "upload storage failed", 500)
					return
				}
				if err := docs.Delete(r.Context(), doc.StoragePath); err != nil {
					portalJSONError(w, "upload cleanup failed", 500)
					return
				}
				continue
			}
			uploaded = append(uploaded, uploadedFile{
				DocumentID:       doc.ID,
				OriginalFilename: doc.OriginalFilename,
				MIMEType:         doc.MIMEType,
				SizeBytes:        doc.SizeBytes,
				PageCount:        doc.PageCount,
				SHA256:           doc.SHA256,
			})
		}
		uploaded = nil
		rows, err := tx.QueryContext(r.Context(), "SELECT id,original_filename,mime_type,size_bytes,page_count,sha256 FROM documents WHERE order_id=? AND purged_at=0 ORDER BY created_at,id", orderID)
		if err != nil {
			portalJSONError(w, "upload storage failed", 500)
			return
		}
		for rows.Next() {
			var f uploadedFile
			if err = rows.Scan(&f.DocumentID, &f.OriginalFilename, &f.MIMEType, &f.SizeBytes, &f.PageCount, &f.SHA256); err != nil {
				break
			}
			uploaded = append(uploaded, f)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			portalJSONError(w, "upload storage failed", 500)
			return
		}
		if len(uploaded) > 10 {
			portalJSONError(w, "at most 10 files per order", 400)
			return
		}
		if err = tx.Commit(); err != nil {
			portalJSONError(w, "upload could not be saved", 500)
			return
		}
		committed = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"orderId": orderID, "uploadToken": token, "files": uploaded})

	})

	// Server-side authoritative quote.
	mux.HandleFunc("POST /api/v1/portal/quote", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, true) {
			return
		}
		if !portalReady(w, r) {
			return
		}
		var req orders.QuoteRequest
		if !portalDecode(w, r, &req, 64<<10) {
			return
		}
		// Collect page counts from the uploaded documents.
		docPages, err := s.loadDocumentPages(r.Context(), req)
		if err != nil {
			portalError(w, err)
			return
		}
		quote, err := s.orders.ComputeQuote(r.Context(), docPages, req)
		if err != nil {
			portalError(w, err)
			return
		}
		if err := s.validatePortalLines(r.Context(), req.ServiceID, req.PrimaryPrinterID, req.Lines); err != nil {
			portalJSONError(w, err.Error(), 409)
			return
		}
		_ = json.NewEncoder(w).Encode(quote)
	})

	// Create final order from quote lines.
	mux.HandleFunc("POST /api/v1/portal/orders", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, true) {
			return
		}
		if !portalReady(w, r) {
			return
		}
		var req struct {
			OrderID          string                    `json:"orderId"`
			ServiceID        string                    `json:"serviceId"`
			DiscountCode     string                    `json:"discountCode"`
			Lines            []orders.QuoteLineRequest `json:"lines"`
			CustomerName     string                    `json:"customerName"`
			CustomerPhone    string                    `json:"customerPhone"`
			CustomerEmail    string                    `json:"customerEmail"`
			CustomerNotes    string                    `json:"customerNotes"`
			PaymentMethod    string                    `json:"paymentMethod"`
			MergeLines       bool                      `json:"mergeLines"`
			PrimaryPrinterID string                    `json:"primaryPrinterId"`
		}
		if !portalDecode(w, r, &req, 64<<10) {
			return
		}
		if req.OrderID == "" {
			portalJSONError(w, "order id is required", 400)
			return
		}
		// Verify the stub order exists and is still in the draft state. A
		// previously submitted order carries submitted_at > 0 even when the
		// status is still pending_payment (the customer has not paid yet), so
		// checking submitted_at is the only reliable way to reject a duplicate
		// submission. Without this guard, a second POST to /portal/orders
		// would append duplicate order_lines to the saved order.
		var status string
		var submittedAt int64
		err := s.db.QueryRowContext(r.Context(), `SELECT status, submitted_at FROM orders WHERE id=?`, req.OrderID).Scan(&status, &submittedAt)
		if err != nil {
			portalJSONError(w, "order not found", 404)
			return
		}
		if submittedAt != 0 || status != orders.StatusPendingPayment {
			portalJSONError(w, "order has already been submitted", 409)
			return
		}

		for _, line := range req.Lines {
			var belongs bool
			if err := s.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM documents WHERE id=? AND order_id=? AND purged_at=0)", line.DocumentID, req.OrderID).Scan(&belongs); err != nil || !belongs {
				portalJSONError(w, "document does not belong to this upload session", 400)
				return
			}
		}
		if req.ServiceID != "" {
			var available bool
			err := s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM services s JOIN service_printers sp ON sp.service_id=s.id JOIN printers p ON p.id=sp.printer_id WHERE s.id=? AND s.enabled=1 AND p.enabled=1 AND p.removed_at IS NULL AND (?='' OR p.id=?))`, req.ServiceID, req.PrimaryPrinterID, req.PrimaryPrinterID).Scan(&available)
			if err != nil || !available {
				portalJSONError(w, "selected service has no available assigned printer", 409)
				return
			}
		}
		if err := s.validatePortalLines(r.Context(), req.ServiceID, req.PrimaryPrinterID, req.Lines); err != nil {
			portalJSONError(w, err.Error(), 409)
			return
		}
		details, detailsErr := s.readCustomerDetails(r.Context())
		if detailsErr != nil {
			portalJSONError(w, "Could not load customer details settings", 500)
			return
		}
		if err := details.apply(map[string]*string{"customerName": &req.CustomerName, "customerPhone": &req.CustomerPhone, "customerEmail": &req.CustomerEmail, "customerNotes": &req.CustomerNotes}); err != nil {
			portalJSONError(w, err.Error(), 400)
			return
		}
		// Compute the authoritative quote.
		docPages, err := s.loadDocumentPages(r.Context(), orders.QuoteRequest{Lines: req.Lines, ServiceID: req.ServiceID, DiscountCode: req.DiscountCode, PrimaryPrinterID: req.PrimaryPrinterID})
		if err != nil {
			portalError(w, err)
			return
		}
		quote, err := s.orders.ComputeQuote(r.Context(), docPages, orders.QuoteRequest{Lines: req.Lines, ServiceID: req.ServiceID, DiscountCode: req.DiscountCode, PrimaryPrinterID: req.PrimaryPrinterID})
		if err != nil {
			portalError(w, err)
			return
		}

		// Resolve the payment method. Default to cash_on_counter
		// (the legacy flow that requires the merchant to approve
		// manually). The "razorpay" choice is rejected when no
		// enabled merchant Razorpay provider is configured so a
		// customer never sees a "Pay online" button that would
		// then 503.
		paymentMethod := strings.TrimSpace(req.PaymentMethod)
		if s.pickup != nil && paymentMethod != orders.PaymentMethodRazorpay {
			portalJSONError(w, "Kiosk orders require verified online payment. Select online payment to continue.", 409)
			return
		}
		if paymentMethod == "" {
			paymentMethod = orders.PaymentMethodCashOnCounter
		}
		if paymentMethod != orders.PaymentMethodCashOnCounter && paymentMethod != orders.PaymentMethodRazorpay {
			portalJSONError(w, fmt.Sprintf("unsupported payment method %q", req.PaymentMethod), 400)
			return
		}
		buttons, err := s.readPortalPaymentButtons(r.Context())
		if err != nil {
			portalJSONError(w, "Could not load payment options", 500)
			return
		}
		if (paymentMethod == orders.PaymentMethodCashOnCounter && !buttons.CashEnabled) || (paymentMethod == orders.PaymentMethodRazorpay && !buttons.OnlineEnabled) {
			portalJSONError(w, "This payment option has been disabled by the shop. Refresh to choose an available option.", 409)
			return
		}
		if paymentMethod == orders.PaymentMethodRazorpay && s.payments == nil {
			portalJSONError(w, "online payment is not configured", 409)
			return
		}
		if paymentMethod == orders.PaymentMethodRazorpay && s.payments != nil {
			providers, err := s.payments.ListProviders(r.Context())
			if err != nil {
				portalError(w, err)
				return
			}
			razorpayReady := false
			for _, p := range providers {
				if p.Enabled && p.Kind == payments.KindRazorpayMerchant {
					razorpayReady = true
					break
				}
			}
			if !razorpayReady {
				portalJSONError(w, "online payment is not configured; choose cash on counter", 409)
				return
			}
		}

		if paymentMethod == orders.PaymentMethodRazorpay && quote.TotalMinor == 0 {
			portalJSONError(w, "No online payment is needed. Choose Cash On Counter for this free order.", 400)
			return
		}
		// Create the order and update the stub atomically.
		// Preserve the draft's unguessable token so a lost submit response
		// can be recovered by the same browser without creating another order.
		var shareToken string
		if err := s.db.QueryRowContext(r.Context(), "SELECT share_token FROM orders WHERE id=?", req.OrderID).Scan(&shareToken); err != nil {
			portalJSONError(w, "order unavailable", 500)
			return
		}

		now := time.Now().Unix()
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			portalJSONError(w, "internal error", 500)
			return
		}
		defer tx.Rollback()

		discount, record, err := business.DiscountFor(r.Context(), tx, req.DiscountCode, quote.SubtotalMinor, now)
		if err != nil {
			portalJSONError(w, "discount is no longer available; request a new quote", 409)
			return
		}
		if discount != quote.DiscountMinor {
			portalJSONError(w, "discount changed; request a new quote", 409)
			return
		}
		if record.ID != "" {
			if _, err := tx.ExecContext(r.Context(), "UPDATE discounts SET times_used=times_used+1,updated_at=? WHERE id=?", now, record.ID); err != nil {
				portalJSONError(w, "save discount failed", 500)
				return
			}
		}
		// Update the stub order with real data and set submitted_at to the same now timestamp so a follow-up POST is
		// rejected by the guard above. The UPDATE is guarded by
		// submitted_at = 0 in addition to status = pending_payment so the
		// race window between the SELECT above and this UPDATE closes safely.
		updateResult, err := tx.ExecContext(r.Context(), `
UPDATE orders SET share_token=?, status=?, total_minor=?,
	customer_name=?, customer_phone=?, customer_email=?, customer_notes=?,
	payment_method=?, primary_printer_id=?, service_id=?, submitted_at=?, updated_at=?, discount_code=?,discount_minor=?,subtotal_minor=?
WHERE id=? AND status=? AND submitted_at=0`,
			shareToken, orders.StatusPendingPayment, quote.TotalMinor,
			strings.TrimSpace(req.CustomerName),
			strings.TrimSpace(req.CustomerPhone),
			strings.TrimSpace(req.CustomerEmail),
			strings.TrimSpace(req.CustomerNotes),
			paymentMethod,
			strings.TrimSpace(req.PrimaryPrinterID), req.ServiceID,
			now, now, quote.DiscountCode, quote.DiscountMinor, quote.SubtotalMinor, req.OrderID, orders.StatusPendingPayment)
		if err != nil {
			portalJSONError(w, "update order: "+err.Error(), 500)
			return
		}
		// Confirm the UPDATE actually modified a row. RowsAffected must be
		// read off the UPDATE itself; running a follow-up SELECT and
		// counting its result would always return 1 once the UPDATE has
		// landed in the same transaction, defeating the duplicate-submit
		// guard when two requests race past the SELECT above.
		n, err := updateResult.RowsAffected()
		if err != nil {
			portalJSONError(w, "verify order: "+err.Error(), 500)
			return
		}
		if n != 1 {
			portalJSONError(w, "order has already been submitted", 409)
			return
		}

		// Insert order lines. The stub was created with zero lines, so no DELETE needed.
		for _, ql := range quote.Lines {
			lineID, err := portalRandomID()
			if err != nil {
				portalJSONError(w, "internal error", 500)
				return
			}
			_, err = tx.ExecContext(r.Context(), `
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, orientation, pages_per_sheet,
	unit_price_minor, line_total_minor,selected_pages_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				lineID, req.OrderID, ql.DocumentID,
				ql.PaperSize, strings.ToUpper(strings.TrimSpace(ql.PaperSize)),
				ql.ColourMode, ql.Sides,
				ql.Copies, ql.PageRangeStart, ql.PageRangeEnd,
				ql.Orientation, ql.PagesPerSheet,
				ql.UnitPriceMinor, ql.LineTotalMinor, pageselection.Encode(ql.Pages))
			if err != nil {
				portalJSONError(w, "insert line: "+err.Error(), 500)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			portalJSONError(w, "commit order: "+err.Error(), 500)
			return
		}

		// Fire new-order notifications asynchronously (do not block the response).
		if s.notifications != nil {
			go func() {
				s.notifications.Notify(context.Background(), req.OrderID,
					strings.TrimSpace(req.CustomerName), quote.TotalMinor,
					quote.Currency, quote.CurrencyMinorUnits, now)
			}()
		}

		// When the customer chose Razorpay, eagerly create the payment
		// intent here so the response can carry the redirect URL and the
		// customer's browser can be sent straight to Razorpay without a
		// second round-trip. The intent id is the unique idempotency key
		// the payments service uses to deduplicate retried POSTs.
		var redirectURL string
		paymentError := ""
		if paymentMethod == orders.PaymentMethodRazorpay {
			paymentError = "Your order is saved. Retry online payment below."
		}
		if paymentMethod == orders.PaymentMethodRazorpay && s.payments != nil {
			providers, err := s.payments.ListProviders(r.Context())
			if err == nil {
				var providerID string
				for _, p := range providers {
					if p.Enabled && p.IsDefault && p.Kind == payments.KindRazorpayMerchant {
						providerID = p.ID
						break
					}
				}
				if providerID == "" {
					for _, p := range providers {
						if p.Enabled && p.Kind == payments.KindRazorpayMerchant {
							providerID = p.ID
							break
						}
					}
				}
				if providerID != "" {
					_, result, err := s.payments.CreateIntent(r.Context(), payments.IntentInput{
						ReturnURL:          s.portalPaymentReturnURL(r),
						OrderID:            req.OrderID,
						ProviderID:         providerID,
						AmountMinor:        quote.TotalMinor,
						Currency:           quote.Currency,
						CurrencyMinorUnits: quote.CurrencyMinorUnits,
						IdempotencyKey:     "order:" + req.OrderID,
						CustomerName:       req.CustomerName, CustomerPhone: req.CustomerPhone, CustomerEmail: req.CustomerEmail,
					})
					if err == nil {
						redirectURL = result.RedirectURL
						paymentError = ""
					} else {
						paymentError = "Your order is saved. Online payment is temporarily unavailable. Retry payment below."
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"orderId":       req.OrderID,
			"shareToken":    shareToken,
			"status":        orders.StatusPendingPayment,
			"paymentMethod": paymentMethod,
			"totalMinor":    quote.TotalMinor,
			"currency":      quote.Currency,
			"currencyMU":    quote.CurrencyMinorUnits,
			"redirectUrl":   redirectURL,
			"paymentError":  paymentError, "discountMinor": quote.DiscountMinor, "subtotalMinor": quote.SubtotalMinor,
		})
	})

	// POST /api/v1/portal/orders/{id}/pay — initiate the payment
	// flow for an order that the customer submitted with
	// paymentMethod="razorpay". The handler picks the merchant's
	// default Razorpay provider, creates a payment intent and
	// returns the redirect URL the customer's browser should follow.
	// The handler is idempotent: a second call returns the same
	// intent's URL (or a fresh one if the previous one expired).
	mux.HandleFunc("POST /api/v1/portal/orders/{id}/pay", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, true) {
			return
		}
		if !portalReady(w, r) {
			return
		}
		if s.payments == nil {
			portalJSONError(w, "payments service unavailable", http.StatusServiceUnavailable)
			return
		}
		orderID := r.PathValue("id")
		if orderID == "" {
			portalJSONError(w, "order id is required", 400)
			return
		}
		order, err := s.orders.Get(r.Context(), orderID)
		if err != nil {
			portalError(w, err)
			return
		}
		if order.Status != orders.StatusPendingPayment {
			portalJSONError(w, "order is not in pending_payment state", 409)
			return
		}
		if order.PaymentMethod != orders.PaymentMethodRazorpay {
			portalJSONError(w, "order is not configured for online payment", 400)
			return
		}
		// Resolve the merchant's default Razorpay provider. The
		// portal never asks the merchant to pick a provider — the
		// dashboard already configured one (or none).
		providers, err := s.payments.ListProviders(r.Context())
		if err != nil {
			portalError(w, err)
			return
		}
		var providerID string
		for _, p := range providers {
			if p.Enabled && p.IsDefault && p.Kind == payments.KindRazorpayMerchant {
				providerID = p.ID
				break
			}
		}
		if providerID == "" {
			// Fall back to any enabled Razorpay provider.
			for _, p := range providers {
				if p.Enabled && p.Kind == payments.KindRazorpayMerchant {
					providerID = p.ID
					break
				}
			}
		}
		if providerID == "" {
			portalJSONError(w, "no Razorpay provider is enabled; ask the merchant to configure one", 503)
			return
		}
		intent, result, err := s.payments.CreateIntent(r.Context(), payments.IntentInput{
			ReturnURL:          s.portalPaymentReturnURL(r),
			OrderID:            order.ID,
			ProviderID:         providerID,
			AmountMinor:        order.TotalMinor,
			Currency:           order.Currency,
			CurrencyMinorUnits: order.CurrencyMU,
			IdempotencyKey:     "order:" + order.ID,
			CustomerName:       order.CustomerName, CustomerPhone: order.CustomerPhone, CustomerEmail: order.CustomerEmail,
		})
		if err != nil {
			portalError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"orderId":     order.ID,
			"intentId":    intent.ID,
			"redirectUrl": result.RedirectURL,
			"expiresAt":   result.ExpiresAt,
		})
	})

	// Order status by ID — used for refresh after creation.
	mux.HandleFunc("GET /api/v1/portal/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s.orders == nil {
			portalJSONError(w, "order service unavailable", http.StatusServiceUnavailable)
			return
		}
		orderID := r.PathValue("id")
		if orderID == "" {
			portalJSONError(w, "order id is required", 400)
			return
		}
		// Load order; caller should show a clean error if not found.
		order, err := s.orders.Get(r.Context(), orderID)
		if err != nil {
			portalError(w, err)
			return
		}
		progress, err := s.orders.PrintProgress(r.Context(), orderID)
		if err != nil {
			portalError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"orderId":     order.ID,
			"printStatus": progress,
			"status":      order.Status,
			"totalMinor":  order.TotalMinor,
			"currency":    order.Currency,
			"currencyMU":  order.CurrencyMU,
			"lines":       order.Lines,
			"createdAt":   order.CreatedAt,
		})
	})
}

// loadDocumentPages looks up page counts for the documents referenced in a
// quote request by querying the documents table directly (the order ID for the
// upload session is needed). This is a lightweight helper that avoids a full
// orders service round-trip for page count lookups.
func (s *Server) loadDocumentPages(ctx context.Context, req orders.QuoteRequest) (map[string]int, error) {
	docIDs := make([]string, 0, len(req.Lines))
	for _, l := range req.Lines {
		if l.DocumentID == "" {
			continue
		}
		docIDs = append(docIDs, l.DocumentID)
	}
	if len(docIDs) == 0 {
		return nil, errors.New("no document ids provided")
	}
	placeholders := make([]byte, 0, len(docIDs)*2)
	args := make([]any, len(docIDs))
	for i, id := range docIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT id, page_count FROM documents WHERE purged_at=0 AND id IN (%s)`, placeholders)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := make(map[string]int, len(docIDs))
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		pages[id] = count
	}
	return pages, rows.Err()
}

func portalRandomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// sortedKeys returns the keys of m in lexical order. The portal options
// endpoint needs deterministic output so the customer's UI does not flicker
// between two loads of the same data.
func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// loadCurrency is a portal helper that reads the business profile's currency.
func (s *Server) loadCurrency(ctx context.Context) (string, int, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT profile_json FROM business_profile WHERE singleton=1`).Scan(&raw)
	if err != nil {
		return "INR", 2, nil // safe fallback
	}
	var p struct{ Currency string }
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "INR", 2, nil
	}
	code := strings.ToUpper(strings.TrimSpace(p.Currency))
	return code, s.currencyMU(code), nil
}

func (s *Server) currencyMU(code string) int {
	// Mirror the currency.MinorUnits logic inline so the portal package does
	// not need to import currency (which lives in the runtime module).
	switch code {
	case "INR":
		return 2
	case "KRW", "IDR", "VND":
		return 0
	case "JPY":
		return 0
	case "USD", "EUR", "GBP", "AUD", "CAD", "SGD", "AED":
		return 2
	default:
		return 2
	}
}

// Called only after portalGuard validates the request origin. Return to the
// same browser origin so its saved receipt remains available. The callback is
// navigation only: payment status still comes from verified server evidence.
func (s *Server) portalPaymentReturnURL(r *http.Request) string {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = s.PublicOrigin()
	}
	if origin == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		origin = scheme + "://" + r.Host
	}
	return strings.TrimRight(origin, "/") + "/portal/"
}
