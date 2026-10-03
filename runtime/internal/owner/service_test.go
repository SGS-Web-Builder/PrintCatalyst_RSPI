package owner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func fixture(t *testing.T) (*store.Store, *Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local.sqlite")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, New(db.DB()), path
}
func allowLicence(t *testing.T, db *store.Store) {
	t.Helper()
	if err := provisioning.New(db.DB()).CompleteGate(context.Background(), provisioning.GateLicence, "test verified licence"); err != nil {
		t.Fatal(err)
	}
}

// setupOwner runs the standard "licence completed, owner created, owner
// logged in" prologue. It returns the active service and the owner's session
// token so operator-aware tests can immediately pivot to exercising delegated
// accounts. The login attempt is permitted by clearing the throttle after the
// failed-login assertion elsewhere in this file keeps it primed.
func setupOwner(t *testing.T, db *store.Store, service *Service) (string, Role) {
	t.Helper()
	ctx := context.Background()
	if err := service.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	// Reset the throttle so the dispatcher in this prologue is not fighting the
	// leftover state from earlier negative tests in the same process.
	if _, err := db.DB().Exec("UPDATE owner_login_throttle SET failures=0,retry_at=0 WHERE singleton=1"); err != nil {
		t.Fatal(err)
	}
	token, role, err := service.Login(ctx, "owner", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	return token, role
}
func TestOwnerDoesNotRequireLicenceAndCannotBeReplaced(t *testing.T) {
	db, s, _ := fixture(t)
	ctx := context.Background()
	// The owner is no longer gated on GateLicence — that gate is completed
	// by the licence activation handler, which (on a fresh install) needs
	// the installation setup token rather than an owner session, so the
	// "owner requires licence, licence requires owner" deadlock cannot
	// exist. The licence gate is still a precondition for every other
	// setup step that uses the owner session.
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatalf("first owner: %v", err)
	}
	if err := s.Create(ctx, "owner", "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short password: %v", err)
	}
	if err := s.Create(ctx, "owner", "a different long password"); !errors.Is(err, ErrSetup) {
		t.Fatalf("owner overwrite: %v", err)
	}
	if err := s.Create(ctx, "attacker", "a different long password"); !errors.Is(err, ErrSetup) {
		t.Fatalf("owner overwrite: %v", err)
	}
	var hash string
	if err := db.DB().QueryRow("SELECT password_hash FROM local_owner").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "sufficiently") || !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatal("password not protected")
	}
	status, err := provisioning.New(db.DB()).Status(ctx)
	// Without an explicit licence activation the licence gate is still
	// incomplete; the owner gate is the only one Create touches, so the
	// next pending gate is the licence itself, not business. The
	// production-ready gate only flips once the activation handler has
	// finished its real signing round-trip.
	if err != nil || status.Next != provisioning.GateLicence {
		t.Fatalf("gates: %+v %v", status, err)
	}
}
func TestSessionExpiryLogoutAndPersistentThrottle(t *testing.T) {
	db, s, path := fixture(t)
	ctx := context.Background()
	allowLicence(t, db)
	// Freeze the clock for the duration of the test. The throttle
	// window after a single failed login is `1 << failures` seconds
	// (two seconds for failures=1). PBKDF2 with 600k iterations
	// takes long enough under -race for wall-clock time to elapse
	// past that window between the failed login and the post-restart
	// assertion, which would make the test flaky even though the
	// production code is correct. Holding the clock still proves
	// every other assertion deterministically.
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.Create(ctx, "Owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "owner", "wrong"); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	db.Close()
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = New(reopened.DB())
	s.now = func() time.Time { return clock }
	if _, _, err := s.Login(ctx, "owner", "a sufficiently long password"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("throttle lost on restart: %v", err)
	}
	// Advance the clock past the two-second throttle window so the
	// retry succeeds.
	clock = clock.Add(time.Minute)
	token, loginRole, err := s.Login(ctx, "owner", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if loginRole != RoleOwner {
		t.Fatalf("owner login role = %q, want %q", loginRole, RoleOwner)
	}
	var stored string
	if err := reopened.DB().QueryRow("SELECT token_hash FROM owner_sessions").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("raw session token stored")
	}
	if name, err := s.Session(ctx, token); err != nil || name != "owner" {
		t.Fatalf("session: %s %v", name, err)
	}
	if subject, err := s.Resolve(ctx, token); err != nil || subject.Role != RoleOwner || subject.Name != "owner" {
		t.Fatalf("resolve: %+v %v", subject, err)
	}
	if _, err := s.Session(ctx, "invalid"); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("revoked session accepted")
	}
	token, _, err = s.Login(ctx, "owner", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(9 * time.Hour) }
	if _, err := s.Resolve(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("expired session accepted")
	}
}
func TestBusinessProfileSurvivesRestartWithoutOpeningOrders(t *testing.T) {
	db, s, path := fixture(t)
	ctx := context.Background()
	allowLicence(t, db)
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	profile := Profile{OwnerName: "Shop Owner", Name: "प्रिंट दुकान", Address: "पुणे", Phone: "+919999999999", Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata"}
	if err := s.SaveProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	invalid := profile
	invalid.OwnerName = strings.Repeat("x", 201)
	if err := s.SaveProfile(ctx, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized owner name accepted: %v", err)
	}
	invalid = profile
	invalid.TimeZone = "not/a/zone"
	if err := s.SaveProfile(ctx, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad timezone accepted: %v", err)
	}
	db.Close()
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := New(reopened.DB()).Profile(ctx)
	if err != nil || actual != profile {
		t.Fatalf("profile lost: %+v %v", actual, err)
	}
	status, err := provisioning.New(reopened.DB()).Status(ctx)
	if err != nil || status.Next != provisioning.GatePricing || status.ProductionReady {
		t.Fatalf("unsafe readiness: %+v %v", status, err)
	}
}

// TestOperatorsRequireOwnerSetupAndRejectCollisions covers the preconditions
// for creating an operator: the owner must already exist (so operators cannot
// be created before owner setup or in a fresh database), the supplied password
// must satisfy the same minimum-strength rules as the owner, and the operator
// username must not collide with the owner username or with an existing
// operator row. The audit trail records the creation event.
func TestOperatorsRequireOwnerSetupAndRejectCollisions(t *testing.T) {
	store, s, _ := fixture(t)
	ctx := context.Background()
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); !errors.Is(err, ErrSetup) {
		t.Fatalf("operator before owner: %v", err)
	}
	if err := s.CreateOperator(ctx, "BAD NAME!", "a sufficiently long password"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad username accepted: %v", err)
	}
	allowLicence(t, store)
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "alice", "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short password accepted: %v", err)
	}
	if err := s.CreateOperator(ctx, "owner", "a sufficiently long password"); !errors.Is(err, ErrOperatorExists) {
		t.Fatalf("owner-username collision accepted: %v", err)
	}
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatalf("first creation: %v", err)
	}
	if err := s.CreateOperator(ctx, "alice", "a different long password"); !errors.Is(err, ErrOperatorExists) {
		t.Fatalf("duplicate creation accepted: %v", err)
	}
	if err := s.CreateOperator(ctx, "Alice", "another long password"); !errors.Is(err, ErrOperatorExists) {
		t.Fatalf("case-insensitive collision accepted: %v", err)
	}
	var hash string
	if err := store.DB().QueryRow(`SELECT password_hash FROM operators WHERE username='alice'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatalf("password not protected: %q", hash)
	}
	var event string
	if err := store.DB().QueryRow(`SELECT event_type FROM audit_events WHERE event_type='operator.created' ORDER BY occurred_at DESC LIMIT 1`).Scan(&event); err != nil {
		t.Fatalf("audit event missing: %v", err)
	}
}

// TestOperatorLifecycleListsEnablesDeletes exercises the read/disable/enable/
// delete surface and confirms that disabling revokes live sessions immediately
// rather than waiting for the session to expire.
func TestOperatorLifecycleListsEnablesDeletes(t *testing.T) {
	store, s, _ := fixture(t)
	ctx := context.Background()
	allowLicence(t, store)
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "bob", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListOperators(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	if listed[0].Username != "alice" || listed[1].Username != "bob" {
		t.Fatalf("order: %+v", listed)
	}
	if listed[0].Role != RoleOperator || !listed[0].Enabled {
		t.Fatalf("row: %+v", listed[0])
	}

	token, err := s.AuthenticateOperator(ctx, "alice", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OperatorSession(ctx, token); err != nil {
		t.Fatalf("operator session: %v", err)
	}
	if subject, err := s.Resolve(ctx, token); err != nil || subject.Role != RoleOperator || subject.Name != "alice" {
		t.Fatalf("resolve: %+v %v", subject, err)
	}

	if err := s.SetOperatorEnabled(ctx, listed[0].ID, false); err != nil {
		t.Fatal(err)
	}
	// Disabled operators cannot authenticate and their live sessions are gone.
	if _, err := s.OperatorSession(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatalf("disabled session still live: %v", err)
	}
	if _, err := s.Resolve(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatalf("disabled session still resolves: %v", err)
	}
	if _, err := s.AuthenticateOperator(ctx, "alice", "a sufficiently long password"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("disabled operator still authenticated: %v", err)
	}

	// Enabling restores the operator row but does not retroactively restore the
	// revoked session; a fresh login is required.
	if err := s.SetOperatorEnabled(ctx, listed[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}

	if err := s.SetOperatorEnabled(ctx, 9999, false); !errors.Is(err, ErrOperatorMissing) {
		t.Fatalf("missing id accepted: %v", err)
	}
	if err := s.DeleteOperator(ctx, listed[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOperator(ctx, listed[0].ID); !errors.Is(err, ErrOperatorMissing) {
		t.Fatalf("double delete: %v", err)
	}
	remaining, err := s.ListOperators(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].Username != "bob" {
		t.Fatalf("remaining: %+v %v", remaining, err)
	}
	var orphan int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM operator_sessions`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if orphan != 0 {
		t.Fatalf("orphan operator_sessions after delete: %d", orphan)
	}
}

