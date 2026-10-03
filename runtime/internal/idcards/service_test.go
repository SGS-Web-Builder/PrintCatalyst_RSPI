package idcards_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

type fixture struct {
	db     *store.Store
	files  *localfiles.Files
	dataDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dataDir := t.TempDir()
	databasePath := filepath.Join(t.TempDir(), "id-cards.sqlite")
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	// Stub business profile + order so documents.order_id satisfies its FK.
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
	return &fixture{db: database, files: files, dataDir: dataDir}
}

// writeCardImage writes a synthetic card-on-dark-background PNG to the
// fixture's documents directory and inserts a matching documents row so the
// service can resolve it by id. The card is a high-contrast quadrilateral
// inside a 400x300 image so detection always succeeds.
func (f *fixture) writeCardImage(t *testing.T, name string, cardTL, cardTR, cardBR, cardBL image.Point) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	bg := color.RGBA{R: 30, G: 30, B: 30, A: 255}
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, bg)
		}
	}
	bright := color.RGBA{R: 230, G: 230, B: 240, A: 255}
	border := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	drawQuadFill(img, []image.Point{cardTL, cardTR, cardBR, cardBL}, bright)
	drawQuadLine(img, []image.Point{cardTL, cardTR, cardBR, cardBL}, border)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.dataDir, "documents", "TESTORDER")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	relPath := "documents/TESTORDER/" + name + ".png"
	_, err := f.db.DB().Exec(`
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, 'TESTORDER', ?, 'image/png', ?, 1, 'deadbeef', ?, 1700000000, 1700604800)`,
		name, name+".png", buf.Len(), relPath)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func drawQuadFill(img *image.RGBA, pts []image.Point, c color.RGBA) {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if pointInQuad(image.Point{X: x, Y: y}, pts) {
				img.Set(x, y, c)
			}
		}
	}
}

func drawQuadLine(img *image.RGBA, pts []image.Point, c color.RGBA) {
	for i := 0; i < 4; i++ {
		a := pts[i]
		b := pts[(i+1)%4]
		steps := abs(a.X-b.X) + abs(a.Y-b.Y)
		if steps < 1 {
			steps = 1
		}
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			x := int(float64(a.X) + float64(b.X-a.X)*t)
			y := int(float64(a.Y) + float64(b.Y-a.Y)*t)
			bounds := img.Bounds()
			if x >= bounds.Min.X && x < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				img.Set(x, y, c)
			}
		}
	}
}

