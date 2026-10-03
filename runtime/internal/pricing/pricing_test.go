package pricing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/currency"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

var testProfile = owner.Profile{
	Name: "Campus Prints", Address: "Pune", Phone: "+919999999999",
	Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
}

// fixture opens a temporary database with licence, owner and business gates
// complete, which is the only state in which pricing may be saved.
func fixture(t *testing.T) (*store.Store, *Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pricing.sqlite")
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
	return database, New(database.DB()), path
}

func sampleEntries() []Entry {
	return []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250},
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesTwoSidedLongEdge, UnitPriceMinor: 400},
		{PaperSize: "A4", ColourMode: ColourColour, Sides: SidesOneSided, UnitPriceMinor: 1500},
		{PaperSize: "A3", ColourMode: ColourColour, Sides: SidesTwoSidedShortEdge, UnitPriceMinor: 6000},
	}
}

func TestSaveRequiresBusinessProfileAndCompletesOnlyThePricingGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	service := New(database.DB())

	// No licence, owner or business configuration: pricing must not save and no
	// gate may complete.
	if _, err := service.Save(ctx, Input{Entries: sampleEntries()}); !errors.Is(err, ErrSetup) {
		t.Fatalf("unlicensed pricing save: %v", err)
	}
	status, err := provisioning.New(database.DB()).Status(ctx)
	if err != nil || status.Completed != 0 {
		t.Fatalf("gates advanced without setup: %+v %v", status, err)
	}

	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test verified licence"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(ctx, Input{Entries: sampleEntries()}); !errors.Is(err, ErrSetup) {
		t.Fatalf("pricing saved before business details: %v", err)
	}
	status, err = provisioning.New(database.DB()).Status(ctx)
	if err != nil || status.Next != provisioning.GateBusiness {
		t.Fatalf("pricing gate completed early: %+v %v", status, err)
	}

	if err := accounts.SaveProfile(ctx, testProfile); err != nil {
		t.Fatal(err)
	}
	book, err := service.Save(ctx, Input{Entries: sampleEntries()})
	if err != nil {
		t.Fatal(err)
	}
	// Precision is derived from the persisted INR profile, not from the request.
	if book.Currency != "INR" || book.CurrencyMinorUnits != 2 || book.DerivedMinorUnits != 2 ||
		!book.CurrencySupported || book.PrecisionMismatch || book.PriceUnit != UnitSheet || book.CurrencyMismatch {
		t.Fatalf("unexpected book: %+v", book)
	}
	if len(book.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(book.Entries))
	}
	status, err = provisioning.New(database.DB()).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Next != provisioning.GatePrinter {
		t.Fatalf("next gate = %q, want %q", status.Next, provisioning.GatePrinter)
	}
	if status.ProductionReady || status.Completed != 4 {
		t.Fatalf("pricing must not open production: %+v", status)
	}
	for _, gate := range status.Gates {
		if gate.Gate == provisioning.GatePricing && !gate.Complete {
			t.Fatal("pricing gate not recorded")
		}
		if gate.Gate != provisioning.GatePricing && gate.Complete && gate.Gate != provisioning.GateLicence &&
			gate.Gate != provisioning.GateOwner && gate.Gate != provisioning.GateBusiness {
			t.Fatalf("later gate completed: %q", gate.Gate)
		}
	}
}