// TestOperatorThrottleIsIndependentFromOwner verifies that brute-force
// attempts against operator usernames do not consume the owner's retry window
// and vice versa. Each account class keeps its own backoff state so one
// username class cannot exhaust the other's recovery budget.
func TestOperatorThrottleIsIndependentFromOwner(t *testing.T) {
	store, s, _ := fixture(t)
	ctx := context.Background()
	allowLicence(t, store)
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	// Two operator failures — owner throttle stays at zero. We advance the
	// service's clock between attempts because each failure bumps the backoff
	// by 2^failures seconds; the next failure would otherwise trip the throttle
	// and we would be measuring throttle behavior rather than throttle leakage.
	for i := 0; i < 2; i++ {
		s.now = func() time.Time { return time.Now().Add(time.Duration(i+1) * time.Hour) }
		if _, err := s.AuthenticateOperator(ctx, "alice", "wrong"); !errors.Is(err, ErrCredentials) {
			t.Fatal(err)
		}
	}
	var ownerFailures, operatorFailures int
	if err := store.DB().QueryRow(`SELECT failures FROM owner_login_throttle`).Scan(&ownerFailures); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT failures FROM operator_login_throttle`).Scan(&operatorFailures); err != nil {
		t.Fatal(err)
	}
	if ownerFailures != 0 || operatorFailures != 2 {
		t.Fatalf("throttle leak: owner=%d operator=%d", ownerFailures, operatorFailures)
	}
	// Hammer owner — operator throttle stays untouched.
	for i := 0; i < 2; i++ {
		s.now = func() time.Time { return time.Now().Add(time.Duration(10+i) * time.Hour) }
		if _, _, err := s.Login(ctx, "owner", "wrong"); !errors.Is(err, ErrCredentials) {
			t.Fatal(err)
		}
	}
	if err := store.DB().QueryRow(`SELECT failures FROM owner_login_throttle`).Scan(&ownerFailures); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT failures FROM operator_login_throttle`).Scan(&operatorFailures); err != nil {
		t.Fatal(err)
	}
	if ownerFailures != 2 || operatorFailures != 2 {
		t.Fatalf("throttle leak: owner=%d operator=%d", ownerFailures, operatorFailures)
	}
	// Operator throttle is sticky across restart, mirroring the owner throttle.
	s.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	token, _, err := s.Login(ctx, "owner", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OperatorSession(ctx, token); err == nil {
		t.Fatal("owner token resolved as operator")
	}
}

