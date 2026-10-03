package orders_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

var testProfile = owner.Profile{
	Name: "Campus Prints", Address: "Pune", Phone: "+919999999999",
	Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
}

// fixture opens a temporary database with licence, owner, business and pricing
// gates complete, which is the only state in which an order can be placed.
func fixture(t *testing.T) (*store.Store, *pricing.Service, *orders.Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "orders.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test verified licence"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, testProfile); err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	if _, err := pricingSvc.Save(ctx, pricing.Input{
		Entries: []pricing.Entry{
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesOneSided, UnitPriceMinor: 250},
			{PaperSize: "A4", ColourMode: pricing.ColourColour, Sides: pricing.SidesOneSided, UnitPriceMinor: 1500},
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesTwoSidedLongEdge, UnitPriceMinor: 400},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return database, pricingSvc, orders.New(database.DB(), pricingSvc)
}

// saveStubOrder creates a minimal order row so documents can have a valid FK.
// In production this happens during the upload step. Tests use this directly.
func saveStubOrder(t *testing.T, ctx context.Context, db *store.Store, orderID string) {
	t.Helper()
	// Use a unique share_token per stub. The orders schema enforces UNIQUE on
	// share_token; sharing the empty placeholder between stubs causes a
	// constraint failure even though the rows are otherwise independent.
	share := orderID + "share"
	if _, err := db.DB().ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		orderID, share, orders.StatusPendingPayment, "INR", 2, 0,
		"", "", "", "", 1700000000, 1700000000); err != nil {
		t.Fatal(err)
	}
}

// insertDocument inserts a document row so a test can reference it by ID.
func insertDocument(t *testing.T, ctx context.Context, db *store.Store, docID, orderID string, pageCount int) {
	t.Helper()
	_, err := db.DB().ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		docID, orderID, "test.pdf", "application/pdf", 1024, pageCount,
		"deadbeef"+docID, "documents/"+orderID+"/"+docID+".pdf",
		1700000000, 1700604800)
	if err != nil {
		t.Fatal(err)
	}
}

func TestQuoteComputesSingleSidedAndMultipliedCopies(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "abc12345abc12345abc12345abc12345"
	saveStubOrder(t, ctx, database, orderID)
	insertDocument(t, ctx, database, "doc11111doc11111doc11111doc11111", orderID, 5)

	quote, err := svc.ComputeQuote(ctx, map[string]int{
		"doc11111doc11111doc11111doc11111": 5,
	}, orders.QuoteRequest{Lines: []orders.QuoteLineRequest{{
		DocumentID:     "doc11111doc11111doc11111doc11111",
		PaperSize:      "A4",
		ColourMode:     pricing.ColourMonochrome,
		Sides:          pricing.SidesOneSided,
		Copies:         2,
		PageRangeStart: 1,
		PageRangeEnd:   5,
	}}})
	if err != nil {
		t.Fatalf("ComputeQuote: %v", err)
	}
	// 5 pages × 2 copies = 10 sheets × 250 minor = 2500
	if got := quote.Lines[0].Sheets; got != 10 {
		t.Fatalf("sheets = %d, want 10", got)
	}
	if got := quote.Lines[0].LineTotalMinor; got != 2500 {
		t.Fatalf("line total = %d, want 2500", got)
	}
	if got := quote.TotalMinor; got != 2500 {
		t.Fatalf("total = %d, want 2500", got)
	}
	if quote.Currency != "INR" || quote.CurrencyMinorUnits != 2 {
		t.Fatalf("currency snapshot wrong: %s/%d", quote.Currency, quote.CurrencyMinorUnits)
	}
}

// fixture2 mirrors fixture() but exposes the database handle so tests can insert
// documents. It avoids the unused database returned by the public helper.
func fixture2(t *testing.T) (*store.Store, *pricing.Service, *orders.Service) {
	t.Helper()
	database, p, o := fixture(t)
	return database, p, o
}

func TestQuoteTwoSidedRoundsSheetsUp(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "bbb12345bbb12345bbb12345bbb12345"
	saveStubOrder(t, ctx, database, orderID)
	insertDocument(t, ctx, database, "doc22222doc22222doc22222doc22222", orderID, 5)

	// 5 pages, 1 copy, two-sided long edge → ceil(5/2) = 3 sheets
	quote, err := svc.ComputeQuote(ctx, map[string]int{
		"doc22222doc22222doc22222doc22222": 5,
	}, orders.QuoteRequest{Lines: []orders.QuoteLineRequest{{
		DocumentID:     "doc22222doc22222doc22222doc22222",
		PaperSize:      "A4",
		ColourMode:     pricing.ColourMonochrome,
		Sides:          pricing.SidesTwoSidedLongEdge,
		Copies:         1,
		PageRangeStart: 1,
		PageRangeEnd:   5,
	}}})
	if err != nil {
		t.Fatalf("ComputeQuote: %v", err)
	}
	if got := quote.Lines[0].Sheets; got != 3 {
		t.Fatalf("sheets = %d, want 3 (ceil 5/2)", got)
	}
	if got := quote.Lines[0].LineTotalMinor; got != 400*3 {
		t.Fatalf("line total = %d, want %d", got, 400*3)
	}
}