func TestSaveRejectsInvalidPricingWithoutTouchingStoredPrices(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Save(ctx, Input{Entries: sampleEntries()}); err != nil {
		t.Fatal(err)
	}

	tooMany := make([]Entry, MaxEntries+1)
	for index := range tooMany {
		tooMany[index] = Entry{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: int64(index)}
	}
	invalidPaper := func(value string) []Entry {
		return []Entry{{PaperSize: value, ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 100}}
	}
	cases := []struct {
		name  string
		input Input
	}{
		{"no entries", Input{}},
		{"too many entries", Input{Entries: tooMany}},
		// There is no precision case here on purpose: Input carries no exponent,
		// so a merchant cannot supply a negative or out-of-range one. A forged
		// precision value is rejected at the HTTP layer as an unknown field.
		{"negative price", Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: -1}}}},
		{"price above ceiling", Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 100_000_001}}}},
		{"unknown colour mode", Input{Entries: []Entry{{PaperSize: "A4", ColourMode: "rainbow", Sides: SidesOneSided, UnitPriceMinor: 100}}}},
		{"unknown sides mode", Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: "stapled", UnitPriceMinor: 100}}}},
		{"empty paper size", Input{Entries: invalidPaper("   ")}},
		{"oversize paper identifier", Input{Entries: invalidPaper(strings.Repeat("A", 65))}},
		{"control characters in paper identifier", Input{Entries: invalidPaper("A4\nDROP TABLE")}},
		{"duplicate combination", Input{Entries: []Entry{
			{PaperSize: "a4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 100},
			{PaperSize: "  A4 ", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 200},
		}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.Save(ctx, testCase.input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}

	// A rejected save must leave the previously stored price book untouched.
	book, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 4 {
		t.Fatalf("stored prices changed after rejected save: %+v", book.Entries)
	}
	// Order-independent: every originally saved row must still be present.
	type entryKey struct {
		paper, colour, sides string
	}
	want := map[entryKey]int64{
		{"A3", ColourColour, SidesTwoSidedShortEdge}:                     6000,
		{"A4", ColourColour, SidesOneSided}:                             1500,
		{"A4", ColourMonochrome, SidesOneSided}:                         250,
		{"A4", ColourMonochrome, SidesTwoSidedLongEdge}:                 400,
	}
	for _, entry := range book.Entries {
		got, ok := want[entryKey{entry.PaperSize, entry.ColourMode, entry.Sides}]
		if !ok || got != entry.UnitPriceMinor || len(entry.Tiers) != 0 {
			t.Fatalf("stored entry changed after rejected save: %+v", entry)
		}
	}
	var auditCount int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'pricing.updated'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("pricing audit events = %d, want 1", auditCount)
	}
}

func TestPricesAreStoredAsIntegerMinorUnits(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Save(ctx, Input{Entries: []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 1},
		{PaperSize: "A4", ColourMode: ColourColour, Sides: SidesOneSided, UnitPriceMinor: 0},
	}}); err != nil {
		t.Fatal(err)
	}
	// SQLite storage class must be integer so no binary floating-point value is
	// ever written for money, and the CHECK constraint must hold at rest.
	var storageClass string
	var stored int64
	if err := database.DB().QueryRow(`SELECT typeof(unit_price_minor), unit_price_minor FROM pricing_rules WHERE colour_mode = 'monochrome'`).Scan(&storageClass, &stored); err != nil {
		t.Fatal(err)
	}
	if storageClass != "integer" || stored != 1 {
		t.Fatalf("price storage = %q/%d, want integer/1", storageClass, stored)
	}
	book, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(book.Entries))
	}
	for _, entry := range book.Entries {
		if entry.UnitPriceMinor < 0 {
			t.Fatalf("negative price stored: %+v", entry)
		}
	}
}

