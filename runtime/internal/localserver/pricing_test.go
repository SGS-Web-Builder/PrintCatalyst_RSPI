package localserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// pricingFixture returns a handler with licence, owner and business state ready
// to be driven through the public endpoints, plus the underlying store so tests
// can assert persisted truth.
func pricingFixture(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "pricing-http.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	setup := provisioning.New(database.DB())
	if err := setup.CompleteGate(context.Background(), provisioning.GateLicence, "verified test licence"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	handler := New("127.0.0.1:8080", "test",
		WithStoreHealth(database), WithProvisioning(setup),
		WithOwner(accounts, "test-bootstrap-token"), WithPricing(pricing.New(database.DB())),
	).Handler()
	return handler, database
}

// signedInOwner walks the real local setup sequence and returns the session
// cookie, CSRF token and a business profile that has been saved.
func signedInOwner(t *testing.T, handler http.Handler) (*http.Cookie, string) {
	t.Helper()
	setup := `{"username":"owner","password":"a sufficiently long password","setupToken":"test-bootstrap-token"}`
	if w := call(handler, "POST", "/api/v1/setup/owner", setup, nil, ""); w.Code != 201 {
		t.Fatalf("owner setup: %d %s", w.Code, w.Body)
	}
	login := call(handler, "POST", "/api/v1/owner/login", `{"username":"owner","password":"a sufficiently long password"}`, nil, "")
	if login.Code != 200 {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.CSRFToken == "" {
		t.Fatal("missing CSRF token")
	}
	return cookies[0], body.CSRFToken
}

// No currencyMinorUnits field: precision is derived server-side, and a request
// that supplies it is rejected as an unknown field.
const pricingBody = `{"entries":[
{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250},
{"paperSize":"A4","colourMode":"monochrome","sides":"two-sided-long-edge","unitPriceMinor":400},
{"paperSize":"A4","colourMode":"colour","sides":"one-sided","unitPriceMinor":1500}]}`

const businessBody = `{"name":"Campus","address":"Pune","phone":"","country":"IN","currency":"INR","locale":"en-IN","timeZone":"Asia/Kolkata"}`

func TestPricingAPIPersistsThroughTheAuthenticatedLocalFlow(t *testing.T) {
	handler, database := pricingFixture(t)
	ctx := context.Background()
	cookie, csrf := signedInOwner(t, handler)

	// Pricing is unavailable until the business profile exists, so the pricing
	// gate cannot be leapfrogged.
	if w := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, csrf); w.Code != 409 {
		t.Fatalf("pricing before business details: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, ""); w.Code != 404 {
		t.Fatalf("unconfigured pricing read: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, ""); w.Code != 404 {
		t.Fatalf("pricing read after business save: %d %s", w.Code, w.Body)
	}

	var book pricing.Book
	saved := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, csrf)
	if saved.Code != 200 {
		t.Fatalf("pricing save: %d %s", saved.Code, saved.Body)
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	// The exponent reported to the screen is derived from INR, not echoed back
	// from the request, which carried no precision field at all.
	if book.Currency != "INR" || book.CurrencyMinorUnits != 2 || book.DerivedMinorUnits != 2 ||
		!book.CurrencySupported || book.PrecisionMismatch || book.PriceUnit != pricing.UnitSheet || len(book.Entries) != 3 {
		t.Fatalf("unexpected book: %+v", book)
	}
	if book.PriceUnitLabel == "" {
		t.Fatal("pricing unit is not described to the merchant")
	}
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"unitPriceMinor":1500`) {
		t.Fatalf("pricing read: %d %s", w.Code, w.Body)
	}

	status, err := provisioning.New(database.DB()).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Next != provisioning.GatePrinter || status.Completed != 4 || status.ProductionReady {
		t.Fatalf("gate progression wrong: %+v", status)
	}
	// Pricing configuration must not open customer order intake.
	if w := call(handler, "POST", "/api/v1/orders", `{"items":[]}`, nil, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("order intake after pricing: %d %s", w.Code, w.Body)
	}
}

func TestPricingAPIRejectsUnauthenticatedAndForgedRequests(t *testing.T) {
	handler, _ := pricingFixture(t)

	// No session at all.
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", nil, ""); w.Code != 401 {
		t.Fatalf("unauthenticated read: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, nil, ""); w.Code != 401 {
		t.Fatalf("unauthenticated write: %d %s", w.Code, w.Body)
	}

	cookie, csrf := signedInOwner(t, handler)
	// Positive control: the valid token is not what the CSRF check rejects. This
	// write fails later with 409 because no business profile exists yet, so the
	// 403 responses below are attributable to the token check itself.
	if w := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, csrf); w.Code != 409 {
		t.Fatalf("valid CSRF token rejected: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, ""); w.Code != 403 {
		t.Fatalf("missing CSRF: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, "wrong-token"); w.Code != 403 {
		t.Fatalf("forged CSRF: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", &http.Cookie{Name: ownerCookie, Value: strings.Repeat("f", 64)}, ""); w.Code != 401 {
		t.Fatalf("forged session: %d %s", w.Code, w.Body)
	}

	// Cross-site, forwarded and non-loopback access is refused before any read.
	crossSite := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/owner/pricing", nil)
	crossSite.RemoteAddr = "127.0.0.1:9000"
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, crossSite)
	if recorder.Code != 403 {
		t.Fatalf("cross-site read: %d %s", recorder.Code, recorder.Body)
	}
	forwarded := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/owner/pricing", nil)
	forwarded.RemoteAddr = "127.0.0.1:9000"
	forwarded.Header.Set("X-Forwarded-For", "203.0.113.7")
	forwarded.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, forwarded)
	if recorder.Code != 403 {
		t.Fatalf("forwarded read: %d %s", recorder.Code, recorder.Body)
	}
	remote := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/owner/pricing", nil)
	remote.RemoteAddr = "203.0.113.7:9000"
	remote.Header.Set("Origin", "http://127.0.0.1:8080")
	remote.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, remote)
	if recorder.Code != 403 {
		t.Fatalf("remote read: %d %s", recorder.Code, recorder.Body)
	}
}

func TestPricingAPIRejectsInvalidMoneyAndUnknownFields(t *testing.T) {
	handler, database := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}

	cases := []struct {
		name string
		body string
		code int
	}{
		{"fractional money rejected", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":2.5}]}`, 400},
		{"quoted money rejected", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":"250"}]}`, 400},
		{"client supplied currency rejected", `{"currency":"USD","entries":[]}`, 400},
		// Precision is derived server-side from supported-currency metadata, so a
		// client-supplied exponent is an unknown field and is refused before it can
		// change what a stored integer means — even when it matches the derived one.
		{"forged zero precision rejected", `{"currencyMinorUnits":0,"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250}]}`, 400},
		{"forged matching precision rejected", `{"currencyMinorUnits":2,"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250}]}`, 400},
		{"forged out of range precision rejected", `{"currencyMinorUnits":9,"entries":[]}`, 400},
		{"client supplied derived precision rejected", `{"derivedMinorUnits":0,"entries":[]}`, 400},
		{"empty price book rejected", `{"entries":[]}`, 400},
		{"negative money rejected", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":-1}]}`, 400},
		{"unknown price unit rejected", `{"priceUnit":"page","entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250}]}`, 400},
		{"unknown colour mode rejected", `{"entries":[{"paperSize":"A4","colourMode":"sepia","sides":"one-sided","unitPriceMinor":250}]}`, 400},
		{"unknown sides mode rejected", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"triplex","unitPriceMinor":250}]}`, 400},
		{"non-array entries rejected", `{"entries":"A4"}`, 400},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if w := call(handler, "PUT", "/api/v1/owner/pricing", testCase.body, cookie, csrf); w.Code != testCase.code {
				t.Fatalf("code = %d body = %s, want %d", w.Code, w.Body, testCase.code)
			}
		})
	}

	// A non-JSON write is refused by the shared owner guard.
	r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/api/v1/owner/pricing", strings.NewReader("not json"))
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Content-Type", "text/plain")
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON write: %d %s", recorder.Code, recorder.Body)
	}

	// Nothing invalid was stored and the pricing gate is still open.
	if w := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, ""); w.Code != 404 {
		t.Fatalf("pricing read after rejected writes: %d %s", w.Code, w.Body)
	}
	status, err := provisioning.New(database.DB()).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Next != provisioning.GatePricing || status.Completed != 3 || status.ProductionReady {
		t.Fatalf("gate state after rejected writes: %+v", status)
	}
}

func TestPricingPersistsAcrossRestartAndKeepsIntakeClosed(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "pricing-restart.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	setup := provisioning.New(database.DB())
	if err := setup.CompleteGate(context.Background(), provisioning.GateLicence, "verified test licence"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	first := New("127.0.0.1:8080", "test",
		WithStoreHealth(database), WithProvisioning(setup),
		WithOwner(accounts, "test-bootstrap-token"), WithPricing(pricing.New(database.DB())),
	).Handler()
	cookie, csrf := signedInOwner(t, first)
	if w := call(first, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	if w := call(first, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("pricing save: %d %s", w.Code, w.Body)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: sessions and prices both come from SQLite, so the eight-hour
	// session survives and stored prices are read back unchanged.
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := New("127.0.0.1:8080", "test",
		WithStoreHealth(reopened), WithProvisioning(provisioning.New(reopened.DB())),
		WithOwner(owner.New(reopened.DB()), "test-bootstrap-token"), WithPricing(pricing.New(reopened.DB())),
	).Handler()
	if w := call(second, "POST", "/api/v1/owner/login", `{"username":"owner","password":"wrong password entirely"}`, nil, ""); w.Code != 401 {
		t.Fatalf("wrong password accepted after restart: %d %s", w.Code, w.Body)
	}
	if w := call(second, "GET", "/api/v1/owner/pricing", "", &http.Cookie{Name: ownerCookie, Value: strings.Repeat("a", 64)}, ""); w.Code != 401 {
		t.Fatalf("forged session accepted after restart: %d %s", w.Code, w.Body)
	}
	read := call(second, "GET", "/api/v1/owner/pricing", "", cookie, "")
	if read.Code != 200 {
		t.Fatalf("pricing read after restart: %d %s", read.Code, read.Body)
	}
	if !strings.Contains(read.Body.String(), `"unitPriceMinor":400`) || !strings.Contains(read.Body.String(), `"currency":"INR"`) {
		t.Fatalf("pricing not restored: %s", read.Body)
	}
	var book pricing.Book
	if err := json.Unmarshal(read.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 3 {
		t.Fatalf("entries after restart = %d, want 3", len(book.Entries))
	}
	if w := call(second, "POST", "/api/v1/orders", `{"items":[]}`, nil, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("order intake after restart: %d %s", w.Code, w.Body)
	}
	status, err := provisioning.New(reopened.DB()).Status(context.Background())
	if err != nil || status.Next != provisioning.GatePrinter || status.ProductionReady {
		t.Fatalf("gate state after restart: %+v %v", status, err)
	}
}

func TestPricingAPIDerivesPrecisionAndRequiresAConfirmedCorrection(t *testing.T) {
	handler, _ := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}

	// The business read reports the derived exponent so the screen can convert a
	// typed amount without ever asking the merchant for a precision.
	business := call(handler, "GET", "/api/v1/owner/business", "", cookie, "")
	if business.Code != 200 {
		t.Fatalf("business read: %d %s", business.Code, business.Body)
	}
	if !strings.Contains(business.Body.String(), `"currencyMinorUnits":2`) || !strings.Contains(business.Body.String(), `"currencySupported":true`) {
		t.Fatalf("business read did not report derived precision: %s", business.Body)
	}

	saved := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, csrf)
	if saved.Code != 200 {
		t.Fatalf("pricing save: %d %s", saved.Code, saved.Body)
	}
	if !strings.Contains(saved.Body.String(), `"currencyMinorUnits":2`) || !strings.Contains(saved.Body.String(), `"derivedMinorUnits":2`) {
		t.Fatalf("saved book did not report the derived exponent: %s", saved.Body)
	}

	// Switching to a zero-decimal currency leaves the stored integers alone and
	// reports the difference rather than reinterpreting them.
	jpy := strings.Replace(businessBody, `"currency":"INR"`, `"currency":"JPY"`, 1)
	if w := call(handler, "PUT", "/api/v1/owner/business", jpy, cookie, csrf); w.Code != 200 {
		t.Fatalf("currency change: %d %s", w.Code, w.Body)
	}
	read := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, "")
	if read.Code != 200 {
		t.Fatalf("pricing read after currency change: %d %s", read.Code, read.Body)
	}
	var flagged pricing.Book
	if err := json.Unmarshal(read.Body.Bytes(), &flagged); err != nil {
		t.Fatal(err)
	}
	if !flagged.PrecisionMismatch || flagged.CurrencyMinorUnits != 2 || flagged.DerivedMinorUnits != 0 || !flagged.CurrencySupported {
		t.Fatalf("precision change not reported: %+v", flagged)
	}
	// Stored amounts must be preserved verbatim — never rescaled by a currency
	// change. Verify every entry by paperSize + colourMode + sides rather than
	// relying on a positional index, because SQLite's BINARY collation sorts
	// 'colour' before 'monochrome'.
	wantEntries := map[string]int64{
		"A4|monochrome|one-sided":           250,
		"A4|monochrome|two-sided-long-edge": 400,
		"A4|colour|one-sided":               1500,
	}
	if len(flagged.Entries) != len(wantEntries) {
		t.Fatalf("stored amounts altered by the currency change: %+v", flagged.Entries)
	}
	gotEntries := map[string]int64{}
	for _, entry := range flagged.Entries {
		key := entry.PaperSize + "|" + entry.ColourMode + "|" + entry.Sides
		gotEntries[key] = entry.UnitPriceMinor
	}
	for key, want := range wantEntries {
		if got := gotEntries[key]; got != want {
			t.Fatalf("stored amount altered for %s: got %d, want %d: %+v", key, got, want, flagged.Entries)
		}
	}

	// Saving without confirmation is refused and the stored book is untouched.
	refused := call(handler, "PUT", "/api/v1/owner/pricing", pricingBody, cookie, csrf)
	if refused.Code != 409 {
		t.Fatalf("unconfirmed precision change: %d %s", refused.Code, refused.Body)
	}
	if !strings.Contains(refused.Body.String(), "decimal precision") {
		t.Fatalf("refusal does not explain the correction: %s", refused.Body)
	}
	after := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, "")
	if !strings.Contains(after.Body.String(), `"currencyMinorUnits":2`) || !strings.Contains(after.Body.String(), `"unitPriceMinor":1500`) {
		t.Fatalf("refused save changed stored prices: %s", after.Body)
	}

	// The merchant's explicit confirmation stores exactly what they re-entered.
	confirmed := call(handler, "PUT", "/api/v1/owner/pricing",
		`{"confirmPrecisionCorrection":true,"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250}]}`,
		cookie, csrf)
	if confirmed.Code != 200 {
		t.Fatalf("confirmed correction: %d %s", confirmed.Code, confirmed.Body)
	}
	var corrected pricing.Book
	if err := json.Unmarshal(confirmed.Body.Bytes(), &corrected); err != nil {
		t.Fatal(err)
	}
	if corrected.Currency != "JPY" || corrected.CurrencyMinorUnits != 0 || corrected.DerivedMinorUnits != 0 || corrected.PrecisionMismatch {
		t.Fatalf("correction not adopted: %+v", corrected)
	}
	if len(corrected.Entries) != 1 || corrected.Entries[0].UnitPriceMinor != 250 {
		t.Fatalf("confirmed amount rescaled: %+v", corrected.Entries)
	}
}

func TestPricingAPIWithTiersPersistsAndCalculatesCorrectly(t *testing.T) {
	handler, database := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	withTiers := `{"entries":[
{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[
{"minQuantity":50,"unitPriceMinor":200},
{"minQuantity":200,"unitPriceMinor":150}]},
{"paperSize":"A4","colourMode":"colour","sides":"one-sided","unitPriceMinor":1500},
{"paperSize":"A3","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":600,"tiers":[
{"minQuantity":100,"unitPriceMinor":500}]}]}`
	saved := call(handler, "PUT", "/api/v1/owner/pricing", withTiers, cookie, csrf)
	if saved.Code != 200 {
		t.Fatalf("save with tiers: %d %s", saved.Code, saved.Body)
	}
	var book pricing.Book
	if err := json.Unmarshal(saved.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(book.Entries))
	}
	wanted := map[string]struct {
		base  int64
		tiers []pricing.Tier
	}{
		"A4|monochrome|one-sided": {250, []pricing.Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 200, UnitPriceMinor: 150}}},
		"A4|colour|one-sided":     {1500, nil},
		"A3|monochrome|one-sided": {600, []pricing.Tier{{MinQuantity: 100, UnitPriceMinor: 500}}},
	}
	got := map[string]pricing.Entry{}
	for _, e := range book.Entries {
		got[e.PaperSize+"|"+e.ColourMode+"|"+e.Sides] = e
	}
	for key, want := range wanted {
		entry, ok := got[key]
		if !ok {
			t.Fatalf("entry %q missing in response: %+v", key, book.Entries)
		}
		if entry.UnitPriceMinor != want.base {
			t.Fatalf("%s base = %d, want %d", key, entry.UnitPriceMinor, want.base)
		}
		if len(entry.Tiers) != len(want.tiers) {
			t.Fatalf("%s tiers = %+v, want %+v", key, entry.Tiers, want.tiers)
		}
		for index := range entry.Tiers {
			if entry.Tiers[index] != want.tiers[index] {
				t.Fatalf("%s tier %d = %+v, want %+v", key, index, entry.Tiers[index], want.tiers[index])
			}
		}
	}
	var ruleCount, tierCount int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM pricing_rules`).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	if ruleCount != 3 {
		t.Fatalf("rules = %d, want 3", ruleCount)
	}
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM pricing_tiers`).Scan(&tierCount); err != nil {
		t.Fatal(err)
	}
	if tierCount != 3 {
		t.Fatalf("tiers = %d, want 3", tierCount)
	}
	if w := call(handler, "POST", "/api/v1/orders", `{"items":[]}`, nil, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("order intake with tiers: %d %s", w.Code, w.Body)
	}
}

