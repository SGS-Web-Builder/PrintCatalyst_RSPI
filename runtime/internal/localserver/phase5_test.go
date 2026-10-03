package localserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// osWriteFile writes body to path, creating any missing parent directories.
// Centralised so tests don't import os and os/path/filepath separately.
func osWriteFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// randomHex returns n random bytes encoded as hex.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// idCardFixture is a self-contained test helper that bypasses the portal
// upload step. Tests that do not need the loopback HTTP upload round-trip
// can write a synthetic card PNG straight into the documents table and
// pass its id to the studio endpoints.
type idCardFixture struct {
	db       *store.Store
	files    *localfiles.Files
	dataDir  string
	frontDoc string
	backDoc  string
}

func newIDCardFixture(t *testing.T) *idCardFixture {
	t.Helper()
	dataDir := t.TempDir()
	databasePath := filepath.Join(t.TempDir(), "idcards.sqlite")
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
	f := &idCardFixture{db: database, files: files, dataDir: dataDir}
	f.frontDoc = f.writeCard(t, "front")
	f.backDoc = f.writeCard(t, "back")
	return f
}

// writeCard encodes a synthetic card-on-dark-background PNG, writes it to the
// documents store, and returns the persisted document id.
func (f *idCardFixture) writeCard(t *testing.T, label string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 25, B: 35, A: 255})
		}
	}
	pts := []image.Point{
		{X: 80, Y: 60}, {X: 320, Y: 70},
		{X: 325, Y: 240}, {X: 75, Y: 235},
	}
	for i := 0; i < 4; i++ {
		a := pts[i]
		b := pts[(i+1)%4]
		steps := abs(a.X-b.X) + abs(a.Y-b.Y)
		if steps < 1 {
			steps = 1
		}
		for s := 0; s <= steps; s++ {
			frac := float64(s) / float64(steps)
			x := int(float64(a.X) + float64(b.X-a.X)*frac)
			y := int(float64(a.Y) + float64(b.Y-a.Y)*frac)
			bounds := img.Bounds()
			if x >= bounds.Min.X && x < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				img.Set(x, y, color.RGBA{R: 240, G: 240, B: 245, A: 255})
			}
		}
	}
	for y := 70; y < 235; y++ {
		for x := 80; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: 230, G: 230, B: 235, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()

	id := randomHex(t, 16)
	rel := filepath.Join("documents", id+".png")
	abs := filepath.Join(f.dataDir, rel)
	if err := osWriteFile(abs, body); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if _, err := f.db.DB().Exec(`INSERT INTO documents (id, order_id, original_filename, mime_type, storage_path, page_count, size_bytes, sha256, created_at, retention_until) VALUES (?, 'TESTORDER', ?, 'image/png', ?, 1, ?, ?, 1700000000, 0)`,
		id, label+".png", rel, len(body), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	return id
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---- Phase 5: ID Card Studio HTTP ----

func TestIDCardsListRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/id-cards/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated sessions = %d, want 401/403", resp.StatusCode)
	}
}