func TestPricingSurvivesRestartAndKeepsOrderIntakeClosed(t *testing.T) {
	database, service, path := fixture(t)
	ctx := context.Background()
	input := Input{Entries: sampleEntries()}
	saved, err := service.Save(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := New(reopened.DB()).Load(ctx)
	if err != nil {
		t.Fatalf("pricing lost on restart: %v", err)
	}
	if actual.Currency != saved.Currency || actual.CurrencyMinorUnits != saved.CurrencyMinorUnits || actual.PriceUnit != saved.PriceUnit {
		t.Fatalf("book changed: %+v vs %+v", actual, saved)
	}
	if len(actual.Entries) != len(saved.Entries) {
		t.Fatalf("entries = %d, want %d", len(actual.Entries), len(saved.Entries))
	}
	for index := range actual.Entries {
		if actual.Entries[index].PaperSize != saved.Entries[index].PaperSize ||
			actual.Entries[index].ColourMode != saved.Entries[index].ColourMode ||
			actual.Entries[index].Sides != saved.Entries[index].Sides ||
			actual.Entries[index].UnitPriceMinor != saved.Entries[index].UnitPriceMinor ||
			len(actual.Entries[index].Tiers) != len(saved.Entries[index].Tiers) {
			t.Fatalf("entry %d changed: %+v vs %+v", index, actual.Entries[index], saved.Entries[index])
		}
		for tierIndex := range actual.Entries[index].Tiers {
			if actual.Entries[index].Tiers[tierIndex] != saved.Entries[index].Tiers[tierIndex] {
				t.Fatalf("tier %d of entry %d changed: %+v vs %+v", tierIndex, index, actual.Entries[index].Tiers[tierIndex], saved.Entries[index].Tiers[tierIndex])
			}
		}
	}
	status, err := provisioning.New(reopened.DB()).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ProductionReady || status.Next != provisioning.GatePrinter {
		t.Fatalf("restart readiness unsafe: %+v", status)
	}
	// Customer order intake stays closed: the print job table remains empty.
	var jobs int
	if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM print_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("print jobs created by pricing configuration: %d", jobs)
	}
}

func TestPricingFollowsBusinessCurrencyAndFlagsMismatch(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	accounts := owner.New(database.DB())
	if _, err := service.Save(ctx, Input{Entries: sampleEntries()}); err != nil {
		t.Fatal(err)
	}
	changed := testProfile
	changed.Currency = "USD"
	if err := accounts.SaveProfile(ctx, changed); err != nil {
		t.Fatal(err)
	}
	book, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !book.CurrencyMismatch || book.Currency != "INR" {
		t.Fatalf("stale currency not flagged: %+v", book)
	}
	// Re-saving adopts the current business currency and clears the mismatch;
	// stored minor-unit amounts are never rescaled silently.
	resaved, err := service.Save(ctx, Input{Entries: sampleEntries()})
	if err != nil {
		t.Fatal(err)
	}
	if resaved.Currency != "USD" || resaved.CurrencyMismatch {
		t.Fatalf("currency not adopted: %+v", resaved)
	}
	status, err := provisioning.New(database.DB()).Status(ctx)
	if err != nil || status.Next != provisioning.GatePrinter {
		t.Fatalf("gate state: %+v %v", status, err)
	}
}