func TestPricingAPIRejectsInvalidTiers(t *testing.T) {
	handler, _ := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	cases := []struct {
		name string
		body string
		code int
	}{
		{"duplicate threshold", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":200},{"minQuantity":50,"unitPriceMinor":150}]}]}`, 400},
		{"first tier not cheaper than base", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":250}]}]}`, 400},
		{"higher quantity more expensive", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":200},{"minQuantity":100,"unitPriceMinor":220}]}]}`, 400},
		{"tier minQuantity = 1", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":1,"unitPriceMinor":200}]}]}`, 400},
		{"tier minQuantity = 0", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":0,"unitPriceMinor":200}]}]}`, 400},
		{"tier above ceiling", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":100000001}]}]}`, 400},
		{"tier fractional quantity", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50.5,"unitPriceMinor":200}]}]}`, 400},
		{"tier quoted quantity", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":"50","unitPriceMinor":200}]}]}`, 400},
		{"non-array tiers rejected", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":"50"}]}`, 400},
		{"free tier without confirmation", `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":100,"unitPriceMinor":0}]}]}`, 409},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := call(handler, "PUT", "/api/v1/owner/pricing", c.body, cookie, csrf)
			if w.Code != c.code {
				t.Fatalf("code = %d body = %s, want %d", w.Code, w.Body, c.code)
			}
		})
	}
	read := call(handler, "GET", "/api/v1/owner/pricing", "", cookie, "")
	if read.Code != 404 {
		t.Fatalf("nothing should be stored after rejected tier saves: %d %s", read.Code, read.Body)
	}
}