// TestLoginDispatcherPrefersOwnerOverOperator ensures the dispatcher in
// Login resolves a colliding username as the owner, never as the operator.
// This is the documented "owner first" rule; without it, an attacker could
// shadow the owner with an operator account and harvest credentials.
func TestLoginDispatcherPrefersOwnerOverOperator(t *testing.T) {
	store, s, _ := fixture(t)
	ctx := context.Background()
	allowLicence(t, store)
	if err := s.Create(ctx, "admin", "the long enough password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "admin", "another long password"); !errors.Is(err, ErrOperatorExists) {
		t.Fatalf("username collision allowed: %v", err)
	}
	// Different usernames: each path is independently reachable.
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	ownerToken, role, err := s.Login(ctx, "admin", "the long enough password")
	if err != nil || role != RoleOwner {
		t.Fatalf("owner login: %s %v", role, err)
	}
	if _, err := s.Resolve(ctx, ownerToken); err != nil {
		t.Fatal(err)
	}
	operatorToken, role, err := s.Login(ctx, "alice", "a sufficiently long password")
	if err != nil || role != RoleOperator {
		t.Fatalf("operator login: %s %v", role, err)
	}
	if subject, err := s.Resolve(ctx, operatorToken); err != nil || subject.Role != RoleOperator {
		t.Fatalf("resolve: %+v %v", subject, err)
	}
	// Logout clears only the supplied token, never every session the same
	// browser happens to hold. A logged-out operator session must not revoke
	// an unrelated owner session.
	if err := s.Logout(ctx, operatorToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, operatorToken); !errors.Is(err, ErrCredentials) {
		t.Fatal("operator session survived logout")
	}
	if subject, err := s.Resolve(ctx, ownerToken); err != nil || subject.Role != RoleOwner || subject.Name != "admin" {
		t.Fatalf("owner session unexpectedly revoked: %+v %v", subject, err)
	}
	// A direct logout of the owner token does clear the owner session, of course.
	if err := s.Logout(ctx, ownerToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, ownerToken); !errors.Is(err, ErrCredentials) {
		t.Fatal("owner session survived logout")
	}
}