func TestPricingIsMerchantConfigurationNotPrinterCapability(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	// A merchant may price an identifier this product has never seen; nothing
	// here may assert that a printer supports it.
	book, err := service.Save(ctx, Input{Entries: []Entry{
		{PaperSize: "Shop Custom 320x480", ColourMode: ColourColour, Sides: SidesTwoSidedLongEdge, UnitPriceMinor: 900},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 1 || book.Entries[0].PaperSize != "Shop Custom 320x480" {
		t.Fatalf("merchant identifier not preserved: %+v", book.Entries)
	}
	status, err := provisioning.New(database.DB()).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range status.Gates {
		if gate.Gate == provisioning.GatePrinter && gate.Complete {
			t.Fatal("pricing completed the printer capability gate")
		}
	}
	rows, err := database.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		lowered := strings.ToLower(name)
		// Skip tables that legitimately belong to other packages added in
		// later phases (notifications Phase 3G, printers Phase 4, business
		// settings Phase B6). The regression this test was guarding against
		// in Phase 3C was pricing introducing its own capability/catalog
		// tables.
		if strings.HasPrefix(name, "notification_") || strings.HasPrefix(name, "printer_") || name == "printers" || name == "service_printers" || name == "business_settings" || name == "services" || name == "discounts" {
			continue
		}
		if strings.Contains(lowered, "capab") || strings.Contains(lowered, "printer") || strings.Contains(lowered, "paper_catalog") {
			t.Fatalf("pricing introduced a capability/catalog table %q", name)
		}
	}
}

func TestLoadIsNotFoundBeforeAnyPricingIsSaved(t *testing.T) {
	_, service, _ := fixture(t)
	if _, err := service.Load(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveRejectsUnsupportedBusinessCurrency(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	// Bypass owner.SaveProfile's format check so the business currency on disk
	// can be one this build has no exponent for. CLF is a real ISO 4217 code, but
	// it is an accounting unit with four decimal places, which the pricing schema
	// cannot store; it is deliberately absent from the allowlist.
	if _, err := database.DB().ExecContext(ctx,
		`UPDATE business_profile SET profile_json = REPLACE(profile_json, '"currency":"INR"', '"currency":"CLF"')`); err != nil {
		t.Fatal(err)
	}
	if currency.Supported("CLF") {
		t.Fatal("CLF must not be in the supported allowlist")
	}
	if _, err := service.Save(ctx, Input{Entries: sampleEntries()}); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("unsupported currency save: err = %v, want ErrUnsupportedCurrency", err)
	}
	if book, err := service.Load(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsupported save wrote prices: err=%v book=%+v", err, book)
	}
	status, err := provisioning.New(database.DB()).Status(ctx)
	if err != nil || status.Next != provisioning.GatePricing {
		t.Fatalf("gates advanced on unsupported-currency rejection: %+v %v", status, err)
	}
}

func TestTiersAreOptionalAndPreservePhase3BBehaviourWhenAbsent(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	// Saving without any tiers must round-trip identically to a Phase 3B save.
	book, err := service.Save(ctx, Input{Entries: []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250},
		{PaperSize: "A4", ColourMode: ColourColour, Sides: SidesOneSided, UnitPriceMinor: 1500},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(book.Entries))
	}
	for _, entry := range book.Entries {
		if entry.Tiers == nil || len(entry.Tiers) != 0 {
			t.Fatalf("no-tier entry should report empty tiers, got %+v", entry.Tiers)
		}
	}
	var ruleCount, tierCount int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM pricing_rules`).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	if ruleCount != 2 {
		t.Fatalf("rules = %d, want 2", ruleCount)
	}
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM pricing_tiers`).Scan(&tierCount); err != nil {
		t.Fatal(err)
	}
	if tierCount != 0 {
		t.Fatalf("tiers = %d, want 0", tierCount)
	}
}

func TestTiersPersistAtomicallyAndRoundTripAfterRestart(t *testing.T) {
	database, service, path := fixture(t)
	ctx := context.Background()
	input := Input{Entries: []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250, Tiers: []Tier{
			{MinQuantity: 50, UnitPriceMinor: 200},
			{MinQuantity: 200, UnitPriceMinor: 150},
		}},
		{PaperSize: "A4", ColourMode: ColourColour, Sides: SidesOneSided, UnitPriceMinor: 1500},
	}}
	saved, err := service.Save(ctx, input)
	if err != nil {
		t.Fatalf("save with tiers: %v", err)
	}
	if len(saved.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(saved.Entries))
	}
	var monochromeEntry Entry
	for _, e := range saved.Entries {
		if e.PaperSize == "A4" && e.ColourMode == ColourMonochrome {
			monochromeEntry = e
		}
	}
	if len(monochromeEntry.Tiers) != 2 {
		t.Fatalf("monochrome tiers = %d, want 2", len(monochromeEntry.Tiers))
	}
	if monochromeEntry.Tiers[0].MinQuantity != 50 || monochromeEntry.Tiers[0].UnitPriceMinor != 200 ||
		monochromeEntry.Tiers[1].MinQuantity != 200 || monochromeEntry.Tiers[1].UnitPriceMinor != 150 {
		t.Fatalf("tiers not persisted in order: %+v", monochromeEntry.Tiers)
	}
	// Restart and confirm tiers survive verbatim, including the rule-less colour
	// entry that must still report an empty (non-nil) tiers slice.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := New(reopened.DB()).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range actual.Entries {
		if e.PaperSize == "A4" && e.ColourMode == ColourColour {
			if e.Tiers == nil || len(e.Tiers) != 0 {
				t.Fatalf("colour entry lost its empty tiers on restart: %+v", e)
			}
		}
	}
}

