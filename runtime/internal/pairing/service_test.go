package pairing

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// testDB is opened once per test binary by TestMain so each pairing
// test reuses the same migrated schema instead of running the
// 16-migration VACUUM-INTO chain on every test function.
var testDB *sql.DB

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "print-catalyst-pairing-suite")
	if err != nil {
		panic("tempdir: " + err.Error())
	}
	databasePath := filepath.Join(dir, "pairing-suite.sqlite")
	st, err := store.Open(context.Background(), databasePath)
	if err != nil {
		panic("open store: " + err.Error())
	}
	testDB = st.DB()
	code := m.Run()
	_ = st.Close()
	_ = os.RemoveAll(dir)
	if code != 0 {
		os.Exit(code)
	}
}

// newTestService creates a Service against the shared test DB with a
// deterministic clock and id generator so tests are reproducible.
func newTestService(t *testing.T) *Service {
	t.Helper()
	// Clear the four pairing tables so each test starts from an empty
	// slate. We deliberately avoid TRUNCATE because SQLite does not
	// support it; the small per-test clear is fine on this size.
	for _, table := range []string{"device_tokens", "paired_devices", "pair_codes", "pair_exchange_attempts"} {
		if _, err := testDB.Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	counter := 0
	svc, err := New(testDB,
		WithClock(func() time.Time { return now }),
		WithIDGenerator(func() string {
			counter++
			return "id-" + u64(counter)
		}),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// u64 formats a non-negative integer in base-10.
func u64(v int) string {
	if v == 0 {
		return "0"
	}
	out := make([]byte, 0, 10)
	for v > 0 {
		out = append([]byte{byte('0' + v%10)}, out...)
		v /= 10
	}
	return string(out)
}

func TestNormalizeCode(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"abcd-efgh", "ABCD-EFGH"},
		{"  ABCD  ", "ABCD"},
		{"\tabc\t", "ABC"},
	}
	for _, c := range cases {
		if got := NormalizeCode(c.raw); got != c.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestIsValidFingerprint(t *testing.T) {
	good := []string{"alice-iphone", "device-1234", strings.Repeat("a", DeviceFingerprintMaxLength)}
	bad := []string{"", " ", strings.Repeat("a", DeviceFingerprintMaxLength+1), "with\x00null", "ctrl\x01char"}
	for _, g := range good {
		if !IsValidFingerprint(g) {
			t.Errorf("expected %q to be valid", g)
		}
	}
	for _, b := range bad {
		if IsValidFingerprint(b) {
			t.Errorf("expected %q to be invalid", b)
		}
	}
}

func TestIsValidLabel(t *testing.T) {
	if !IsValidLabel("Alice's iPhone 15") {
		t.Error("expected normal label to be valid")
	}
	if IsValidLabel("") {
		t.Error("expected empty label to be invalid")
	}
	if IsValidLabel(strings.Repeat("a", DeviceLabelMaxLength+1)) {
		t.Error("expected oversize label to be invalid")
	}
}

func TestInitiate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if len(code.Code) != CodeLength {
		t.Errorf("code length = %d, want %d", len(code.Code), CodeLength)
	}
	for _, ch := range code.Code {
		if !strings.ContainsRune(CodeAlphabet, ch) {
			t.Errorf("code %q contains character %q outside alphabet", code.Code, ch)
		}
	}
	if code.ExpiresAt.Sub(code.CreatedAt) != CodeTTL {
		t.Errorf("TTL = %v, want %v", code.ExpiresAt.Sub(code.CreatedAt), CodeTTL)
	}
	if !strings.Contains(code.DeepLink, code.Code) {
		t.Errorf("deep link %q missing code %q", code.DeepLink, code.Code)
	}
	if !strings.HasPrefix(code.DeepLink, "printcatalyst://pair") {
		t.Errorf("default deep link missing scheme: %q", code.DeepLink)
	}
}

func TestInitiateDeepLinkTemplate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "printcatalyst-onpremise://pair?code={{code}}&src=merchant")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if !strings.Contains(code.DeepLink, "printcatalyst-onpremise://pair") {
		t.Errorf("deep link missing custom scheme: %q", code.DeepLink)
	}
	if !strings.Contains(code.DeepLink, "src=merchant") {
		t.Errorf("deep link lost static query: %q", code.DeepLink)
	}
}

func TestInitiateUniqueCodes(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		code, err := svc.Initiate(ctx, "")
		if err != nil {
			t.Fatalf("initiate %d: %v", i, err)
		}
		if seen[code.Code] {
			t.Errorf("duplicate code generated: %q", code.Code)
		}
		seen[code.Code] = true
	}
}