func TestQuotePageRangeBounds(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "ccc12345ccc12345ccc12345ccc12345"
	saveStubOrder(t, ctx, database, orderID)
	insertDocument(t, ctx, database, "doc33333doc33333doc33333doc33333", orderID, 10)

	// Customer selected pages 5-8 (4 pages) of a 10-page document.
	quote, err := svc.ComputeQuote(ctx, map[string]int{
		"doc33333doc33333doc33333doc33333": 10,
	}, orders.QuoteRequest{Lines: []orders.QuoteLineRequest{{
		DocumentID:     "doc33333doc33333doc33333doc33333",
		PaperSize:      "A4",
		ColourMode:     pricing.ColourMonochrome,
		Sides:          pricing.SidesOneSided,
		Copies:         1,
		PageRangeStart: 5,
		PageRangeEnd:   8,
	}}})
	if err != nil {
		t.Fatalf("ComputeQuote: %v", err)
	}
	if got := quote.Lines[0].Sheets; got != 4 {
		t.Fatalf("sheets = %d, want 4 (5-8 = 4 pages)", got)
	}
}

func TestQuoteRejectsUnknownPricingCombination(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "ddd12345ddd12345ddd12345ddd12345"
	saveStubOrder(t, ctx, database, orderID)
	insertDocument(t, ctx, database, "doc44444doc44444doc44444doc44444", orderID, 1)

	_, err := svc.ComputeQuote(ctx, map[string]int{
		"doc44444doc44444doc44444doc44444": 1,
	}, orders.QuoteRequest{Lines: []orders.QuoteLineRequest{{
		DocumentID:     "doc44444doc44444doc44444doc44444",
		PaperSize:      "NoSuchPaper",
		ColourMode:     pricing.ColourMonochrome,
		Sides:          pricing.SidesOneSided,
		Copies:         1,
		PageRangeStart: 1,
		PageRangeEnd:   1,
	}}})
	if err == nil {
		t.Fatal("expected error for unknown pricing combination")
	}
}