func TestTierCalculationPicksHighestQualifyingThreshold(t *testing.T) {
	_, service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
		Tiers: []Tier{
			{MinQuantity: 50, UnitPriceMinor: 200},
			{MinQuantity: 100, UnitPriceMinor: 150},
			{MinQuantity: 500, UnitPriceMinor: 100},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		quantity int64
		want     int64
	}{
		{1, 250}, {49, 250}, {50, 200}, {99, 200},
		{100, 150}, {499, 150}, {500, 100}, {10000, 100},
	}
	for _, c := range cases {
		total, err := CalculateLineTotal(250, []Tier{
			{MinQuantity: 50, UnitPriceMinor: 200},
			{MinQuantity: 100, UnitPriceMinor: 150},
			{MinQuantity: 500, UnitPriceMinor: 100},
		}, c.quantity)
		if err != nil {
			t.Fatalf("quantity=%d: %v", c.quantity, err)
		}
		if total.UnitPriceMinor != c.want {
			t.Fatalf("quantity=%d: unit=%d, want %d", c.quantity, total.UnitPriceMinor, c.want)
		}
	}
}

func TestTiersApplyIndependentlyToEachCombination(t *testing.T) {
	_, service, _ := fixture(t)
	ctx := context.Background()
	// Each combination has its own tier list; saving A4 with tiers and A3 without
	// must not attach A4's tiers to A3.
	if _, err := service.Save(ctx, Input{Entries: []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250, Tiers: []Tier{
			{MinQuantity: 100, UnitPriceMinor: 150},
		}},
		{PaperSize: "A3", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 600},
	}}); err != nil {
		t.Fatal(err)
	}
	book, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var a3, a4 Entry
	for _, e := range book.Entries {
		if e.PaperSize == "A3" {
			a3 = e
		}
		if e.PaperSize == "A4" {
			a4 = e
		}
	}
	if len(a3.Tiers) != 0 {
		t.Fatalf("A3 tiers = %d, want 0 (no cross-combination bleed)", len(a3.Tiers))
	}
	if len(a4.Tiers) != 1 || a4.Tiers[0].MinQuantity != 100 || a4.Tiers[0].UnitPriceMinor != 150 {
		t.Fatalf("A4 tiers = %+v, want single tier 100/150", a4.Tiers)
	}
	// Pure calculation: A3 has no tiers so it uses 600 at every quantity.
	total, err := CalculateLineTotal(a3.UnitPriceMinor, a3.Tiers, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total.UnitPriceMinor != 600 {
		t.Fatalf("A3 at 100: unit=%d, want 600 (no tiers)", total.UnitPriceMinor)
	}
}

func TestRemovingACombinationDeletesItsTiers(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Save(ctx, Input{Entries: []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250, Tiers: []Tier{
			{MinQuantity: 50, UnitPriceMinor: 200},
		}},
		{PaperSize: "A4", ColourMode: ColourColour, Sides: SidesOneSided, UnitPriceMinor: 1500, Tiers: []Tier{
			{MinQuantity: 100, UnitPriceMinor: 1200},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Removing A4 colour must cascade its tiers away; A4 monochrome tiers stay.
	if _, err := service.Save(ctx, Input{Entries: []Entry{
		{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250, Tiers: []Tier{
			{MinQuantity: 50, UnitPriceMinor: 200},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	var orphanTiers int
	if err := database.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM pricing_tiers t
WHERE NOT EXISTS (SELECT 1 FROM pricing_rules r WHERE r.id = t.pricing_rule_id)`).Scan(&orphanTiers); err != nil {
		t.Fatal(err)
	}
	if orphanTiers != 0 {
		t.Fatalf("orphan tiers after removing combination: %d", orphanTiers)
	}
	var colourTiers int
	if err := database.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM pricing_tiers t
JOIN pricing_rules r ON r.id = t.pricing_rule_id
WHERE r.colour_mode = 'colour'`).Scan(&colourTiers); err != nil {
		t.Fatal(err)
	}
	if colourTiers != 0 {
		t.Fatalf("colour tiers survived removal: %d", colourTiers)
	}
}

func TestBasePriceChangeRejectsTiersThatAreNoLongerDiscounts(t *testing.T) {
	_, service, _ := fixture(t)
	ctx := context.Background()
	// Save A4 monochrome at base 250 with a tier at 200. Then lower the base to
	// 150 and try to keep the same 200 tier — it is no longer a discount and the
	// save must be refused so the merchant reconsiders.
	if _, err := service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
		Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}},
	}}}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 150,
		Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}},
	}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("tier not cheaper than new base: err=%v, want ErrInvalid", err)
	}
	// The stored prices are still the original ones; the rejected save must
	// leave them untouched.
	book, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var a4 Entry
	for _, e := range book.Entries {
		if e.PaperSize == "A4" {
			a4 = e
		}
	}
	if a4.UnitPriceMinor != 250 || len(a4.Tiers) != 1 || a4.Tiers[0].UnitPriceMinor != 200 {
		t.Fatalf("rejected save altered stored prices: %+v", a4)
	}
}