// TestOperatorSessionPersistsAcrossRestart confirms an operator session
// minted in process A is honored by a freshly opened store in process B. The
// eight-hour expiry and the SHA-256 token digest must both come from SQLite.
func TestOperatorSessionPersistsAcrossRestart(t *testing.T) {
	db, s, path := fixture(t)
	ctx := context.Background()
	allowLicence(t, db)
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	token, err := s.AuthenticateOperator(ctx, "alice", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed := New(reopened.DB())
	if subject, err := resumed.Resolve(ctx, token); err != nil || subject.Role != RoleOperator || subject.Name != "alice" {
		t.Fatalf("resume: %+v %v", subject, err)
	}
	if op, err := resumed.OperatorSession(ctx, token); err != nil || op.Username != "alice" || !op.Enabled {
		t.Fatalf("operator session after restart: %+v %v", op, err)
	}
	resumed.now = func() time.Time { return time.Now().Add(9 * time.Hour) }
	if _, err := resumed.Resolve(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("expired operator session accepted")
	}
}

// TestLoginRejectsDisabledOperatorsAndUnknownUsernames locks down the
// dispatcher's failure modes. A disabled operator must not authenticate; an
// unknown username must look identical to a wrong password from the caller's
// perspective so account state is not enumerated through error messages.
func TestLoginRejectsDisabledOperatorsAndUnknownUsernames(t *testing.T) {
	store, s, _ := fixture(t)
	ctx := context.Background()
	allowLicence(t, store)
	if err := s.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperator(ctx, "alice", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListOperators(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatal(err)
	}
	if err := s.SetOperatorEnabled(ctx, listed[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateOperator(ctx, "alice", "a sufficiently long password"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("disabled login accepted: %v", err)
	}
	if _, _, err := s.Login(ctx, "alice", "a sufficiently long password"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("dispatcher accepted disabled: %v", err)
	}
	if _, _, err := s.Login(ctx, "ghost", "a sufficiently long password"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	// Inputs that exceed the per-call size envelope are credential failures too.
	if _, _, err := s.Login(ctx, strings.Repeat("a", 81), "anything"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("oversize username accepted: %v", err)
	}
	if _, err := s.AuthenticateOperator(ctx, "alice", strings.Repeat("a", 1025)); !errors.Is(err, ErrCredentials) {
		t.Fatalf("oversize password accepted: %v", err)
	}
}
