package passport_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

type fixture struct {
	db       *store.Store
	files    *localfiles.Files
	dataDir  string
	document string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dataDir := t.TempDir()
	databasePath := filepath.Join(t.TempDir(), "passports.sqlite")
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.DB().Exec(`INSERT INTO business_profile (singleton, profile_json, updated_at) VALUES (1, '{"name":"Test","address":"","phone":"","country":"IN","currency":"INR","locale":"en-IN","timeZone":"Asia/Kolkata"}', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor, customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate) VALUES ('TESTORDER', 'share', 'pending_payment', 'INR', 2, 0, '', '', '', '', 1700000000, 1700000000, 0)`); err != nil {
		t.Fatal(err)
	}
	files, err := localfiles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.MkdirAll("documents"); err != nil {
		t.Fatal(err)
	}
	fx := &fixture{db: database, files: files, dataDir: dataDir}
	fx.document = fx.writePhoto(t)
	return fx
}

// writePhoto writes a synthetic portrait image to the documents directory
// and inserts a matching documents row so the service can resolve it by id.
// The image is a 400x600 portrait with the upper half light grey (the
// "face region" the geometric detector will return) and the lower half
// dark grey (the "body" — irrelevant for the test).
func (f *fixture) writePhoto(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.NRGBA{R: 220, G: 220, B: 220, A: 255})
		}
	}
	for y := 300; y < 600; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.NRGBA{R: 40, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.dataDir, "documents", "TESTORDER")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "portrait.png")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	rel := "documents/TESTORDER/portrait.png"
	if _, err := f.db.DB().Exec(`INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until) VALUES ('doc-portrait', 'TESTORDER', 'portrait.png', 'image/png', ?, 1, 'deadbeef', ?, 1700000000, 1700604800)`, buf.Len(), rel); err != nil {
		t.Fatal(err)
	}
	return "doc-portrait"
}

func TestCreateSessionPersistsAndAutoDetects(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	sess, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetIndiaPassport,
		Background: passport.BackgroundReplaceWhite,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("session id empty")
	}
	if sess.Status != "auto_detected" {
		t.Errorf("status = %q, want auto_detected", sess.Status)
	}
	if sess.FaceRegion == nil {
		t.Fatal("face region is nil after auto-detect")
	}
	if !sess.FaceRegion.Bounds().In(image.Rect(0, 0, 400, 600)) {
		t.Errorf("face region out of source bounds: %+v", sess.FaceRegion.Bounds())
	}
}

func TestListReturnsCreatedSessions(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	for i := 0; i < 3; i++ {
		if _, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
			DocumentID: f.document,
			Preset:     passport.PresetIndiaPassport,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want 3", len(sessions))
	}
}

func TestSetFaceRegionMarksManualTrue(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	sess, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetIndiaPassport,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.SetFaceRegion(context.Background(), sess.ID, passport.FaceRegion{
		X: 50, Y: 50, Width: 300, Height: 360,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.FaceRegion.Manual {
		t.Fatal("Manual flag not set after SetFaceRegion")
	}
	if updated.FaceRegion.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 after manual confirmation", updated.FaceRegion.Confidence)
	}
	if updated.Status != "manual_confirmed" {
		t.Errorf("Status = %q, want manual_confirmed", updated.Status)
	}
}

func TestRemoveSoftDeletesSession(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	sess, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetIndiaPassport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(context.Background(), sess.ID); err != passport.ErrNotFound {
		t.Fatalf("Get after Remove = %v, want %v", err, passport.ErrNotFound)
	}
}

func TestComposeWritesPNGBesideTheSession(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	sess, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetIndiaPassport,
		Sheet:      passport.SheetA4,
	})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := svc.Compose(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if composed.Status != "composed" {
		t.Errorf("status = %q, want composed", composed.Status)
	}
	if len(composed.Outputs) != 1 {
		t.Fatalf("len(outputs) = %d, want 1", len(composed.Outputs))
	}
	out := composed.Outputs[0]
	if out.SHA256 == "" {
		t.Fatal("output SHA-256 empty")
	}
	if out.PhotoCount < 4 {
		t.Errorf("photo count = %d, want >= 4 (A4 fits multiple India photos)", out.PhotoCount)
	}
	full, err := svc.OutputBlobPath(out.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 8 || !bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("rendered file is not a PNG: %d bytes, prefix %v", len(body), body[:min(8, len(body))])
	}
}

func TestCustomPresetRequiresExplicitDimensions(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	_, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetCustom,
	})
	if err == nil {
		t.Fatal("CreateSession with custom preset and no dimensions should fail")
	}
	if _, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetCustom,
		WidthMm:    60, HeightMm: 80,
	}); err != nil {
		t.Fatalf("CreateSession with custom preset and dimensions should succeed: %v", err)
	}
}

func TestUnknownPresetIsRejected(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	_, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     "mordor_passport",
	})
	if err != passport.ErrUnknownPreset {
		t.Fatalf("CreateSession unknown preset = %v, want %v", err, passport.ErrUnknownPreset)
	}
}

func TestUnknownBackgroundIsRejected(t *testing.T) {
	f := newFixture(t)
	svc := passport.New(f.db.DB(), f.files, passport.GeometricDetector{})
	_, err := svc.CreateSession(context.Background(), passport.CreateSessionInput{
		DocumentID: f.document,
		Preset:     passport.PresetIndiaPassport,
		Background: "neon",
	})
	if err != passport.ErrInvalidBackground {
		t.Fatalf("CreateSession unknown background = %v, want %v", err, passport.ErrInvalidBackground)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}