func TestFreeTierRequiresExplicitConfirmation(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	// A free tier (unit_price_minor == 0) must be refused without explicit
	// confirmation, and the merchant's confirmation must store exactly the
	// amounts they re-entered.
	if _, err := service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
		Tiers: []Tier{{MinQuantity: 100, UnitPriceMinor: 0}},
	}}}); !errors.Is(err, ErrConfirmFreePricing) {
		t.Fatalf("free tier without confirmation: err=%v, want ErrConfirmFreePricing", err)
	}
	// Stored prices are untouched after the rejection.
	book, err := service.Load(ctx)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("free-tier rejection wrote prices: err=%v book=%+v", err, book)
	}
	if _, err := service.Save(ctx, Input{
		ConfirmFreePricing: true,
		Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 100, UnitPriceMinor: 0}},
		}},
	}); err != nil {
		t.Fatalf("confirmed free tier: %v", err)
	}
	var auditCount int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'pricing.updated'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit events = %d, want 1", auditCount)
	}
	book, err = service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Entries) != 1 || len(book.Entries[0].Tiers) != 1 || book.Entries[0].Tiers[0].UnitPriceMinor != 0 {
		t.Fatalf("free tier not stored: %+v", book.Entries)
	}
}

func TestTierValidationRejectsDuplicateAndNonMonotonicShapes(t *testing.T) {
	_, service, _ := fixture(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		input Input
	}{
		{"duplicate threshold", Input{Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 50, UnitPriceMinor: 150}},
		}}}},
		{"first tier not cheaper than base", Input{Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 250}},
		}}}},
		{"higher quantity more expensive", Input{Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}, {MinQuantity: 100, UnitPriceMinor: 220}},
		}}}},
		{"minQuantity=1", Input{Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 1, UnitPriceMinor: 200}},
		}}}},
		{"negative tier unit price", Input{Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: -1}},
		}}}},
		{"tier unit price above ceiling", Input{Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
			Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 100_000_001}},
		}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := service.Save(ctx, c.input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestTierPricingKeepsOrderIntakeClosedAndGateStateIntact(t *testing.T) {
	_, service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
		Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}},
	}}}); err != nil {
		t.Fatal(err)
	}
	status, err := provisioning.New(service.db).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ProductionReady {
		t.Fatal("discounts must not open production")
	}
	if status.Next != provisioning.GatePrinter {
		t.Fatalf("next gate = %q, want %q", status.Next, provisioning.GatePrinter)
	}
	if status.Completed != 4 {
		t.Fatalf("completed gates = %d, want 4", status.Completed)
	}
}