func TestExchangeHappyPath(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	result, err := svc.Exchange(ctx, ExchangeInput{
		Code:        code.Code,
		Fingerprint: "alice-iphone-15",
		Label:       "Alice's iPhone 15",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if result.DeviceID == "" {
		t.Error("device id missing")
	}
	if result.Token == "" {
		t.Error("token missing")
	}
	if result.ExpiresAt.Sub(result.IssuedAt) != TokenTTL {
		t.Errorf("token TTL = %v, want %v", result.ExpiresAt.Sub(result.IssuedAt), TokenTTL)
	}
	_, err = svc.Exchange(ctx, ExchangeInput{
		Code:        code.Code,
		Fingerprint: "alice-iphone-15",
		Label:       "Alice's iPhone 15",
	})
	if err != ErrUnconfigured {
		t.Errorf("replay: got %v, want ErrUnconfigured", err)
	}
}

func TestVerifyToken(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	result, err := svc.Exchange(ctx, ExchangeInput{
		Code:        code.Code,
		Fingerprint: "alice",
		Label:       "Alice",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	device, err := svc.VerifyToken(ctx, result.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if device.ID != result.DeviceID {
		t.Errorf("device id = %q, want %q", device.ID, result.DeviceID)
	}
	if device.Fingerprint != "alice" {
		t.Errorf("fingerprint = %q, want alice", device.Fingerprint)
	}
}

func TestVerifyTokenRejectsUnknownAndEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.VerifyToken(ctx, ""); err != ErrSignature {
		t.Errorf("empty token: got %v, want ErrSignature", err)
	}
	if _, err := svc.VerifyToken(ctx, "not-a-real-token"); err != ErrSignature {
		t.Errorf("garbage token: got %v, want ErrSignature", err)
	}
}

func TestExchangeInvalidInput(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	cases := []ExchangeInput{
		{Code: code.Code, Fingerprint: "", Label: "Alice"},
		{Code: code.Code, Fingerprint: "alice", Label: ""},
		{Code: "", Fingerprint: "alice", Label: "Alice"},
		{Code: code.Code, Fingerprint: "alice\x00", Label: "Alice"},
		{Code: code.Code, Fingerprint: "alice", Label: "Alice\x01"},
	}
	for i, input := range cases {
		if _, err := svc.Exchange(ctx, input); err != ErrInvalid {
			t.Errorf("case %d: got %v, want ErrInvalid", i, err)
		}
	}
}

func TestExchangeUnknownCode(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Exchange(ctx, ExchangeInput{
		Code:        "ZZZZZZZZ",
		Fingerprint: "alice",
		Label:       "Alice",
	})
	if err != ErrUnconfigured {
		t.Errorf("got %v, want ErrUnconfigured", err)
	}
}

func TestExchangeExpiredCode(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := now
	svc, err := New(testDB,
		WithClock(func() time.Time { return clock }),
		WithIDGenerator(func() string { return "id-exp" }),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	clock = clock.Add(CodeTTL + time.Minute)
	_, err = svc.Exchange(ctx, ExchangeInput{
		Code:        code.Code,
		Fingerprint: "alice",
		Label:       "Alice",
	})
	if err != ErrExpired {
		t.Errorf("got %v, want ErrExpired", err)
	}
}

func TestRevoke(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	code, err := svc.Initiate(ctx, "")
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	result, err := svc.Exchange(ctx, ExchangeInput{
		Code:        code.Code,
		Fingerprint: "alice",
		Label:       "Alice",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if err := svc.Revoke(ctx, result.DeviceID, "lost phone"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.VerifyToken(ctx, result.Token); err != ErrRevoked {
		t.Errorf("verify after revoke: got %v, want ErrRevoked", err)
	}
	if err := svc.Revoke(ctx, result.DeviceID, "still lost"); err != ErrUnconfigured {
		t.Errorf("re-revoke: got %v, want ErrUnconfigured", err)
	}
}

func TestRevokeEmptyInput(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.Revoke(ctx, "", ""); err != ErrInvalid {
		t.Errorf("got %v, want ErrInvalid", err)
	}
}

func TestList(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	first, _ := svc.Initiate(ctx, "")
	if _, err := svc.Exchange(ctx, ExchangeInput{
		Code:        first.Code,
		Fingerprint: "bob",
		Label:       "Bob's Pixel",
	}); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	pending, _ := svc.Initiate(ctx, "")
	status, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(status.Devices) != 1 {
		t.Fatalf("devices = %d, want 1", len(status.Devices))
	}
	if status.Devices[0].Label != "Bob's Pixel" {
		t.Errorf("label = %q, want %q", status.Devices[0].Label, "Bob's Pixel")
	}
	if len(status.PendingCodes) != 2 {
		t.Fatalf("codes = %d, want 2", len(status.PendingCodes))
	}
	if status.PendingCodes[0].ID == first.ID && status.PendingCodes[0].ConsumedAt.IsZero() {
		t.Error("first code should be marked consumed")
	}
	if status.PendingCodes[0].ID == pending.ID && !status.PendingCodes[0].ConsumedAt.IsZero() {
		t.Error("pending code should not be consumed")
	}
}

func TestEqual(t *testing.T) {
	if !Equal("abc", "abc") {
		t.Error("Equal(abc, abc) = false")
	}
	if Equal("abc", "abd") {
		t.Error("Equal(abc, abd) = true")
	}
	if Equal("abc", "abcd") {
		t.Error("Equal(abc, abcd) = true")
	}
}