func pointInQuad(p image.Point, pts []image.Point) bool {
	for i := 0; i < 4; i++ {
		a := pts[i]
		b := pts[(i+1)%4]
		cross := (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
		if cross < 0 {
			return false
		}
	}
	return true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestCreateSessionDetectsCornersAndPersistsLayout(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	backDoc := f.writeCardImage(t, "back",
		image.Point{X: 100, Y: 80}, image.Point{X: 300, Y: 85},
		image.Point{X: 305, Y: 220}, image.Point{X: 95, Y: 215})
	svc := idcards.New(f.db.DB(), f.files)
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc,
		BackDocID: backDoc,
		Sheet: idcards.SheetA4,
		Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutSideBySide,
		ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("session id is empty")
	}
	if sess.FrontCorners == nil {
		t.Fatal("front corners missing")
	}
	if sess.BackCorners == nil {
		t.Fatal("back corners missing")
	}
	if sess.FrontCorners.Confidence < idcards.MinConfidence {
		t.Fatalf("front confidence %f below %f", sess.FrontCorners.Confidence, idcards.MinConfidence)
	}
}

func TestSetCornersMarksManualAndUpdatesStatus(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	svc := idcards.New(f.db.DB(), f.files)
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc, Sheet: idcards.SheetA4, Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.SetCorners(context.Background(), sess.ID, idcards.SideFront, idcards.Corners{
		TL: idcards.Point{X: 80, Y: 60},
		TR: idcards.Point{X: 320, Y: 70},
		BR: idcards.Point{X: 325, Y: 240},
		BL: idcards.Point{X: 75, Y: 235},
		Confidence: 1.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.FrontCorners.Manual {
		t.Fatal("SetCorners did not mark manual")
	}
	if updated.Status != "manual_confirmed" {
		t.Fatalf("status = %s", updated.Status)
	}
}

func TestComposeFrontOnlyWritesPNG(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	svc := idcards.New(f.db.DB(), f.files)
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc, Sheet: idcards.SheetA4, Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := svc.Compose(context.Background(), idcards.ComposeInput{SessionID: sess.ID, Side: idcards.SideFront})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Status != "composed" {
		t.Fatalf("status = %s", composed.Status)
	}
	if len(composed.Outputs) != 1 {
		t.Fatalf("outputs = %d", len(composed.Outputs))
	}
	path := composed.Outputs[0].StoragePath
	full := filepath.Join(f.dataDir, path)
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("composed output is empty")
	}
}

func TestComposeSideBySideUsesBothDocuments(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	backDoc := f.writeCardImage(t, "back",
		image.Point{X: 100, Y: 80}, image.Point{X: 300, Y: 85},
		image.Point{X: 305, Y: 220}, image.Point{X: 95, Y: 215})
	svc := idcards.New(f.db.DB(), f.files)
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc, BackDocID: backDoc,
		Sheet: idcards.SheetA4, Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutSideBySide, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := svc.Compose(context.Background(), idcards.ComposeInput{SessionID: sess.ID, Side: idcards.SideFront})
	if err != nil {
		t.Fatal(err)
	}
	if len(composed.Outputs) != 1 {
		t.Fatalf("outputs = %d", len(composed.Outputs))
	}
}

func TestComposeWithoutCornersFails(t *testing.T) {
	f := newFixture(t)
	// Use a blank image so detection produces no corners.
	frontDoc := f.writeBlankImage(t, "blank")
	svc := idcards.New(f.db.DB(), f.files)
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc, Sheet: idcards.SheetA4, Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Compose(context.Background(), idcards.ComposeInput{SessionID: sess.ID}); err == nil {
		t.Fatal("expected compose to fail without corners")
	}
}

func (f *fixture) writeBlankImage(t *testing.T, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	bg := color.RGBA{R: 128, G: 128, B: 128, A: 255}
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, bg)
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
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	relPath := "documents/TESTORDER/" + name + ".png"
	_, err := f.db.DB().Exec(`
INSERT INTO documents (id, order_id, original_filename, mime_type, size_bytes, page_count, sha256, storage_path, created_at, retention_until)
VALUES (?, 'TESTORDER', ?, 'image/png', ?, 1, 'deadbeef', ?, 1700000000, 1700604800)`,
		name, name+".png", buf.Len(), relPath)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRemoveSoftDeletesSession(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	svc := idcards.New(f.db.DB(), f.files)
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc, Sheet: idcards.SheetA4, Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutFrontOnly, ActualSize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(context.Background(), sess.ID); err == nil {
		t.Fatal("expected Get to fail after Remove")
	}
}

func TestCalibrationCRUD(t *testing.T) {
	f := newFixture(t)
	svc := idcards.New(f.db.DB(), f.files)
	created, err := svc.CreateCalibration(context.Background(), idcards.Calibration{
		Name: "HP M404 duplex",
		Sheet: idcards.SheetA4,
		FlipEdge: idcards.FlipLongEdge,
		DXmm: 1.5,
		DYmm: -0.5,
		Notes: "Test alignment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("calibration id empty")
	}
	list, err := svc.ListCalibrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
	if err := svc.DeleteCalibration(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListCalibrations(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCalibrationRejectsOutOfRangeOffsets(t *testing.T) {
	f := newFixture(t)
	svc := idcards.New(f.db.DB(), f.files)
	if _, err := svc.CreateCalibration(context.Background(), idcards.Calibration{
		Name: "Bad", Sheet: idcards.SheetA4, FlipEdge: idcards.FlipLongEdge,
		DXmm: 100, DYmm: 0,
	}); err == nil {
		t.Fatal("expected error for too-large offset")
	}
}

func TestComposeAppliesCalibrationOffsets(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	backDoc := f.writeCardImage(t, "back",
		image.Point{X: 100, Y: 80}, image.Point{X: 300, Y: 85},
		image.Point{X: 305, Y: 220}, image.Point{X: 95, Y: 215})
	svc := idcards.New(f.db.DB(), f.files)
	cal, err := svc.CreateCalibration(context.Background(), idcards.Calibration{
		Name: "Test", Sheet: idcards.SheetA4, FlipEdge: idcards.FlipLongEdge,
		DXmm: 2.0, DYmm: -1.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
		FrontDocID: frontDoc, BackDocID: backDoc,
		Sheet: idcards.SheetA4, Card: idcards.CardCR80,
		LayoutKind: idcards.LayoutSideBySide, ActualSize: true,
		CalibrationID: cal.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := svc.Compose(context.Background(), idcards.ComposeInput{SessionID: sess.ID, Side: idcards.SideFront})
	if err != nil {
		t.Fatal(err)
	}
	if len(composed.Outputs) != 1 {
		t.Fatalf("outputs = %d", len(composed.Outputs))
	}
}

func TestListReturnsCreatedSessions(t *testing.T) {
	f := newFixture(t)
	frontDoc := f.writeCardImage(t, "front",
		image.Point{X: 80, Y: 60}, image.Point{X: 320, Y: 70},
		image.Point{X: 325, Y: 240}, image.Point{X: 75, Y: 235})
	svc := idcards.New(f.db.DB(), f.files)
	for i := 0; i < 3; i++ {
		_, err := svc.CreateSession(context.Background(), idcards.CreateSessionInput{
			FrontDocID: frontDoc, Sheet: idcards.SheetA4, Card: idcards.CardCR80,
			LayoutKind: idcards.LayoutFrontOnly, ActualSize: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("list = %d", len(list))
	}
}
