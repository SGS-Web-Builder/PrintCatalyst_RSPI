package payments

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// newServiceFixture constructs a fresh Service backed by a temp
// database, deterministic encryption seed and a frozen clock.
func newServiceFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payments.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	clock := &fakePaymentsClock{t: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)}
	licensingCounter := 0
	paymentCounter := 0
	// Seed licensing so CreateIntent can read the installation id.
	licensingSvc, err := licensing.New(database.DB(), filepath.Join(t.TempDir(), "data"),
		licensing.WithClock(clock.now),
		licensing.WithEncryptionSeed(func() []byte { return []byte("payment-test-seed") }),
		licensing.WithIDGenerator(func() string {
			licensingCounter++
			return "license-" + string(rune('a'+licensingCounter))
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := licensingSvc.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	service, err := New(database.DB(), filepath.Join(t.TempDir(), "data"),
		WithClock(clock.now),
		WithEncryptionSeed(func() []byte { return []byte("payment-test-seed") }),
		WithIDGenerator(func() string {
			paymentCounter++
			return "payment-" + string(rune('a'+paymentCounter))
		}),
	)
	if err != nil {
		t.Fatalf("payments.New: %v", err)
	}
	return service, database
}

type fakePaymentsClock struct{ t time.Time }

func (f *fakePaymentsClock) now() time.Time { return f.t }

func TestEnsurePlatformProviderSeedsAndIsIdempotent(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	first, err := service.EnsurePlatformProvider(ctx)
	if err != nil {
		t.Fatalf("EnsurePlatformProvider first: %v", err)
	}
	second, err := service.EnsurePlatformProvider(ctx)
	if err != nil {
		t.Fatalf("EnsurePlatformProvider second: %v", err)
	}
	if first.ID != second.ID {
		t.Fatal("EnsurePlatformProvider returned a different id on the second call")
	}
	if first.Kind != KindRazorpayPlatform {
		t.Fatalf("kind = %q, want razorpay_platform", first.Kind)
	}
	if first.Enabled {
		t.Fatal("platform provider should be disabled by default")
	}
}

func TestCreateProviderPersistsAndRedactsSecret(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	created, err := service.CreateProvider(ctx, ProviderInput{
		Kind:        KindRazorpayMerchant,
		DisplayName: "Test Merchant Gateway",
		Enabled:     true,
		IsDefault:   true,
		Config:      map[string]any{"key_id": "rzp_test"},
		Secret:      "merchant-secret-1234567890",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if !created.HasSecret {
		t.Fatal("created provider reports no secret")
	}
	if !created.Enabled {
		t.Fatal("created provider is not enabled")
	}
	if !created.IsDefault {
		t.Fatal("created provider is not default")
	}
	listed, err := service.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %d, want 1 (merchant only; platform not seeded)", len(listed))
	}
	if !listed[0].HasSecret {
		t.Fatal("listed provider reports no secret")
	}
}

func TestUpdateProviderPreservesSecretWhenOmitted(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	created, err := service.CreateProvider(ctx, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "Update Test", Enabled: true,
		Secret: "merchant-secret-1234567890",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateProvider(ctx, created.ID, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "Update Test", Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if !updated.HasSecret {
		t.Fatal("UpdateProvider lost the existing secret")
	}
	if updated.Enabled {
		t.Fatal("UpdateProvider kept the provider enabled")
	}
}

func TestDeleteProviderRefusesPlatformRow(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, err := service.EnsurePlatformProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteProvider(ctx, platform.ID); err != ErrInvalid {
		t.Fatalf("DeleteProvider platform: err = %v, want ErrInvalid", err)
	}
}

func TestDeleteProviderRemovesMerchantRow(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	created, err := service.CreateProvider(ctx, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "To delete", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteProvider(ctx, created.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if _, err := service.ListProviders(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCreateIntentPlatformPathAuthorizesInOneRoundTrip(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, err := service.EnsurePlatformProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	intent, result, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-1",
		ProviderID:         platform.ID,
		AmountMinor:        12345,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		CustomerName:       "Test Customer",
		CustomerPhone:      "+919999999999",
		IdempotencyKey:     "key-1",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	if intent.Status != StatusAuthorized {
		t.Fatalf("intent status = %q, want authorized", intent.Status)
	}
	if result.GatewayPaymentID == "" {
		t.Fatal("result.GatewayPaymentID is empty")
	}
}

func TestCreateIntentEnforcesUniqueIdempotencyKey(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, err := service.EnsurePlatformProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	input := IntentInput{
		OrderID:            "order-2",
		ProviderID:         platform.ID,
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		CustomerName:       "Test Customer",
		IdempotencyKey:     "dup-key",
	}
	if _, _, err := service.CreateIntent(ctx, input); err != nil {
		t.Fatalf("first CreateIntent: %v", err)
	}
	if _, _, err := service.CreateIntent(ctx, input); err != ErrDuplicate {
		t.Fatalf("second CreateIntent: err = %v, want ErrDuplicate", err)
	}
}

func TestCreateIntentRejectsDisabledProvider(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, err := service.EnsurePlatformProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Default state is disabled.
	if _, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-3",
		ProviderID:         platform.ID,
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "key-3",
	}); err != ErrUnconfigured {
		t.Fatalf("CreateIntent on disabled provider: err = %v, want ErrUnconfigured", err)
	}
}

func TestCreateIntentRejectsInvalidInput(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "",
		ProviderID:         platform.ID,
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "x",
	}); err != ErrInvalid {
		t.Fatalf("missing order id: err = %v, want ErrInvalid", err)
	}
	if _, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "ok",
		ProviderID:         platform.ID,
		AmountMinor:        0,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "y",
	}); err != ErrInvalid {
		t.Fatalf("zero amount: err = %v, want ErrInvalid", err)
	}
}

func TestManualApproveMovesIntentToCaptured(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	intent, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-manual",
		ProviderID:         platform.ID,
		AmountMinor:        5000,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "manual-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.ManualApprove(ctx, intent.ID, MethodCash, "cashier-1", 5000, "INR", "owner", "paid in cash")
	if err != nil {
		t.Fatalf("ManualApprove: %v", err)
	}
	if approval.Method != MethodCash {
		t.Fatalf("method = %q, want cash", approval.Method)
	}
	refreshed, err := service.loadIntent(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != StatusCaptured {
		t.Fatalf("status = %q, want captured", refreshed.Status)
	}
}

func TestManualApproveRejectsAmountMismatch(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	intent, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-mismatch",
		ProviderID:         platform.ID,
		AmountMinor:        5000,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "mm-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ManualApprove(ctx, intent.ID, MethodCash, "", 6000, "INR", "owner", ""); err != ErrAmount {
		t.Fatalf("err = %v, want ErrAmount", err)
	}
}

func TestManualRejectMovesIntentToFailed(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	intent, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-reject",
		ProviderID:         platform.ID,
		AmountMinor:        1000,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "reject-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ManualReject(ctx, intent.ID, "owner", "duplicate payment"); err != nil {
		t.Fatalf("ManualReject: %v", err)
	}
	refreshed, err := service.loadIntent(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", refreshed.Status)
	}
}

func TestManualApproveTwiceIsRejected(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	intent, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-twice",
		ProviderID:         platform.ID,
		AmountMinor:        1000,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "twice-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ManualApprove(ctx, intent.ID, MethodCash, "", 1000, "INR", "owner", ""); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := service.ManualApprove(ctx, intent.ID, MethodCash, "", 1000, "INR", "owner", ""); err != ErrTransition {
		t.Fatalf("second approve: err = %v, want ErrTransition", err)
	}
}

func TestListIntentsReturnsNewestFirst(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := service.CreateIntent(ctx, IntentInput{
			OrderID:            "order-list-" + string(rune('a'+i)),
			ProviderID:         platform.ID,
			AmountMinor:        100,
			Currency:           "INR",
			CurrencyMinorUnits: 2,
			IdempotencyKey:     "list-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	intents, err := service.ListIntents(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 3 {
		t.Fatalf("intents = %d, want 3", len(intents))
	}
	for i := 1; i < len(intents); i++ {
		if intents[i-1].CreatedAt.Before(intents[i].CreatedAt) {
			t.Fatal("intents are not newest-first")
		}
	}
}

func TestLedgerReturnsAuditEntriesNewestFirst(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	platform, _ := service.EnsurePlatformProvider(ctx)
	if _, err := service.UpdateProvider(ctx, platform.ID, ProviderInput{
		Kind: KindRazorpayPlatform, DisplayName: "Platform Razorpay (signed by Print Catalyst)",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-ledger",
		ProviderID:         platform.ID,
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "ledger-1",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := service.Ledger(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("entries = %d, want >= 2", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].OccurredAt.Before(entries[i].OccurredAt) {
			t.Fatal("ledger is not newest-first")
		}
	}
}

func TestEncryptedSecretRoundTripThroughDatabase(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	created, err := service.CreateProvider(ctx, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "Secret round-trip", Enabled: true,
		Secret: "the-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := service.loadSecret(ctx, created.ID)
	if err != nil {
		t.Fatalf("loadSecret: %v", err)
	}
	if string(secret) != "the-secret" {
		t.Fatalf("secret = %q, want the-secret", secret)
	}
}

func TestUpdateProviderReplacesSecret(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	created, err := service.CreateProvider(ctx, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "Replace secret", Enabled: true,
		Secret: "old-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateProvider(ctx, created.ID, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "Replace secret", Enabled: true,
		Secret: "new-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasSecret {
		t.Fatal("updated provider reports no secret")
	}
	secret, err := service.loadSecret(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "new-secret" {
		t.Fatalf("secret = %q, want new-secret", secret)
	}
}

func TestCreateIntentRejectsMissingInstallation(t *testing.T) {
	// A payments service on a database with no licensing singleton
	// returns ErrUnconfigured from CreateIntent. Seed a provider so
	// the load-provider check does not pre-empt the install check.
	path := filepath.Join(t.TempDir(), "no-license.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	service, err := New(database.DB(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := service.CreateProvider(context.Background(), ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "missing install", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateIntent(context.Background(), IntentInput{
		OrderID:            "x",
		ProviderID:         provider.ID,
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
	}); err != ErrUnconfigured {
		t.Fatalf("err = %v, want ErrUnconfigured", err)
	}
}

func TestMerchantKindWithoutAdapterReturnsError(t *testing.T) {
	service, _ := newServiceFixture(t)
	ctx := context.Background()
	created, err := service.CreateProvider(ctx, ProviderInput{
		Kind: KindRazorpayMerchant, DisplayName: "No adapter", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateIntent(ctx, IntentInput{
		OrderID:            "order-no-adapter",
		ProviderID:         created.ID,
		AmountMinor:        100,
		Currency:           "INR",
		CurrencyMinorUnits: 2,
		IdempotencyKey:     "no-adapter-1",
	}); err == nil {
		t.Fatal("CreateIntent accepted a merchant provider without a registered adapter")
	}
	if err != nil && !strings.Contains(err.Error(), "merchant gateway adapter not registered") {
		t.Fatalf("err = %v, want merchant gateway adapter error", err)
	}
}