func TestIDCardsCreateListGetDelete(t *testing.T) {
	ts, database, dataRoot := portalFixtureWithData(t)
	// Seed a single document row for the studio to consume. The end-to-end
	// create → get → delete cycle only needs one front document; the
	// side_by_side two-document variant is exercised by the compose test.
	frontID := seedIDCardDocument(t, database.DB(), dataRoot, "front.png")
	cookie, csrf := signedInAsOwner(t, ts.URL)

	createBody, _ := json.Marshal(map[string]any{
		"frontDocId": frontID,
		"sheet":      "A4",
		"card":       "cr80",
		"layoutKind": "front_only",
		"flipEdge":   "long_edge",
		"actualSize": true,
		"dpi":        150,
	})
	createReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/id-cards/sessions", bytes.NewReader(createBody))
	createReq.AddCookie(cookie)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-CSRF-Token", csrf)
	createReq.Header.Set("Origin", ts.URL)
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != 201 {
		t.Fatalf("create status = %d, body = %s", createResp.StatusCode, readAllBody(t, createResp))
	}
	var created idcardSessionPayload
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("session id missing")
	}
	if created.Status != "auto_detected" && created.Status != "pending" && created.Status != "manual_confirmed" {
		t.Fatalf("unexpected status %q", created.Status)
	}

	// List.
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/id-cards/sessions", nil)
	listReq.AddCookie(cookie)
	listReq.Header.Set("X-CSRF-Token", csrf)
	listReq.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != 200 {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var listed struct {
		Sessions []idcardSessionPayload `json:"sessions"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(listed.Sessions))
	}

	// Get.
	getReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/id-cards/sessions/"+created.ID, nil)
	getReq.AddCookie(cookie)
	getReq.Header.Set("X-CSRF-Token", csrf)
	getReq.Header.Set("Origin", ts.URL)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != 200 {
		t.Fatalf("get status = %d", getResp.StatusCode)
	}

	// Delete.
	delReq, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/owner/id-cards/sessions/"+created.ID, nil)
	delReq.AddCookie(cookie)
	delReq.Header.Set("X-CSRF-Token", csrf)
	delReq.Header.Set("Origin", ts.URL)
	delReq.Header.Set("Content-Type", "application/json")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != 204 {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}
}

func TestIDCardsSetCornersAndCompose(t *testing.T) {
	ts, database, dataRoot := portalFixtureWithData(t)
	frontID := seedIDCardDocument(t, database.DB(), dataRoot, "front.png")
	cookie, csrf := signedInAsOwner(t, ts.URL)

	// Create the session with a synthetic document.
	createBody, _ := json.Marshal(map[string]any{
		"frontDocId": frontID,
		"sheet":      "A4",
		"card":       "cr80",
		"layoutKind": "front_only",
		"actualSize": true,
		"dpi":        150,
	})
	createReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/id-cards/sessions", bytes.NewReader(createBody))
	createReq.AddCookie(cookie)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-CSRF-Token", csrf)
	createReq.Header.Set("Origin", ts.URL)
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != 201 {
		t.Fatalf("create status = %d, body = %s", createResp.StatusCode, readAllBody(t, createResp))
	}
	var created idcardSessionPayload
	json.NewDecoder(createResp.Body).Decode(&created)

	// Set manual corners.
	cornersBody, _ := json.Marshal(map[string]any{
		"side": "front",
		"corners": map[string]any{
			"tl":         map[string]float64{"x": 80, "y": 60},
			"tr":         map[string]float64{"x": 320, "y": 70},
			"br":         map[string]float64{"x": 325, "y": 240},
			"bl":         map[string]float64{"x": 75, "y": 235},
			"confidence": 1.0,
			"manual":     true,
		},
	})
	cReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/id-cards/sessions/"+created.ID+"/corners", bytes.NewReader(cornersBody))
	cReq.AddCookie(cookie)
	cReq.Header.Set("Content-Type", "application/json")
	cReq.Header.Set("X-CSRF-Token", csrf)
	cReq.Header.Set("Origin", ts.URL)
	cResp, err := http.DefaultClient.Do(cReq)
	if err != nil {
		t.Fatal(err)
	}
	defer cResp.Body.Close()
	if cResp.StatusCode != 200 {
		t.Fatalf("corners status = %d, body = %s", cResp.StatusCode, readAllBody(t, cResp))
	}
	var updated idcardSessionPayload
	json.NewDecoder(cResp.Body).Decode(&updated)
	if updated.FrontCorners == nil || !updated.FrontCorners.Manual {
		t.Fatal("manual flag not preserved on corners update")
	}
	if updated.Status != "manual_confirmed" {
		t.Fatalf("status after manual = %q", updated.Status)
	}

	// Compose.
	composeBody, _ := json.Marshal(map[string]any{"side": "front"})
	cpReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/id-cards/sessions/"+created.ID+"/compose", bytes.NewReader(composeBody))
	cpReq.AddCookie(cookie)
	cpReq.Header.Set("Content-Type", "application/json")
	cpReq.Header.Set("X-CSRF-Token", csrf)
	cpReq.Header.Set("Origin", ts.URL)
	cpResp, err := http.DefaultClient.Do(cpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer cpResp.Body.Close()
	if cpResp.StatusCode != 200 {
		t.Fatalf("compose status = %d, body = %s", cpResp.StatusCode, readAllBody(t, cpResp))
	}
	var composed idcardSessionPayload
	json.NewDecoder(cpResp.Body).Decode(&composed)
	if composed.Status != "composed" {
		t.Fatalf("composed status = %q", composed.Status)
	}
	if len(composed.Outputs) != 1 {
		t.Fatalf("outputs = %d", len(composed.Outputs))
	}

	// Fetch the image bytes.
	imgReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/id-cards/sessions/"+created.ID+"/outputs/front/image", nil)
	imgReq.AddCookie(cookie)
	imgReq.Header.Set("X-CSRF-Token", csrf)
	imgReq.Header.Set("Origin", ts.URL)
	imgResp, err := http.DefaultClient.Do(imgReq)
	if err != nil {
		t.Fatal(err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != 200 {
		t.Fatalf("image status = %d", imgResp.StatusCode)
	}
	body, _ := io.ReadAll(imgResp.Body)
	if len(body) < 8 {
		t.Fatal("image body too small")
	}
	// PNG magic.
	if !(body[0] == 0x89 && body[1] == 'P' && body[2] == 'N' && body[3] == 'G') {
		t.Fatalf("not a PNG: %x", body[:8])
	}
}

func TestIDCardsCalibrationsCRUD(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	createBody, _ := json.Marshal(map[string]any{
		"name":     "Brother HL-Laser 1",
		"sheet":    "A4",
		"flipEdge": "long_edge",
		"dxMm":     1.5,
		"dyMm":     -0.8,
		"notes":    "left edge shift",
	})
	cReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/id-cards/calibrations", bytes.NewReader(createBody))
	cReq.AddCookie(cookie)
	cReq.Header.Set("Content-Type", "application/json")
	cReq.Header.Set("X-CSRF-Token", csrf)
	cReq.Header.Set("Origin", ts.URL)
	cResp, err := http.DefaultClient.Do(cReq)
	if err != nil {
		t.Fatal(err)
	}
	defer cResp.Body.Close()
	if cResp.StatusCode != 201 {
		body, _ := io.ReadAll(cResp.Body)
		t.Fatalf("calibration create status = %d, body = %s", cResp.StatusCode, string(body))
	}
	var created idcards.Calibration
	if err := json.NewDecoder(cResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("calibration id missing")
	}

	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/id-cards/calibrations", nil)
	listReq.AddCookie(cookie)
	listReq.Header.Set("X-CSRF-Token", csrf)
	listReq.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var listed struct {
		Calibrations []idcards.Calibration `json:"calibrations"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Calibrations) != 1 {
		t.Fatalf("calibrations = %d", len(listed.Calibrations))
	}

	delReq, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/owner/id-cards/calibrations/"+created.ID, nil)
	delReq.AddCookie(cookie)
	delReq.Header.Set("X-CSRF-Token", csrf)
	delReq.Header.Set("Origin", ts.URL)
	delReq.Header.Set("Content-Type", "application/json")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != 204 {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}
}