func TestQuoteAppliesPricingTiers(t *testing.T) {
	// Build a fixture with a tier discount for high-volume A4 monochrome.
	path := filepath.Join(t.TempDir(), "tier-quote.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, testProfile); err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	if _, err := pricingSvc.Save(ctx, pricing.Input{
		Entries: []pricing.Entry{
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesOneSided, UnitPriceMinor: 250, Tiers: []pricing.Tier{
				{MinQuantity: 50, UnitPriceMinor: 200},
				{MinQuantity: 200, UnitPriceMinor: 150},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	svc := orders.New(database.DB(), pricingSvc)
	orderID := "eee12345eee12345eee12345eee12345"
	saveStubOrder(t, ctx, database, orderID)
	insertDocument(t, ctx, database, "doc55555doc55555doc55555doc55555", orderID, 100)

	// 100 sheets × ₹2.00 (tier from 50+ saves this rate) = 20000
	quote, err := svc.ComputeQuote(ctx, map[string]int{
		"doc55555doc55555doc55555doc55555": 100,
	}, orders.QuoteRequest{Lines: []orders.QuoteLineRequest{{
		DocumentID:     "doc55555doc55555doc55555doc55555",
		PaperSize:      "A4",
		ColourMode:     pricing.ColourMonochrome,
		Sides:          pricing.SidesOneSided,
		Copies:         100,
		PageRangeStart: 1,
		PageRangeEnd:   1,
	}}})
	if err != nil {
		t.Fatalf("ComputeQuote: %v", err)
	}
	if got := quote.Lines[0].UnitPriceMinor; got != 200 {
		t.Fatalf("unit price = %d, want 200 (tier from 50+)", got)
	}
	if got := quote.Lines[0].LineTotalMinor; got != 20000 {
		t.Fatalf("line total = %d, want 20000", got)
	}
}

func TestCreateOrderPersistsLinesAndRejectsEmptyLines(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()

	// Empty lines is an error.
	if _, err := svc.CreateOrder(ctx, orders.CreateOrderRequest{}, nil); err == nil {
		t.Fatal("expected error for empty lines")
	}

	// Set up a real document so the line's FK to documents is satisfied.
	orderID := "fff12345fff12345fff12345fff12345"
	saveStubOrder(t, ctx, database, orderID)
	docID := "doc66666doc66666doc66666doc66666"
	insertDocument(t, ctx, database, docID, orderID, 5)

	// Valid create.
	order, err := svc.CreateOrder(ctx, orders.CreateOrderRequest{
		Lines: []orders.QuoteLineRequest{{
			DocumentID:     docID,
			PaperSize:      "A4",
			ColourMode:     pricing.ColourMonochrome,
			Sides:          pricing.SidesOneSided,
			Copies:         2,
			PageRangeStart: 1,
			PageRangeEnd:   5,
		}},
		CustomerName:  "Ravi Sharma",
		CustomerPhone: "+919876543210",
		CustomerEmail: "ravi@example.com",
		CustomerNotes: "Please double-check the cover page",
	}, []orders.QuoteLine{{
		DocumentID:     docID,
		PaperSize:      "A4",
		ColourMode:     pricing.ColourMonochrome,
		Sides:          pricing.SidesOneSided,
		Sheets:         10,
		UnitPriceMinor: 250,
		LineTotalMinor: 2500,
	}})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.Status != orders.StatusPendingPayment {
		t.Fatalf("status = %s, want pending_payment", order.Status)
	}
	if order.TotalMinor != 2500 {
		t.Fatalf("total = %d, want 2500", order.TotalMinor)
	}
	if order.CustomerName != "Ravi Sharma" {
		t.Fatalf("name = %q", order.CustomerName)
	}

	// Get + List round-trip.
	loaded, err := svc.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != order.ID {
		t.Fatalf("loaded ID = %s, want %s", loaded.ID, order.ID)
	}
	if len(loaded.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(loaded.Lines))
	}
	if loaded.Lines[0].DocumentID != docID {
		t.Fatalf("line document = %s, want %s", loaded.Lines[0].DocumentID, docID)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range list {
		if o.ID == order.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("order %s not in list", order.ID)
	}
}

func TestGetReturnsErrNotFoundForUnknownID(t *testing.T) {
	_, _, svc := fixture(t)
	_, err := svc.Get(context.Background(), "ff12345ff12345ff12345ff12345ff")
	if err != orders.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateOrderRequiresCustomerNameAndPhone(t *testing.T) {
	_, _, svc := fixture(t)
	_, err := svc.CreateOrder(context.Background(), orders.CreateOrderRequest{
		Lines: []orders.QuoteLineRequest{{
			DocumentID: "doc", PaperSize: "A4", ColourMode: pricing.ColourMonochrome,
			Sides: pricing.SidesOneSided, Copies: 1, PageRangeStart: 1, PageRangeEnd: 1,
		}},
		CustomerPhone: "+919999999999",
	}, []orders.QuoteLine{{UnitPriceMinor: 250, LineTotalMinor: 250}})
	if err == nil {
		t.Fatal("expected error for missing customer name")
	}
	_, err = svc.CreateOrder(context.Background(), orders.CreateOrderRequest{
		Lines: []orders.QuoteLineRequest{{
			DocumentID: "doc", PaperSize: "A4", ColourMode: pricing.ColourMonochrome,
			Sides: pricing.SidesOneSided, Copies: 1, PageRangeStart: 1, PageRangeEnd: 1,
		}},
		CustomerName: "Name",
	}, []orders.QuoteLine{{UnitPriceMinor: 250, LineTotalMinor: 250}})
	if err == nil {
		t.Fatal("expected error for missing customer phone")
	}
}

// ---- Phase 3F: status transitions and invoices ----

func TestSetStatusFollowsAllowedTransitions(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "ststus1ststus1ststus1ststus1st1"
	saveStubOrder(t, ctx, database, orderID)
	docID := "docinv1docinv1docinv1docinv1docinv"
	insertDocument(t, ctx, database, docID, orderID, 1)

	// unknown target status is rejected.
	if _, err := svc.SetStatus(ctx, orderID, "bogus", "owner"); err == nil {
		t.Fatal("expected error for unknown target status")
	}
	// empty actor rejected.
	if _, err := svc.SetStatus(ctx, orderID, orders.StatusPaid, " "); err == nil {
		t.Fatal("expected error for empty actor")
	}
	// illegal transition (pending_payment → dispatched) is rejected.
	if _, err := svc.SetStatus(ctx, orderID, orders.StatusDispatched, "owner"); err != orders.ErrInvalidTransition {
		t.Fatalf("got %v, want ErrInvalidTransition", err)
	}
	// legal transition: pending_payment → paid.
	o, err := svc.SetStatus(ctx, orderID, orders.StatusPaid, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != orders.StatusPaid {
		t.Fatalf("status = %s, want paid", o.Status)
	}
	// legal: paid → dispatched.
	o, err = svc.SetStatus(ctx, orderID, orders.StatusDispatched, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != orders.StatusDispatched {
		t.Fatalf("status = %s, want dispatched", o.Status)
	}
	// legal: dispatched → completed.
	o, err = svc.SetStatus(ctx, orderID, orders.StatusCompleted, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != orders.StatusCompleted {
		t.Fatalf("status = %s, want completed", o.Status)
	}
	// completed is terminal; further transitions fail.
	if _, err := svc.SetStatus(ctx, orderID, orders.StatusCancelled, "owner"); err != orders.ErrInvalidTransition {
		t.Fatalf("terminal state got %v, want ErrInvalidTransition", err)
	}
}

func TestSetStatusWritesAuditEvent(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "audit12audit12audit12audit12aud12"
	saveStubOrder(t, ctx, database, orderID)
	if _, err := svc.SetStatus(ctx, orderID, orders.StatusPaid, "merchant"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE event_type=?`, orders.EventPaid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit events = %d, want 1", count)
	}
}

func TestIssueInvoiceAllocatesSequenceAndPersistsMerchantSnapshot(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()

	orderID := "inv123inv123inv123inv123inv123"
	saveStubOrder(t, ctx, database, orderID)
	docID := "invdoc1invdoc1invdoc1invdoc1inv1"
	insertDocument(t, ctx, database, docID, orderID, 5)
	// Populate total + lines so IssueInvoice has something to invoice.
	if _, err := database.DB().ExecContext(ctx, `UPDATE orders SET total_minor=? WHERE id=?`, 1500, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides, copies, page_range_start, page_range_end, unit_price_minor, line_total_minor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"line1inv1line1inv1line1inv1line1in1", orderID, docID, "A4", "A4", "monochrome", "one-sided", 1, 1, 5, 250, 1500); err != nil {
		t.Fatal(err)
	}
	// Move the order to paid so IssueInvoice can be called.
	if _, err := svc.SetStatus(ctx, orderID, orders.StatusPaid, "owner"); err != nil {
		t.Fatal(err)
	}

	inv, err := svc.IssueInvoice(ctx, orderID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Number != 1 {
		t.Fatalf("number = %d, want 1", inv.Number)
	}
	if inv.Merchant.Name != "Campus Prints" {
		t.Fatalf("merchant name = %s, want Campus Prints", inv.Merchant.Name)
	}
	if inv.TotalMinor == 0 {
		t.Fatal("total = 0, want non-zero")
	}
	if len(inv.Lines) == 0 {
		t.Fatal("expected invoice lines")
	}
	// Second issue must be refused (one invoice per order).
	if _, err := svc.IssueInvoice(ctx, orderID, "owner"); err == nil {
		t.Fatal("expected error issuing a second invoice")
	}

	// Second invoice for a different paid order advances the sequence.
	orderID2 := "inv456inv456inv456inv456inv456"
	saveStubOrder(t, ctx, database, orderID2)
	insertDocument(t, ctx, database, "invdoc2invdoc2invdoc2invdoc2inv2", orderID2, 5)
	if _, err := database.DB().ExecContext(ctx, `UPDATE orders SET total_minor=? WHERE id=?`, 3000, orderID2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetStatus(ctx, orderID2, orders.StatusPaid, "owner"); err != nil {
		t.Fatal(err)
	}
	inv2, err := svc.IssueInvoice(ctx, orderID2, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if inv2.Number != 2 {
		t.Fatalf("number = %d, want 2", inv2.Number)
	}
}

func TestIssueInvoiceRequiresPaidStatus(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "prepaidprepaidprepaidprepaidprep1"
	saveStubOrder(t, ctx, database, orderID)
	docID := "prepaid2prepaid2prepaid2prepaid2pre2"
	insertDocument(t, ctx, database, docID, orderID, 1)
	// Order is still pending_payment. Issuing an invoice is rejected.
	if _, err := svc.IssueInvoice(ctx, orderID, "owner"); err != orders.ErrInvalidTransition {
		t.Fatalf("got %v, want ErrInvalidTransition", err)
	}
}

func TestListInvoicesAndGetByOrder(t *testing.T) {
	database, _, svc := fixture2(t)
	defer database.Close()
	ctx := context.Background()
	orderID := "list123list123list123list123list1"
	saveStubOrder(t, ctx, database, orderID)
	insertDocument(t, ctx, database, "listdoc1listdoc1listdoc1listdoc1", orderID, 1)
	if _, err := svc.SetStatus(ctx, orderID, orders.StatusPaid, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueInvoice(ctx, orderID, "owner"); err != nil {
		t.Fatal(err)
	}
	invs, err := svc.ListInvoices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want 1", len(invs))
	}
	got, err := svc.GetInvoiceForOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 1 {
		t.Fatalf("number = %d, want 1", got.Number)
	}
}