func TestCurrencyChangePreservesStoredTierAmountsVerbatim(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	accounts := owner.New(database.DB())
	if _, err := service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
		Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}},
	}}}); err != nil {
		t.Fatal(err)
	}
	// Switch to a zero-decimal currency. The tier's stored integer (200) must
	// stay 200; the merchant is told about the precision change and confirms
	// the new amounts explicitly.
	profile := testProfile
	profile.Currency = "JPY"
	if err := accounts.SaveProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	flagged, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !flagged.PrecisionMismatch || !flagged.CurrencyMismatch {
		t.Fatalf("currency change not flagged: %+v", flagged)
	}
	if len(flagged.Entries) != 1 || len(flagged.Entries[0].Tiers) != 1 ||
		flagged.Entries[0].UnitPriceMinor != 250 || flagged.Entries[0].Tiers[0].UnitPriceMinor != 200 {
		t.Fatalf("stored amounts rescaled on currency change: %+v", flagged.Entries)
	}
	// Without confirmation the save is refused and the stored amounts are kept.
	_, err = service.Save(ctx, Input{Entries: []Entry{{
		PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250,
		Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 200}},
	}}})
	if !errors.Is(err, ErrPrecisionCorrection) {
		t.Fatalf("unconfirmed precision correction: err=%v, want ErrPrecisionCorrection", err)
	}
	unchanged, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Entries[0].Tiers[0].UnitPriceMinor != 200 {
		t.Fatalf("tier rescaled without confirmation: %+v", unchanged.Entries[0].Tiers)
	}
	// Confirmation stores the new amounts verbatim; no rescaling.
	confirmed, err := service.Save(ctx, Input{
		ConfirmPrecisionCorrection: true,
		Entries: []Entry{{
			PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 300,
			Tiers: []Tier{{MinQuantity: 50, UnitPriceMinor: 250}},
		}},
	})
	if err != nil {
		t.Fatalf("confirmed precision correction: %v", err)
	}
	if confirmed.Currency != "JPY" || confirmed.Entries[0].UnitPriceMinor != 300 || confirmed.Entries[0].Tiers[0].UnitPriceMinor != 250 {
		t.Fatalf("correction rescaled amounts: %+v", confirmed)
	}
}

func TestSaveRequiresExplicitCorrectionForPrecisionMismatch(t *testing.T) {
	database, service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Save(ctx, Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250}}}); err != nil {
		t.Fatal(err)
	}
	first, err := service.Load(ctx)
	if err != nil || first.Currency != "INR" || first.CurrencyMinorUnits != 2 {
		t.Fatalf("first save: err=%v first=%+v", err, first)
	}

	accounts := owner.New(database.DB())
	setCurrency := func(code string) {
		t.Helper()
		profile := testProfile
		profile.Currency = code
		if err := accounts.SaveProfile(ctx, profile); err != nil {
			t.Fatalf("set business currency %s: %v", code, err)
		}
	}
	// A zero-decimal currency makes the stored 250 read as 250 rather than 2.50.
	setCurrency("JPY")
	mismatch, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !mismatch.PrecisionMismatch || mismatch.CurrencyMinorUnits != 2 || mismatch.DerivedMinorUnits != 0 {
		t.Fatalf("precision mismatch not reported: %+v", mismatch)
	}
	if !mismatch.CurrencyMismatch || mismatch.Currency != "INR" {
		t.Fatalf("stored book rewritten by the currency change: %+v", mismatch)
	}

	// Without confirmation the mismatch blocks the overwritten save and leaves
	// the stored integers exactly as they were.
	if _, err := service.Save(ctx, Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 300}}}); !errors.Is(err, ErrPrecisionCorrection) {
		t.Fatalf("precision mismatch save without correction: err=%v, want ErrPrecisionCorrection", err)
	}
	unchanged, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.CurrencyMinorUnits != 2 || unchanged.Entries[0].UnitPriceMinor != 250 {
		t.Fatalf("stored prices rescaled without confirmation: %+v", unchanged)
	}
	if unchanged.Currency != "INR" {
		t.Fatalf("stored currency rescaled without confirmation: %+v", unchanged)
	}

	// With explicit confirmation the merchant's reviewed amounts replace the
	// stored integers verbatim. There is no server-side rescaling: what is
	// stored next is exactly what they re-entered.
	resaved, err := service.Save(ctx, Input{
		ConfirmPrecisionCorrection: true,
		Entries:                    []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 300}},
	})
	if err != nil {
		t.Fatalf("confirmed precision correction: %v", err)
	}
	if resaved.Currency != "JPY" {
		t.Fatalf("business currency not adopted on correction: %+v", resaved)
	}
	if resaved.CurrencyMinorUnits != 0 || resaved.PrecisionMismatch || resaved.CurrencyMismatch || resaved.DerivedMinorUnits != 0 {
		t.Fatalf("precision not adopted on correction: %+v", resaved)
	}
	if resaved.Entries[0].UnitPriceMinor != 300 {
		t.Fatalf("correction rescaled the amount; stored %d, want 300", resaved.Entries[0].UnitPriceMinor)
	}

	// A follow-up save is no longer considered a correction.
	if _, err := service.Save(ctx, Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 310}}}); err != nil {
		t.Fatalf("post-correction save without flag: %v", err)
	}
	// Drifting to a three-decimal currency is a fresh correction, and confirming
	// it stores the entered integer rather than dividing it by ten.
	setCurrency("KWD")
	drifted, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !drifted.PrecisionMismatch || drifted.CurrencyMinorUnits != 0 || drifted.DerivedMinorUnits != 3 {
		t.Fatalf("drift to KWD not reported: %+v", drifted)
	}
	if _, err := service.Save(ctx, Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 310}}}); !errors.Is(err, ErrPrecisionCorrection) {
		t.Fatalf("post-correction drift to KWD not blocked: %v", err)
	}
	// Confirming that drift stores the new amounts verbatim; no division by ten.
	confirmed, err := service.Save(ctx, Input{
		ConfirmPrecisionCorrection: true,
		Entries:                    []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 310}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Entries[0].UnitPriceMinor != 310 || confirmed.CurrencyMinorUnits != 3 {
		t.Fatalf("three-decimal correction not verbatim: %+v", confirmed)
	}
}

