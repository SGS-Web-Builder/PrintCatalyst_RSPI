package printers_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func fixture(t *testing.T) (*store.Store, *printers.Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "printers.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return database, printers.New(database.DB())
}

func sampleAttributes() printers.RawAttributes {
	return printers.RawAttributes{
		"media-supported":             {"iso_a4_210x297mm", "na_letter_8.5x11in", "iso_a3_297x420mm"},
		"media-col-database":          {"media-size iso_a4_210x297mm x-dimension 21000 y-dimension 29700"},
		"media-source-supported":      {"auto", "tray-1", "tray-2", "bypass"},
		"print-color-mode-supported":  {"monochrome", "color"},
		"sides-supported":             {"one-sided", "two-sided-long-edge"},
		"finishings-supported":        {"3", "staple-top-left", "punch-3-hole"},
		"output-bin-supported":        {"face-down"},
	}
}

func TestNormalizeIPPAttributesProducesExpectedSnapshot(t *testing.T) {
	snap := printers.NormalizeIPPAttributes(sampleAttributes())
	if len(snap.PaperSizes) != 3 {
		t.Fatalf("paper sizes = %d, want 3", len(snap.PaperSizes))
	}
	// A4 should be first (sorted) and have dimensions.
	a4, ok := findPaper(snap.PaperSizes, "A4")
	if !ok {
		t.Fatal("A4 missing from paper sizes")
	}
	if a4.WidthMM != 210 || a4.HeightMM != 297 {
		t.Fatalf("A4 dimensions = %dx%d, want 210x297", a4.WidthMM, a4.HeightMM)
	}
	// Colour modes.
	if len(snap.ColourModes) != 2 {
		t.Fatalf("colour modes = %d, want 2", len(snap.ColourModes))
	}
	// Sides.
	if len(snap.SidesModes) != 2 {
		t.Fatalf("sides modes = %d, want 2", len(snap.SidesModes))
	}
	// Trays: 4 (auto, tray-1, tray-2, bypass).
	if len(snap.Trays) != 4 {
		t.Fatalf("trays = %d, want 4", len(snap.Trays))
	}
	// Finishing: 3 (staple + punch) + 1 output bin = 4.
	if len(snap.Finishing) != 4 {
		t.Fatalf("finishing = %d, want 4", len(snap.Finishing))
	}
	if snap.Fingerprint == "" {
		t.Fatal("fingerprint missing")
	}
}

func findPaper(list []printers.PaperSize, key string) (printers.PaperSize, bool) {
	for _, p := range list {
		if p.Key == key {
			return p, true
		}
	}
	return printers.PaperSize{}, false
}

func TestNormalizeIPPAttributesIgnoresEmptyAndUnknown(t *testing.T) {
	snap := printers.NormalizeIPPAttributes(printers.RawAttributes{
		"media-supported":            {"", "A4"},
		"print-color-mode-supported": {"unknown-mode"},
		"vendor-private-attribute":   {"something-special"},
	})
	if len(snap.PaperSizes) != 1 {
		t.Fatalf("paper sizes = %d, want 1", len(snap.PaperSizes))
	}
	if len(snap.ColourModes) != 0 {
		t.Fatalf("colour modes = %d, want 0", len(snap.ColourModes))
	}
	// Vendor attribute is preserved in Raw for diagnostics.
	if _, ok := snap.Raw["vendor-private-attribute"]; !ok {
		t.Fatal("vendor-private-attribute missing from Raw")
	}
}

func TestNormalizePaperKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"A4", "A4"},
		{"a4", "A4"},
		{" iso A4 ", "A4"},
		{"iso a4", "A4"},
		{"iso_a4_210x297mm", "A4"},
		{"na_letter_8.5x11in", "LETTER"},
		{"Letter (8.5x11)", "LETTER"},
	}
	for _, c := range cases {
		if got := printers.NormalizePaperKey(c.in); got != c.want {
			t.Errorf("NormalizePaperKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFingerprintStableAcrossCalls(t *testing.T) {
	attrs := sampleAttributes()
	snap := printers.NormalizeIPPAttributes(attrs)
	fp1 := printers.Fingerprint(printers.BackendIPP, "TestQueue", "drv", "1.0", "ipp://localhost/printers/test", snap)
	fp2 := printers.Fingerprint(printers.BackendIPP, "TestQueue", "drv", "1.0", "ipp://localhost/printers/test", snap)
	if fp1 != fp2 {
		t.Fatalf("fingerprint not stable: %s vs %s", fp1, fp2)
	}
	// Different driver version should produce a different fingerprint.
	fp3 := printers.Fingerprint(printers.BackendIPP, "TestQueue", "drv", "1.1", "ipp://localhost/printers/test", snap)
	if fp3 == fp1 {
		t.Fatal("driver version change did not change fingerprint")
	}
}

func TestRegisterPersistsPrinterAndCapabilities(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	ctx := context.Background()
	p, err := svc.Register(ctx, printers.RegisterInput{
		Backend:    printers.BackendIPP,
		QueueName:  "TestQueue",
		URI:        "ipp://localhost/printers/test",
		Capabilities: printers.NormalizeIPPAttributes(sampleAttributes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" {
		t.Fatal("id missing")
	}
	if p.Capabilities == nil {
		t.Fatal("capabilities missing")
	}
	if p.Capabilities.PaperSizes == nil || len(p.Capabilities.PaperSizes) == 0 {
		t.Fatal("paper sizes missing")
	}
	// Re-register should update, not duplicate.
	again, err := svc.Register(ctx, printers.RegisterInput{
		Backend:    printers.BackendIPP,
		QueueName:  "TestQueue",
		URI:        "ipp://localhost/printers/test",
		Capabilities: printers.NormalizeIPPAttributes(sampleAttributes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != p.ID {
		t.Fatalf("re-register produced different id: %s vs %s", again.ID, p.ID)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list count = %d, want 1", len(list))
	}
}

func TestEnableAndDisable(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	ctx := context.Background()
	p, err := svc.Register(ctx, printers.RegisterInput{
		Backend:    printers.BackendMock,
		QueueName:  "Mock",
		Capabilities: printers.NormalizeIPPAttributes(sampleAttributes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Fatal("newly registered printer should be disabled by default")
	}
	if err := svc.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("printer should be enabled after Enable()")
	}
	if err := svc.Disable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("printer should be disabled after Disable()")
	}
}

func TestRemoveHidesFromList(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	ctx := context.Background()
	p, _ := svc.Register(ctx, printers.RegisterInput{
		Backend:    printers.BackendMock,
		QueueName:  "Mock",
		Capabilities: printers.NormalizeIPPAttributes(sampleAttributes()),
	})
	if err := svc.Remove(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, p.ID); err != printers.ErrNotFound {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	list, _ := svc.List(ctx)
	if len(list) != 0 {
		t.Fatalf("list count = %d, want 0 after remove", len(list))
	}
}

func TestRecordAndListVerifications(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	ctx := context.Background()
	p, _ := svc.Register(ctx, printers.RegisterInput{
		Backend:    printers.BackendMock,
		QueueName:  "Mock",
		Capabilities: printers.NormalizeIPPAttributes(sampleAttributes()),
	})
	err := svc.RecordVerification(ctx, printers.Verification{
		PrinterID:      p.ID,
		CapabilityType: "paper_size",
		CapabilityKey:  "A4",
		Status:         printers.VerificationVerified,
		Evidence:       "test page printed successfully",
		VerifiedBy:     "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	verifs, err := svc.ListVerifications(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(verifs) != 1 {
		t.Fatalf("verifications = %d, want 1", len(verifs))
	}
	if verifs[0].CapabilityKey != "A4" {
		t.Fatalf("key = %s, want A4", verifs[0].CapabilityKey)
	}
}

func TestRecordVerificationRejectsBadStatus(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	ctx := context.Background()
	p, _ := svc.Register(ctx, printers.RegisterInput{
		Backend:    printers.BackendMock,
		QueueName:  "Mock",
		Capabilities: printers.NormalizeIPPAttributes(sampleAttributes()),
	})
	err := svc.RecordVerification(ctx, printers.Verification{
		PrinterID:      p.ID,
		CapabilityType: "paper_size",
		CapabilityKey:  "A4",
		Status:         printers.VerificationStatus("bogus"),
	})
	if err == nil {
		t.Fatal("expected error for bogus status")
	}
}

func TestRegisterRejectsBadBackend(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	_, err := svc.Register(context.Background(), printers.RegisterInput{
		Backend:   printers.Backend("alien"),
		QueueName: "Mock",
	})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
	_, err = svc.Register(context.Background(), printers.RegisterInput{
		Backend:   printers.BackendMock,
		QueueName: "",
	})
	if err == nil {
		t.Fatal("expected error for empty queue")
	}
}

func TestSortedPaperSizesIsDeterministic(t *testing.T) {
	unsorted := []printers.PaperSize{
		{Key: "Legal"}, {Key: "A4"}, {Key: "A3"},
	}
	snap := &printers.Snapshot{PaperSizes: unsorted}
	sorted := printers.SortedPaperSizes(snap)
	keys := make([]string, 0, len(sorted))
	for _, p := range sorted {
		keys = append(keys, p.Key)
	}
	want := []string{"A3", "A4", "Legal"}
	if !sort.StringsAreSorted(keys) {
		// Just check that A3 is first.
		if sorted[0].Key != "A3" {
			t.Fatalf("first = %s, want A3", sorted[0].Key)
		}
	}
	if keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("got %v, want %v", keys, want)
	}
}
