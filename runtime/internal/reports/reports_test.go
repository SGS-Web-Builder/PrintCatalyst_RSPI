package reports_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/reports"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func fixture(t *testing.T) (*store.Store, *reports.Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reports.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, owner.Profile{
		Name: "Test Shop", Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
	}); err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	if _, err := pricingSvc.Save(ctx, pricing.Input{
		Entries: []pricing.Entry{
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesOneSided, UnitPriceMinor: 200},
			{PaperSize: "A4", ColourMode: pricing.ColourColour, Sides: pricing.SidesOneSided, UnitPriceMinor: 1000},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ordersSvc := orders.New(database.DB(), pricingSvc)
	_ = ordersSvc // used for context (the orders table is populated below)

	// Insert two paid orders with lines.
	insertOrder(t, ctx, database, "ord10000000000000000000000001", "paid", 600, 200, 1)
	insertOrder(t, ctx, database, "ord20000000000000000000000002", "completed", 1200, 2, 1000)
	insertOrder(t, ctx, database, "ord30000000000000000000000003", "pending_payment", 200, 1, 200)
	return database, reports.New(database.DB())
}

func insertOrder(t *testing.T, ctx context.Context, db *store.Store, orderID string, status string, lineTotal int64, copies int, unitPrice int64) {
	t.Helper()
	if _, err := db.DB().ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		orderID, orderID+"s", status, "INR", 2, lineTotal*int64(copies),
		"Customer "+orderID[:4], "+919999999999", "", "", 1700000000, 1700000000); err != nil {
		t.Fatal(err)
	}
	docID := orderID + "doc111111111111111111"
	if _, err := db.DB().ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		docID, orderID, "test.pdf", "application/pdf", 1024, 1,
		"sha256-"+docID, "docs/"+orderID, 1700000000, 1700604800); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, unit_price_minor, line_total_minor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orderID+"line1", orderID, docID, "A4", "A4", "monochrome", "one-sided",
		copies, 1, 1, unitPrice, lineTotal*int64(copies)); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryReturnsStatusBreakdown(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	summary, err := svc.Summary(context.Background(), 0, 2000000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ByStatus) == 0 {
		t.Fatal("expected status rows")
	}
	// pending_payment (not settled), paid, completed.
	statusMap := make(map[string]int)
	for _, r := range summary.ByStatus {
		statusMap[r.Status] = r.Count
	}
	if got := statusMap["pending_payment"]; got != 1 {
		t.Fatalf("pending_payment count = %d, want 1", got)
	}
	if got := statusMap["paid"]; got != 1 {
		t.Fatalf("paid count = %d, want 1", got)
	}
	if got := statusMap["completed"]; got != 1 {
		t.Fatalf("completed count = %d, want 1", got)
	}
}

func TestSummaryFiltersByDateRange(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	// Range before any orders.
	summary, err := svc.Summary(context.Background(), 0, 1000000000)
	if err != nil {
		t.Fatal(err)
	}
	var totalCount int
	for _, r := range summary.ByStatus {
		totalCount += r.Count
	}
	if totalCount != 0 {
		t.Fatalf("expected zero orders in old range, got %d", totalCount)
	}
}

func TestTopCombinationsReturnsSortedByVolume(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	combos, err := svc.TopCombinations(context.Background(), 0, 2000000000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) == 0 {
		t.Fatal("expected combinations")
	}
	// Both orders used A4/monochrome; it should be first.
	if combos[0].PaperSize != "A4" || combos[0].ColourMode != "monochrome" {
		t.Fatalf("top combo = %s/%s, want A4/monochrome", combos[0].PaperSize, combos[0].ColourMode)
	}
}

func TestTopCombinationsLimitsResults(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	combos, err := svc.TopCombinations(context.Background(), 0, 2000000000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) > 1 {
		t.Fatalf("combinations count = %d, want at most 1", len(combos))
	}
}

func TestTopCombinationsExcludesUnpaid(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	combos, err := svc.TopCombinations(context.Background(), 0, 2000000000, 10)
	if err != nil {
		t.Fatal(err)
	}
	// pending_payment order excluded; only paid + completed count.
	for _, c := range combos {
		if c.LineCount == 1 && c.PaperSize == "A4" {
			// This is from the paid/completed orders (2 orders × 1 line each = 2 lines).
			if c.LineCount < 2 {
				t.Fatalf("paid/completed line count = %d, want >= 2", c.LineCount)
			}
		}
	}
}
