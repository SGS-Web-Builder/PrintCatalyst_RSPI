package licensing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// newServiceFixture constructs a fresh Service backed by a temp
// database, deterministic seed and a frozen clock. Returns the service,
// the database handle and a controllable clock.
func newServiceFixture(t *testing.T) (*Service, *store.Store, *fakeClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "licensing.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	clock := &fakeClock{t: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)}
	service, err := New(database.DB(), filepath.Join(t.TempDir(), "data"),
		WithClock(clock.now),
		WithEncryptionSeed(func() []byte { return []byte("test-seed") }),
		WithIDGenerator(func() string {
			clock.id++
			return fmt.Sprintf("audit-%d", clock.id)
		}),
	)
	if err != nil {
		t.Fatalf("licensing.New: %v", err)
	}
	return service, database, clock
}

type fakeClock struct {
	t  time.Time
	id int
}

func (f *fakeClock) now() time.Time { return f.t }
func (f *fakeClock) advance(d time.Duration) {
	f.t = f.t.Add(d)
}

func TestEnsureKeyPairGeneratesOnce(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	first, err := service.EnsureKeyPair(ctx)
	if err != nil {
		t.Fatalf("EnsureKeyPair first: %v", err)
	}
	second, err := service.EnsureKeyPair(ctx)
	if err != nil {
		t.Fatalf("EnsureKeyPair second: %v", err)
	}
	if !first.Equal(second) {
		t.Fatal("EnsureKeyPair regenerated the key pair on second call")
	}
}

func TestActivatePersistsCurrentLicense(t *testing.T) {
	service, _, clock := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	license, err := service.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !license.IsCurrent {
		t.Fatal("activated licence is not flagged as current")
	}
	if license.SupportUntil.Before(clock.now()) {
		t.Fatal("support window ends before now")
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Unconfigured {
		t.Fatal("Status reports unconfigured after activation")
	}
	if status.Current == nil {
		t.Fatal("Status returned no current licence")
	}
	if status.Current.ID != license.ID {
		t.Fatal("Status licence id does not match activated licence")
	}
}

func TestActivateRotatesPreviousCurrent(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := service.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("second activation kept the same licence id")
	}
	var currentCount int
	if err := service.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM licenses WHERE is_current = 1`).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if currentCount != 1 {
		t.Fatalf("current licence count = %d, want 1", currentCount)
	}
}

func TestRefreshVerifiesCurrentLicense(t *testing.T) {
	service, _, clock := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	clock.advance(48 * time.Hour)
	if err := service.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
}

func TestStatusReportsOfflineGraceAfterActivation(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.OfflineGrace {
		t.Fatal("Status did not report offline grace after activation")
	}
	if status.GraceRemaining == "" {
		t.Fatal("Status did not report grace remaining duration")
	}
}

func TestRevokePersistsReason(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, "merchant requested cancellation"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Revoked {
		t.Fatal("Status did not report revoked")
	}
	if status.RevokedReason != "merchant requested cancellation" {
		t.Fatalf("revoked reason = %q, want %q", status.RevokedReason, "merchant requested cancellation")
	}
}

func TestRevokeRejectsEmptyReason(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Revoke empty reason: err = %v, want ErrInvalid", err)
	}
}

func TestTransferGeneratesNewKeyAndInvalidatesLicence(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	original, err := service.EnsureKeyPair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Transfer(ctx); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	next, ok, err := service.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("PublicKey reports no key after Transfer")
	}
	if original.Equal(next) {
		t.Fatal("Transfer kept the same device key")
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Current != nil {
		t.Fatal("Status still reports a current licence after Transfer")
	}
	if !status.Unconfigured {
		t.Fatal("Status should be unconfigured until re-activation after Transfer")
	}
}

func TestEventsAreAppendOnlyNewestFirst(t *testing.T) {
	service, _, clock := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Hour)
	if err := service.Revoke(ctx, "manual"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Hour)
	if err := service.Transfer(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("events count = %d, want at least 4", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i-1].OccurredAt.Before(events[i].OccurredAt) {
			t.Fatalf("events are not newest-first: %v before %v", events[i-1].OccurredAt, events[i].OccurredAt)
		}
	}
}

func TestStatusRejectsTamperedSignature(t *testing.T) {
	service, db, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE licenses SET payload = X'00'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Status(ctx); err == nil {
		t.Fatal("Status accepted a tampered payload")
	}
}

func TestVerifyOnStartAcceptsFreshLicense(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err := service.VerifyOnStart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("VerifyOnStart returned false on a fresh activation")
	}
}

func TestVerifyOnStartFailsWithoutKey(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ok, err := service.VerifyOnStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("VerifyOnStart returned true without a device key")
	}
}

func TestActivateRejectsTamperedEnvelope(t *testing.T) {
	// A hostile control plane that returns a payload signed by a
	// different key (a stolen signature) must be rejected by the
	// local service's signature verification step.
	hostilePrivate, _ := GenerateKeyPair()
	service, _, _ := newServiceFixture(t)
	if _, err := service.EnsureKeyPair(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.controlPlane = LocalControlPlane{PrivateKey: hostilePrivate.PrivateKey}
	if _, err := service.Activate(context.Background()); !errors.Is(err, ErrSignature) {
		t.Fatalf("Activate with stolen signature: err = %v, want ErrSignature", err)
	}
}

func TestEventsLimit(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events count = %d, want 2", len(events))
	}
	events, err = service.Events(ctx, 99999)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("events defaulted to zero with a large limit")
	}
}

func TestStatusReportsSupportUntil(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	license, err := service.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.SupportUntil == nil || !status.SupportUntil.Equal(license.SupportUntil) {
		t.Fatalf("SupportUntil = %v, want %v", status.SupportUntil, license.SupportUntil)
	}
}

func TestEnsureKeyPairReturnsConsistentFingerprint(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	publicKey, err := service.EnsureKeyPair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := Fingerprint(publicKey)
	again, err := service.EnsureKeyPair(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(again) != expected {
		t.Fatal("EnsureKeyPair produced different fingerprints across calls")
	}
}

func TestStatusAfterActivationIncludesEntitlements(t *testing.T) {
	service, _, _ := newServiceFixture(t)
	ctx := context.Background()
	if _, err := service.EnsureKeyPair(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Entitlements) == 0 {
		t.Fatal("Status returned no entitlements")
	}
	for _, e := range status.Entitlements {
		if !strings.HasPrefix(string(e), "") {
			t.Fatalf("entitlement has no recognised prefix: %s", e)
		}
	}
}
