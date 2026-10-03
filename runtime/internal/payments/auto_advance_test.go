// Tests that the payments service auto-advances an order from
// pending_payment → paid when a payment intent is captured.
//
// This is the instant-print trigger: the dispatcher's 20ms poll loop
// picks up paid orders and submits them to the printer backend. The
// test guards the wiring so a future refactor of either side does
// not silently break the connection.
package payments_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

const testOrderID = "ord-auto-advance-1"

// TestAutoAdvanceOrderToPaidOnCapture verifies the payments service
// advances the linked order to "paid" when an intent is captured via
// the ManualApprove path. The dispatcher polls every 20 ms so the
// order should reach "paid" well within 500 ms of the capture call
// returning.
func TestAutoAdvanceOrderToPaidOnCapture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "auto-advance.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	licensingSvc, err := licensing.New(database.DB(), t.TempDir(),
		licensing.WithEncryptionSeed(func() []byte { return []byte("auto-advance-seed") }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := licensingSvc.Ensure(ctx); err != nil {
		t.Fatal(err)
	}

	// Seed a minimum-viable order: business profile, order, document,
	// order_line — all the foreign keys the migrations expect.
	now := time.Now().Unix()
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO business_profile (singleton, profile_json, updated_at)
VALUES (1, '{"currency":"INR"}', ?)`, now); err != nil {
		t.Fatalf("seed business profile: %v", err)
	}
	// Insert the order first so the documents FK is satisfied.
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor,
	customer_name, customer_phone, customer_email, customer_notes, payment_method,
	created_at, updated_at, submitted_at, portal_gate)
VALUES (?, 'tok-auto', ?, 'INR', 2, 100, 'Test', '+91', '', '', 'cash_on_counter', ?, ?, ?, 0)`,
		testOrderID, orders.StatusPendingPayment, now, now, now); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES ('doc-auto', ?, 'doc.pdf', 'application/pdf', 10, 1, 'abc', 'documents/auto/doc.pdf', ?, ?)`,
		testOrderID, now, now+86400); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO order_lines (id, order_id, document_id, paper_size, paper_key, colour_mode, sides,
	copies, page_range_start, page_range_end, unit_price_minor, line_total_minor)
VALUES ('line-auto', ?, 'doc-auto', 'A4', 'A4', 'monochrome', 'one-sided', 1, 1, 1, 100, 100)`,
		testOrderID); err != nil {
		t.Fatalf("seed order_line: %v", err)
	}

	// Now wire the orders service and payments service together.
	pricingSvc := pricing.New(database.DB())
	ordersSvc := orders.New(database.DB(), pricingSvc)
	svc, err := payments.New(database.DB(), t.TempDir(),
		payments.WithOrders(ordersSvc),
		payments.WithEncryptionSeed(func() []byte { return []byte("auto-advance-seed") }),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a payment intent that the manual approval will transition.
	rfcNow := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO payment_intents (id, order_id, installation_id, provider_id, amount_minor, currency, currency_minor_units, status, idempotency_key, created_at, updated_at)
VALUES ('int-auto', ?, 'inst', 'prov-auto', 100, 'INR', 2, 'redirected', 'auto-1', ?, ?)`,
		testOrderID, rfcNow, rfcNow); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if _, err := database.DB().ExecContext(ctx, `
INSERT INTO payment_providers (id, kind, display_name, enabled, is_default, config_json, created_at, updated_at)
VALUES ('prov-auto', 'manual', 'Cash', 1, 1, '{}', ?, ?)`,
		rfcNow, rfcNow); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	if _, err := svc.ManualApprove(ctx, "int-auto", payments.MethodCash, "ref-1", 100, "INR", "tester", "auto-advance"); err != nil {
		t.Fatalf("ManualApprove: %v", err)
	}

	// advanceOrderToPaid runs in a goroutine; poll for up to 500 ms.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		var status string
		if err := database.DB().QueryRowContext(ctx, `SELECT status FROM orders WHERE id=?`, testOrderID).Scan(&status); err == nil {
			if status == orders.StatusPaid {
				return // success
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	var finalStatus string
	_ = database.DB().QueryRowContext(ctx, `SELECT status FROM orders WHERE id=?`, testOrderID).Scan(&finalStatus)
	t.Fatalf("order status = %q, want paid (auto-advance did not fire)", finalStatus)
}