func TestPricingAPIConfirmsFreeTier(t *testing.T) {
	handler, _ := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	body := `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":100,"unitPriceMinor":0}]}]}`
	refused := call(handler, "PUT", "/api/v1/owner/pricing", body, cookie, csrf)
	if refused.Code != 409 {
		t.Fatalf("free tier without confirmation: %d %s", refused.Code, refused.Body)
	}
	if !strings.Contains(refused.Body.String(), "free") {
		t.Fatalf("refusal must explain the free tier: %s", refused.Body)
	}
	confirmed := call(handler, "PUT", "/api/v1/owner/pricing",
		`{"confirmFreePricing":true,"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":100,"unitPriceMinor":0}]}]}`,
		cookie, csrf)
	if confirmed.Code != 200 {
		t.Fatalf("confirmed free tier: %d %s", confirmed.Code, confirmed.Body)
	}
	var book pricing.Book
	if err := json.Unmarshal(confirmed.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 1 || len(book.Entries[0].Tiers) != 1 || book.Entries[0].Tiers[0].UnitPriceMinor != 0 {
		t.Fatalf("free tier not stored verbatim: %+v", book.Entries)
	}
}

func TestPricingAPIPersistsTiersAcrossRestart(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "tiers-restart.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	setup := provisioning.New(database.DB())
	if err := setup.CompleteGate(context.Background(), provisioning.GateLicence, "verified test licence"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	first := New("127.0.0.1:8080", "test",
		WithStoreHealth(database), WithProvisioning(setup),
		WithOwner(accounts, "test-bootstrap-token"), WithPricing(pricing.New(database.DB())),
	).Handler()
	cookie, csrf := signedInOwner(t, first)
	if w := call(first, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	withTiers := `{"entries":[
{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[
{"minQuantity":50,"unitPriceMinor":200},
{"minQuantity":200,"unitPriceMinor":150}]}]}`
	if w := call(first, "PUT", "/api/v1/owner/pricing", withTiers, cookie, csrf); w.Code != 200 {
		t.Fatalf("pricing save: %d %s", w.Code, w.Body)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := New("127.0.0.1:8080", "test",
		WithStoreHealth(reopened), WithProvisioning(provisioning.New(reopened.DB())),
		WithOwner(owner.New(reopened.DB()), "test-bootstrap-token"), WithPricing(pricing.New(reopened.DB())),
	).Handler()
	read := call(second, "GET", "/api/v1/owner/pricing", "", cookie, "")
	if read.Code != 200 {
		t.Fatalf("pricing read after restart: %d %s", read.Code, read.Body)
	}
	if !strings.Contains(read.Body.String(), `"minQuantity":50`) || !strings.Contains(read.Body.String(), `"unitPriceMinor":200`) ||
		!strings.Contains(read.Body.String(), `"minQuantity":200`) || !strings.Contains(read.Body.String(), `"unitPriceMinor":150`) {
		t.Fatalf("tiers not restored verbatim: %s", read.Body)
	}
	if w := call(second, "POST", "/api/v1/orders", `{"items":[]}`, nil, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("order intake after tiers restart: %d %s", w.Code, w.Body)
	}
}

func TestPricingAPIRemovedCombinationDropsItsTiers(t *testing.T) {
	handler, database := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	both := `{"entries":[
{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":200}]},
{"paperSize":"A4","colourMode":"colour","sides":"one-sided","unitPriceMinor":1500,"tiers":[{"minQuantity":100,"unitPriceMinor":1200}]}]}`
	if w := call(handler, "PUT", "/api/v1/owner/pricing", both, cookie, csrf); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	justMono := `{"entries":[
{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":200}]}]}`
	if w := call(handler, "PUT", "/api/v1/owner/pricing", justMono, cookie, csrf); w.Code != 200 {
		t.Fatalf("save without colour: %d %s", w.Code, w.Body)
	}
	var orphan int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM pricing_tiers t WHERE NOT EXISTS (SELECT 1 FROM pricing_rules r WHERE r.id = t.pricing_rule_id)`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if orphan != 0 {
		t.Fatalf("orphan tiers after removing combination: %d", orphan)
	}
}

func TestPricingAPIRejectsTiersWithoutAuthAndCSRF(t *testing.T) {
	handler, _ := pricingFixture(t)
	cookie, csrf := signedInOwner(t, handler)
	if w := call(handler, "PUT", "/api/v1/owner/business", businessBody, cookie, csrf); w.Code != 200 {
		t.Fatalf("business save: %d %s", w.Code, w.Body)
	}
	body := `{"entries":[{"paperSize":"A4","colourMode":"monochrome","sides":"one-sided","unitPriceMinor":250,"tiers":[{"minQuantity":50,"unitPriceMinor":200}]}]}`
	if w := call(handler, "PUT", "/api/v1/owner/pricing", body, nil, ""); w.Code != 401 {
		t.Fatalf("missing session: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "PUT", "/api/v1/owner/pricing", body, cookie, ""); w.Code != 403 {
		t.Fatalf("missing CSRF: %d %s", w.Code, w.Body)
	}
	if w := call(handler, "PUT", "/api/v1/owner/pricing", body, cookie, "forged-token"); w.Code != 403 {
		t.Fatalf("forged CSRF: %d %s", w.Code, w.Body)
	}
}
