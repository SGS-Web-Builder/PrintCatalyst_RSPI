package localserver_test

import (
	"bytes"
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

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
)

// ---- Phase 6: Passport Photo Studio HTTP ----

func postJSON(t *testing.T, baseURL, path string, cookie *http.Cookie, csrf string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+path, bytes.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", baseURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getJSON(t *testing.T, baseURL, path string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", baseURL+path, nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", baseURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func putJSON(t *testing.T, baseURL, path string, cookie *http.Cookie, csrf string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("PUT", baseURL+path, bytes.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", baseURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPassportsListRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/passports/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated sessions = %d, want 401/403", resp.StatusCode)
	}
}

func TestPassportsPresetsArePublicForAuthenticatedOwner(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/passports/presets", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(bs))
	}
	var parsed struct {
		Presets []map[string]any `json:"presets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Presets) < 6 {
		t.Fatalf("presets = %d, want >= 6 country presets", len(parsed.Presets))
	}
}

func TestPassportsCreateListGetDelete(t *testing.T) {
	ts, database, dataRoot := portalFixtureWithData(t)
	docID := seedPassportDocument(t, database.DB(), dataRoot, "portrait.png")
	cookie, csrf := signedInAsOwner(t, ts.URL)

	createBody, _ := json.Marshal(map[string]any{
		"documentId":  docID,
		"preset":      string(passport.PresetIndiaPassport),
		"background":  string(passport.BackgroundReplaceWhite),
		"sheet":       string(passport.SheetA4),
		"dpi":         300,
	})
	resp := postJSON(t, ts.URL, "/api/v1/owner/passports/sessions", cookie, csrf, createBody)
	if resp.StatusCode != 201 {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created passportSessionWire
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("session id empty")
	}
	if created.Status != "auto_detected" {
		t.Errorf("status = %q, want auto_detected", created.Status)
	}
	if created.FaceRegion == nil {
		t.Fatal("face region not auto-detected")
	}

	// List returns the session we just created.
	listResp := getJSON(t, ts.URL, "/api/v1/owner/passports/sessions", cookie, csrf)
	var list struct {
		Sessions []passportSessionWire `json:"sessions"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	listResp.Body.Close()
	if len(list.Sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(list.Sessions))
	}

	// Get returns the same session by id.
	getResp := getJSON(t, ts.URL, "/api/v1/owner/passports/sessions/"+created.ID, cookie, csrf)
	var got passportSessionWire
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if got.ID != created.ID {
		t.Errorf("got.id = %q, want %q", got.ID, created.ID)
	}

	// Soft delete.
	delReq, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/owner/passports/sessions/"+created.ID, nil)
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

func TestPassportsSetFaceRegionAndCompose(t *testing.T) {
	ts, database, dataRoot := portalFixtureWithData(t)
	docID := seedPassportDocument(t, database.DB(), dataRoot, "portrait.png")
	cookie, csrf := signedInAsOwner(t, ts.URL)

	createBody, _ := json.Marshal(map[string]any{
		"documentId": docID,
		"preset":     string(passport.PresetIndiaPassport),
		"sheet":      string(passport.SheetA4),
		"dpi":        300,
	})
	createResp := postJSON(t, ts.URL, "/api/v1/owner/passports/sessions", cookie, csrf, createBody)
	var created passportSessionWire
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	createResp.Body.Close()

	// Operator-confirmed face region (numeric override form).
	regionBody, _ := json.Marshal(map[string]any{
		"x":          60,
		"y":          60,
		"width":      280,
		"height":     360,
		"confidence": 1.0,
		"manual":     true,
	})
	regionResp := putJSON(t, ts.URL, "/api/v1/owner/passports/sessions/"+created.ID+"/face-region", cookie, csrf, regionBody)
	if regionResp.StatusCode != 200 {
		bs, _ := io.ReadAll(regionResp.Body)
		regionResp.Body.Close()
		t.Fatalf("face-region status = %d, body = %s", regionResp.StatusCode, string(bs))
	}
	var regionUpdated passportSessionWire
	if err := json.NewDecoder(regionResp.Body).Decode(&regionUpdated); err != nil {
		t.Fatal(err)
	}
	regionResp.Body.Close()
	if regionUpdated.Status != "manual_confirmed" {
		t.Errorf("status = %q, want manual_confirmed", regionUpdated.Status)
	}

	// Compose renders a PNG sheet and records an output.
	composeBody := []byte(`{}`)
	composeResp := postJSON(t, ts.URL, "/api/v1/owner/passports/sessions/"+created.ID+"/compose", cookie, csrf, composeBody)
	if composeResp.StatusCode != 200 {
		bs, _ := io.ReadAll(composeResp.Body)
		composeResp.Body.Close()
		t.Fatalf("compose status = %d, body = %s", composeResp.StatusCode, string(bs))
	}
	var composed passportSessionWire
	if err := json.NewDecoder(composeResp.Body).Decode(&composed); err != nil {
		t.Fatal(err)
	}
	composeResp.Body.Close()
	if composed.Status != "composed" {
		t.Errorf("status = %q, want composed", composed.Status)
	}
	if len(composed.Outputs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(composed.Outputs))
	}
	out := composed.Outputs[0]
	if out.SHA256 == "" {
		t.Fatal("output SHA-256 empty")
	}
	if out.PhotoCount < 4 {
		t.Errorf("photo count = %d, want >= 4 (A4 fits multiple India photos)", out.PhotoCount)
	}

	// Fetch the rendered PNG through the same-origin endpoint and verify
	// the magic bytes so we know the file lands on disk and the response is
	// actually a PNG.
	imgReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/passports/sessions/"+created.ID+"/outputs/"+out.ID+"/image", nil)
	imgReq.AddCookie(cookie)
	imgReq.Header.Set("X-CSRF-Token", csrf)
	imgReq.Header.Set("Origin", ts.URL)
	imgResp, err := http.DefaultClient.Do(imgReq)
	if err != nil {
		t.Fatal(err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != 200 {
		bs, _ := io.ReadAll(imgResp.Body)
		t.Fatalf("image status = %d, body = %s", imgResp.StatusCode, string(bs))
	}
	if got := imgResp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	body, err := io.ReadAll(imgResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("image body empty")
	}
	if !bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("not a PNG: prefix %x", body[:min(8, len(body))])
	}
}

