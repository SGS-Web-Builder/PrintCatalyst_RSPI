package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenConfiguresSQLiteAndAppliesInitialSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "print-catalyst.sqlite")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	if got := pragmaString(t, store.DB(), "journal_mode"); got != "wal" {
		t.Fatalf("journal_mode = %q, want wal", got)
	}
	if got := pragmaInt(t, store.DB(), "foreign_keys"); got != 1 {
		t.Fatalf("foreign_keys = %d, want 1", got)
	}
	for _, table := range []string{"schema_migrations", "provisioning_gates", "audit_events", "print_jobs", "outbox_events", "pricing_book", "pricing_rules", "pricing_tiers"} {
		var name string
		err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing: %v", table, err)
		}
	}
}

func TestOpenIsIdempotentAndRetainsExistingRows(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "print-catalyst.sqlite")
	first, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB().Exec(`INSERT INTO audit_events (id, event_type, evidence, occurred_at) VALUES ('event-1', 'test', 'retained', '2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var evidence string
	if err := second.DB().QueryRow(`SELECT evidence FROM audit_events WHERE id = 'event-1'`).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != "retained" {
		t.Fatalf("evidence = %q", evidence)
	}
	var migrationCount int
	if err := second.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 37 {
		t.Fatalf("migration count = %d, want 37", migrationCount)
	}
}

func TestOpenAppliesAllEmbeddedMigrations(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "print-catalyst.sqlite")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var migrationCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			want++
		}
	}
	if migrationCount != want {
		t.Fatalf("migration count = %d, want %d (every embedded .sql)", migrationCount, want)
	}
	// Phase 8c adds the notification transports delivery table. The
	// migration count above is the authoritative source; this assert
	// documents the expected count so a reviewer can spot the bump
	// immediately when 016 lands.
	if migrationCount < 15 {
		t.Fatalf("migration count = %d, want >= 15 (Phase 8c adds notification transports)", migrationCount)
	}
	for _, table := range []string{"pricing_book", "pricing_rules", "pricing_tiers", "operators", "operator_sessions", "operator_login_throttle", "documents", "orders", "order_lines", "invoices", "invoice_lines", "invoice_sequence", "notification_settings", "printers", "printer_capabilities", "printer_paper_sizes", "printer_finishing_options", "printer_verifications", "id_card_calibrations", "id_card_sessions", "id_card_corners", "id_card_outputs", "passport_sessions", "passport_face_regions", "passport_outputs", "tunnel_state", "tunnel_events", "installation_keys", "licenses", "license_events", "payment_providers", "payment_intents", "payment_authorizations", "payment_attempts", "payment_webhook_events", "payment_ledger", "manual_payments", "notification_deliveries"} {
		var name string
		if err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("table %q missing after fresh open: %v", table, err)
		}
	}
}

func TestUpgradeCreatesVerifiedSnapshotOfExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec("CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP); INSERT INTO schema_migrations(version) VALUES('001_initial.sql'); INSERT INTO audit_events VALUES('keep','test','retained','2026-01-01')"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	upgraded, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	files, err := filepath.Glob(path + ".before-002_owner_business.sql-*.backup")
	if err != nil || len(files) != 1 {
		t.Fatalf("snapshot missing: %v %v", files, err)
	}
	// The pricing migration must snapshot too, so an existing installation can
	// roll back 003 without losing owner or business data.
	pricingSnapshots, err := filepath.Glob(path + ".before-003_pricing.sql-*.backup")
	if err != nil || len(pricingSnapshots) != 1 {
		t.Fatalf("pricing snapshot missing: %v %v", pricingSnapshots, err)
	}
	// The tiers migration must snapshot too, so a Phase 3B installation that
	// upgrades to 004 has a verified pre-tiers snapshot to roll back to.
	tierSnapshots, err := filepath.Glob(path + ".before-004_pricing_tiers.sql-*.backup")
	if err != nil || len(tierSnapshots) != 1 {
		t.Fatalf("tiers snapshot missing: %v %v", tierSnapshots, err)
	}
	// The operators migration must snapshot too, so an installation upgrading
	// to Phase 3D can roll back to a verified state without losing the owner
	// account, business profile, pricing rules and tiers.
	operatorSnapshots, err := filepath.Glob(path + ".before-005_operators.sql-*.backup")
	if err != nil || len(operatorSnapshots) != 1 {
		t.Fatalf("operators snapshot missing: %v %v", operatorSnapshots, err)
	}
	// The orders migration must snapshot too, so a Phase 3D installation that
	// upgrades to Phase 3E has a verified pre-orders snapshot to roll back to
	// without losing earlier provisioning state.
	ordersSnapshots, err := filepath.Glob(path + ".before-006_orders.sql-*.backup")
	if err != nil || len(ordersSnapshots) != 1 {
		t.Fatalf("orders snapshot missing: %v %v", ordersSnapshots, err)
	}
	// The invoices migration must snapshot too, so a Phase 3E installation
	// that upgrades to Phase 3F has a verified pre-invoices snapshot to roll
	// back to.
	invoicesSnapshots, err := filepath.Glob(path + ".before-007_invoices.sql-*.backup")
	if err != nil || len(invoicesSnapshots) != 1 {
		t.Fatalf("invoices snapshot missing: %v %v", invoicesSnapshots, err)
	}
	// The notifications migration must snapshot too, so an installation upgrading
	// to Phase 3G has a verified pre-notifications snapshot to roll back to.
	notifSnapshots, err := filepath.Glob(path + ".before-008_notifications.sql-*.backup")
	if err != nil || len(notifSnapshots) != 1 {
		t.Fatalf("notifications snapshot missing: %v %v", notifSnapshots, err)
	}
	// The printers migration must snapshot too, so an installation upgrading
	// to Phase 4 has a verified pre-printers snapshot to roll back to.
	printerSnapshots, err := filepath.Glob(path + ".before-009_printers.sql-*.backup")
	if err != nil || len(printerSnapshots) != 1 {
		t.Fatalf("printers snapshot missing: %v %v", printerSnapshots, err)
	}
	// The ID Card Studio migration must snapshot too, so an installation
	// upgrading to Phase 5 has a verified pre-id-cards snapshot to roll
	// back to without losing earlier printers, notifications, orders or
	// pricing state.
	idCardSnapshots, err := filepath.Glob(path + ".before-010_id_cards.sql-*.backup")
	if err != nil || len(idCardSnapshots) != 1 {
		t.Fatalf("id-cards snapshot missing: %v %v", idCardSnapshots, err)
	}
	// The Passport Photo Studio migration must snapshot too, so an installation
	// upgrading to Phase 6 has a verified pre-passports snapshot to roll back to
	// without losing earlier printers, notifications, orders or pricing state.
	passportSnapshots, err := filepath.Glob(path + ".before-011_passports.sql-*.backup")
	if err != nil || len(passportSnapshots) != 1 {
		t.Fatalf("passports snapshot missing: %v %v", passportSnapshots, err)
	}
	// The tunnel migration must snapshot too, so an installation upgrading
	// to Phase 7 has a verified pre-tunnel snapshot to roll back to without
	// losing earlier passport, ID-card, printer, notification, order or
	// pricing state.
	tunnelSnapshots, err := filepath.Glob(path + ".before-012_tunnel.sql-*.backup")
	if err != nil || len(tunnelSnapshots) != 1 {
		t.Fatalf("tunnel snapshot missing: %v %v", tunnelSnapshots, err)
	}
	// The licensing migration must snapshot too, so an installation upgrading
	// to Phase 8 has a verified pre-licensing snapshot to roll back to without
	// losing earlier tunnel, passport, ID-card, printer, notification, order
	// or pricing state.
	licenceSnapshots, err := filepath.Glob(path + ".before-013_licensing.sql-*.backup")
	if err != nil || len(licenceSnapshots) != 1 {
		t.Fatalf("licensing snapshot missing: %v %v", licenceSnapshots, err)
	}
	// The payments migration must snapshot too, so an installation upgrading
	// to Phase 8b has a verified pre-payments snapshot to roll back to without
	// losing earlier licence, tunnel, passport, ID-card, printer, notification,
	// order or pricing state.
	paymentSnapshots, err := filepath.Glob(path + ".before-014_payments.sql-*.backup")
	if err != nil || len(paymentSnapshots) != 1 {
		t.Fatalf("payments snapshot missing: %v %v", paymentSnapshots, err)
	}
	// The notification transports migration must snapshot too, so an
	// installation upgrading to Phase 8c has a verified pre-transports
	// snapshot to roll back to without losing earlier payment, licence,
	// tunnel, passport, ID-card, printer, notification, order or pricing
	// state.
	transportsSnapshots, err := filepath.Glob(path + ".before-015_notification_transports.sql-*.backup")
	if err != nil || len(transportsSnapshots) != 1 {
		t.Fatalf("notification transports snapshot missing: %v %v", transportsSnapshots, err)
	}
	if info, err := os.Stat(pricingSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty pricing snapshot")
	}
	if info, err := os.Stat(tierSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty tiers snapshot")
	}
	if info, err := os.Stat(notifSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty notifications snapshot")
	}
	if info, err := os.Stat(printerSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty printers snapshot")
	}
	if info, err := os.Stat(idCardSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty id-cards snapshot")
	}
	if info, err := os.Stat(passportSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty passports snapshot")
	}
	if info, err := os.Stat(tunnelSnapshots[0]); err != nil || info.Size() == 0 {
		t.Fatal("empty tunnel snapshot")
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Size() == 0 {
		t.Fatal("empty snapshot")
	}
	copy, err := sql.Open("sqlite", files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var evidence string
	if err := copy.QueryRow("SELECT evidence FROM audit_events WHERE id='keep'").Scan(&evidence); err != nil || evidence != "retained" {
		t.Fatalf("backup data: %s %v", evidence, err)
	}
	var n int
	if err := copy.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup must precede migration: %d %v", n, err)
	}
}

func TestOpenFailsSafelyWhenDatabaseIsExclusivelyLocked(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "print-catalyst.sqlite")
	initialized, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	locker, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	for _, statement := range []string{
		`PRAGMA journal_mode = DELETE`,
		`PRAGMA locking_mode = EXCLUSIVE`,
		`BEGIN EXCLUSIVE`,
	} {
		if _, err := locker.Exec(statement); err != nil {
			t.Fatalf("lock fixture %q: %v", statement, err)
		}
	}
	defer func() {
		_, _ = locker.Exec(`ROLLBACK`)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	second, err := Open(ctx, databasePath)
	if err == nil {
		second.Close()
		t.Fatal("store.Open() succeeded while the database was exclusively locked")
	}
	if _, pingErr := locker.Exec(`SELECT 1`); pingErr != nil {
		t.Fatalf("lock holder became unusable: %v", pingErr)
	}
}

func pragmaString(t *testing.T, database *sql.DB, name string) string {
	t.Helper()
	var value string
	if err := database.QueryRow("PRAGMA " + name).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func pragmaInt(t *testing.T, database *sql.DB, name string) int {
	t.Helper()
	var value int
	if err := database.QueryRow("PRAGMA " + name).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