func TestIDCardsDocumentsList(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/id-cards/documents", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var listed struct {
		Documents []struct {
			ID string `json:"id"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	// No documents in this fixture; empty array is fine.
	if listed.Documents == nil {
		t.Fatal("documents array missing")
	}
}

// idcardSessionPayload mirrors the JSON shape the dashboard consumes so the
// test can assert against the wire format directly. The fields are flat and
// stable across releases; nested corners are decoded into the same shape
// the studio exposes to the UI.
type idcardSessionPayload struct {
	ID            string `json:"id"`
	OrderID       string `json:"orderId"`
	FrontDocID    string `json:"frontDocId"`
	BackDocID     string `json:"backDocId"`
	Sheet         string `json:"sheet"`
	Card          string `json:"card"`
	CardWidthMm   float64 `json:"cardWidthMm"`
	CardHeightMm  float64 `json:"cardHeightMm"`
	LayoutKind    string `json:"layoutKind"`
	FlipEdge      string `json:"flipEdge"`
	Rows          int    `json:"rows"`
	Cols          int    `json:"cols"`
	ActualSize    bool   `json:"actualSize"`
	DPI           int    `json:"dpi"`
	CalibrationID string `json:"calibrationId"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
	FrontCorners  *struct {
		TL         map[string]float64 `json:"tl"`
		TR         map[string]float64 `json:"tr"`
		BR         map[string]float64 `json:"br"`
		BL         map[string]float64 `json:"bl"`
		Confidence float64            `json:"confidence"`
		Manual     bool               `json:"manual"`
	} `json:"frontCorners,omitempty"`
	BackCorners *struct {
		TL         map[string]float64 `json:"tl"`
		TR         map[string]float64 `json:"tr"`
		BR         map[string]float64 `json:"br"`
		BL         map[string]float64 `json:"bl"`
		Confidence float64            `json:"confidence"`
		Manual     bool               `json:"manual"`
	} `json:"backCorners,omitempty"`
	Outputs []idcards.Output `json:"outputs"`
}

// insertCardDocument inserts a stub documents row so the studio endpoints
// have something to reference. The storage_path points at a sentinel file
// that the compose step never reads when the test only exercises the
// HTTP plumbing; tests that go further and ask the service to compose
// must use seedIDCardDocument instead so the file actually exists on
// disk at the server's data root.
//
// The portalFixture does not seed an order, so we create a minimal one
// here to satisfy the documents.order_id foreign key. Both the order id
// and the share_token are randomised so multiple inserts in the same test
// do not collide on the UNIQUE constraints.
func insertCardDocument(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	orderID := "TEST-" + randomHex(t, 8)
	shareToken := randomHex(t, 12)
	if _, err := db.Exec(`INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor, customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate) VALUES (?, ?, 'pending_payment', 'INR', 2, 0, '', '', '', '', 1700000000, 1700000000, 0)`, orderID, shareToken); err != nil {
		t.Fatal(err)
	}
	id := randomHex(t, 16)
	rel := filepath.Join("documents", id+".png")
	body := []byte("test-image-bytes")
	sum := sha256.Sum256(body)
	if _, err := db.Exec(`INSERT INTO documents (id, order_id, original_filename, mime_type, storage_path, page_count, size_bytes, sha256, created_at, retention_until) VALUES (?, ?, ?, 'image/png', ?, 1, ?, ?, 1700000000, 0)`,
		id, orderID, name, rel, len(body), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedIDCardDocument is the compose-capable version of insertCardDocument:
// it writes a small but real PNG file at the server's data root so the
// compose step can read the blob and decode it. Tests that exercise the
// full session create → corners → compose cycle must use this helper.
//
// The synthetic image is intentionally minimal (200×150) so the
// quadrilateral detector can finish quickly inside the test timeout.
// Detection runs O(n³) over the edge pixel set; production fixtures ship
// higher-resolution cards and the studio is not on the hot path of a
// test run.
func seedIDCardDocument(t *testing.T, db *sql.DB, dataRoot, name string) string {
	t.Helper()
	orderID := "TEST-" + randomHex(t, 8)
	shareToken := randomHex(t, 12)
	if _, err := db.Exec(`INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor, customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate) VALUES (?, ?, 'pending_payment', 'INR', 2, 0, '', '', '', '', 1700000000, 1700000000, 0)`, orderID, shareToken); err != nil {
		t.Fatal(err)
	}
	const W, H = 200, 150
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	// Solid dark background (avoids producing strong edge pixels everywhere).
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			img.Set(x, y, color.RGBA{R: 25, G: 25, B: 30, A: 255})
		}
	}
	// Card body — a single solid rectangle. The detector picks it up from
	// the strong horizontal/vertical edges around it.
	for y := 30; y < 120; y++ {
		for x := 40; x < 160; x++ {
			img.Set(x, y, color.RGBA{R: 230, G: 230, B: 235, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	id := randomHex(t, 16)
	rel := filepath.Join("documents", id+".png")
	// localfiles.New resolves the data root through filepath.EvalSymlinks so
	// /tmp → /private/tmp on macOS. Resolve the same way before writing so
	// the server can read back what we wrote.
	resolvedRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(resolvedRoot, rel)
	if err := osWriteFile(abs, body); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if _, err := db.Exec(`INSERT INTO documents (id, order_id, original_filename, mime_type, storage_path, page_count, size_bytes, sha256, created_at, retention_until) VALUES (?, ?, ?, 'image/png', ?, 1, ?, ?, 1700000000, 0)`,
		id, orderID, name, rel, len(body), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	return id
}