func TestPassportsRejectsUnknownPreset(t *testing.T) {
	ts, database, dataRoot := portalFixtureWithData(t)
	docID := seedPassportDocument(t, database.DB(), dataRoot, "portrait.png")
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body, _ := json.Marshal(map[string]any{
		"documentId": docID,
		"preset":     "atlantis_passport",
	})
	resp := postJSON(t, ts.URL, "/api/v1/owner/passports/sessions", cookie, csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("unknown preset status = %d, want 400", resp.StatusCode)
	}
}

func TestPassportsDocumentsList(t *testing.T) {
	ts, database, dataRoot := portalFixtureWithData(t)
	_ = seedPassportDocument(t, database.DB(), dataRoot, "portrait.png")
	cookie, csrf := signedInAsOwner(t, ts.URL)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/passports/documents", nil)
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
	var parsed struct {
		Documents []map[string]any `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Documents) < 1 {
		t.Fatalf("documents = %d, want >= 1", len(parsed.Documents))
	}
}

// passportSessionWire mirrors the localserver passportSessionView shape so
// tests can decode the JSON response without importing the localserver
// package's internal types.
type passportSessionWire struct {
	ID                 string                  `json:"id"`
	OrderID            string                  `json:"orderId"`
	DocumentID         string                  `json:"documentId"`
	Preset             string                  `json:"preset"`
	PresetDisplayName  string                  `json:"presetDisplayName"`
	WidthMm            float64                 `json:"widthMm"`
	HeightMm           float64                 `json:"heightMm"`
	Background         string                  `json:"background"`
	HeadHeightMm       float64                 `json:"headHeightMm"`
	EyeLineFromBottomMm float64                 `json:"eyeLineFromBottomMm"`
	Status             string                  `json:"status"`
	Error              string                  `json:"error"`
	CreatedAt          int64                   `json:"createdAt"`
	UpdatedAt          int64                   `json:"updatedAt"`
	FaceRegion         *passport.FaceRegion    `json:"faceRegion,omitempty"`
	Outputs            []passport.Output      `json:"outputs"`
}

// seedPassportDocument writes a synthetic portrait image into the on-disk
// data root, inserts the matching documents row, and returns the new id.
// The image is a uniform 400x600 portrait; the geometric detector only
// needs the dimensions, not the content.
func seedPassportDocument(t *testing.T, db *sql.DB, dataRoot, name string) string {
	t.Helper()
	orderID := "PP-" + randomHex(t, 8)
	shareToken := randomHex(t, 12)
	if _, err := db.Exec(`INSERT INTO orders (id, share_token, status, currency, currency_minor_units, total_minor, customer_name, customer_phone, customer_email, customer_notes, created_at, updated_at, portal_gate) VALUES (?, ?, 'pending_payment', 'INR', 2, 0, '', '', '', '', 1700000000, 1700000000, 0)`, orderID, shareToken); err != nil {
		t.Fatal(err)
	}
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
	body := buf.Bytes()
	id := randomHex(t, 16)
	rel := filepath.Join("documents", id+".png")
	abs := filepath.Join(dataRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if _, err := db.Exec(`INSERT INTO documents (id, order_id, original_filename, mime_type, storage_path, page_count, size_bytes, sha256, created_at, retention_until) VALUES (?, ?, ?, 'image/png', ?, 1, ?, ?, 1700000000, 0)`, id, orderID, name, rel, len(body), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	return id
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}