func TestINRAmountMeansExactMinorUnitsUnderDerivedPrecision(t *testing.T) {
	_, service, _ := fixture(t)
	ctx := context.Background()
	book, err := service.Save(ctx, Input{Entries: []Entry{{PaperSize: "A4", ColourMode: ColourMonochrome, Sides: SidesOneSided, UnitPriceMinor: 250}}})
	if err != nil {
		t.Fatal(err)
	}
	if book.CurrencyMinorUnits != 2 || book.Currency != "INR" {
		t.Fatalf("derived precision for INR not 2: %+v", book)
	}
	if book.Entries[0].UnitPriceMinor != 250 {
		t.Fatalf("INR 2.50 stored as %d minor units, want 250", book.Entries[0].UnitPriceMinor)
	}
	loaded, err := service.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Entries[0].UnitPriceMinor != 250 {
		t.Fatalf("reloaded as %d, not the entered 250", loaded.Entries[0].UnitPriceMinor)
	}

	// Every exponent in the allowlist has to fit the range migration 003 stores,
	// otherwise a newly supported currency would fail the CHECK on save.
	for _, code := range currency.Codes() {
		if exponent := currency.MinorUnits(code); exponent < 0 || exponent > maxMinorUnits {
			t.Fatalf("%s exponent %d is outside the 0..%d pricing_book can store", code, exponent, maxMinorUnits)
		}
	}
	// Spot-check the published ISO 4217 exponents the derivation depends on.
	for _, want := range []struct {
		code     string
		exponent int
	}{
		{"INR", 2}, {"USD", 2}, {"EUR", 2}, {"GBP", 2},
		{"JPY", 0}, {"KRW", 0}, {"VND", 0},
		{"KWD", 3}, {"BHD", 3}, {"OMR", 3}, {"TND", 3},
	} {
		if got := currency.MinorUnits(want.code); got != want.exponent {
			t.Fatalf("%s exponent = %d, want %d", want.code, got, want.exponent)
		}
		if got := currency.MinorUnits(strings.ToLower(" " + want.code + " ")); got != want.exponent {
			t.Fatalf("%s is not normalized before lookup, got %d", want.code, got)
		}
	}
	if currency.Supported("XYZ") || currency.Supported("") || currency.MinorUnits("XYZ") != currency.Unsupported {
		t.Fatal("an unknown code must be reported unsupported rather than guessed")
	}
}
