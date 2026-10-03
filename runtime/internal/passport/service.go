package passport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

// CreateSessionInput is the payload accepted by Service.CreateSession. The
// document id references the existing documents table; the studio never
// stores image bytes directly.
type CreateSessionInput struct {
	OrderID             string
	DocumentID          string
	Preset              DocumentPreset
	WidthMm             float64
	HeightMm            float64
	Background          BackgroundKind
	Sheet               SheetPreset
	PhotosPerSheet      int
	DPI                 int
	ComplianceNote      string
}

// Service is the persistence + composition surface for the Passport Photo
// Studio. It owns the SQLite handle, the local files abstraction, the face
// detector and a clock for timestamps. Tests inject their own detector and
// clock via WithDetector / WithClock.
type Service struct {
	db       *sql.DB
	files    *localfiles.Files
	detector FaceDetector
	now      func() time.Time
}

// New constructs a Service. The db is the SQLite handle from store.Open; the
// files is the protected local data root so composed PNGs live inside the
// install's data tree; the supplied detector is invoked when a new session
// is created. Pass GeometricDetector{} in tests and in the phase 6 stub.
func New(database *sql.DB, files *localfiles.Files, detector FaceDetector) *Service {
	return &Service{
		db:       database,
		files:    files,
		detector: detector,
		now:      time.Now,
	}
}

// WithClock replaces the timestamp source. Tests use a fixed clock so the
// created_at / updated_at columns are stable across runs.
func (s *Service) WithClock(now func() time.Time) *Service {
	clone := *s
	clone.now = now
	return &clone
}

// WithDetector replaces the face detector. Tests use the stub detector by
// default; production code wires the licence-reviewed ONNX adapter when it
// becomes available.
func (s *Service) WithDetector(d FaceDetector) *Service {
	clone := *s
	clone.detector = d
	return &clone
}

// CreateSession writes a new session row, runs the face detector on the
// supplied document, and returns the populated session.
//
// Detection failures are not fatal: the session is still created with a
// nil face region and a status of "pending" so the operator is forced to
// lay down a manual region via the UI. This matches the master spec rule
// "never crop automatically when confidence is below the configured
// threshold".
func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (Session, error) {
	if in.DocumentID == "" {
		return Session{}, fmt.Errorf("%w: document id is required", ErrInvalid)
	}
	if in.Preset == PresetCustom {
		if in.WidthMm <= 0 || in.HeightMm <= 0 {
			return Session{}, fmt.Errorf("%w: custom preset needs width_mm and height_mm > 0", ErrInvalid)
		}
	} else {
		spec, err := PresetFor(in.Preset)
		if err != nil {
			return Session{}, err
		}
		in.WidthMm = spec.WidthMm
		in.HeightMm = spec.HeightMm
	}
	spec := PresetSpec{
		Preset:             in.Preset,
		WidthMm:            in.WidthMm,
		HeightMm:           in.HeightMm,
		HeadHeightMm:       0.69 * in.HeightMm,
		EyeLineFromBottomMm: 0.6 * in.HeightMm,
	}
	if in.Preset != PresetCustom {
		canonical, err := PresetFor(in.Preset)
		if err == nil {
			spec.HeadHeightMm = canonical.HeadHeightMm
			spec.EyeLineFromBottomMm = canonical.EyeLineFromBottomMm
		}
	}
	if in.Background == "" {
		in.Background = BackgroundReplaceWhite
	}
	if _, err := BackgroundColor(in.Background); err != nil {
		return Session{}, err
	}
	if in.DPI <= 0 {
		in.DPI = 300
	}
	if in.Sheet == "" {
		in.Sheet = SheetA4
	}
	if _, _, err := SheetDimensions(in.Sheet); err != nil {
		return Session{}, err
	}
	if in.PhotosPerSheet < 0 {
		in.PhotosPerSheet = 0
	}
	docBody, err := s.readDocument(ctx, in.DocumentID)
	if err != nil {
		return Session{}, fmt.Errorf("%w: source document: %v", ErrInvalid, err)
	}
	img, err := Decode(docBody)
	if err != nil {
		return Session{}, fmt.Errorf("%w: decode source: %v", ErrInvalid, err)
	}
	sess := Session{
		ID:             s.NewID(),
		OrderID:        in.OrderID,
		DocumentID:     in.DocumentID,
		Preset:         in.Preset,
		WidthMm:        spec.WidthMm,
		HeightMm:       spec.HeightMm,
		Background:     in.Background,
		HeadHeightMm:   spec.HeadHeightMm,
		EyeLineFromBottomMm: spec.EyeLineFromBottomMm,
		ComplianceNote: in.ComplianceNote,
		Status:         "pending",
		CreatedAt:      s.now().Unix(),
		UpdatedAt:      s.now().Unix(),
	}
	var facePtr *FaceRegion
	if s.detector != nil {
		region, derr := s.detector.DetectFace(ctx, img, spec)
		if derr == nil && !region.Bounds().Empty() {
			facePtr = &region
			sess.Status = "auto_detected"
		}
	}
	sess.FaceRegion = facePtr
	if err := s.insertSession(ctx, sess, in.Sheet, in.PhotosPerSheet, in.DPI); err != nil {
		return Session{}, err
	}
	if err := s.upsertFaceRegion(ctx, sess.ID, facePtr); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, sess.ID)
}

// SetFaceRegion stores the operator-confirmed face region. The function
// marks the region as manual and sets confidence to 1.0 because the
// operator has accepted the region as authoritative. A manual override moves
// the session into the "manual_confirmed" status so the UI can render the
// compose button without re-asking for confirmation.
func (s *Service) SetFaceRegion(ctx context.Context, id string, region FaceRegion) (Session, error) {
	if region.Width <= 0 || region.Height <= 0 {
		return Session{}, ErrBadFaceRegion
	}
	sess, err := s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	region.Manual = true
	region.Confidence = 1.0
	if err := s.upsertFaceRegion(ctx, id, &region); err != nil {
		return Session{}, err
	}
	if err := s.updateStatus(ctx, id, "manual_confirmed"); err != nil {
		return Session{}, err
	}
	sess.FaceRegion = &region
	sess.Status = "manual_confirmed"
	sess.UpdatedAt = s.now().Unix()
	return sess, nil
}

// UpdateLayout changes the preset, dimensions, background, sheet layout
// or compliance note without losing the operator's manual region.
func (s *Service) UpdateLayout(ctx context.Context, id string, in CreateSessionInput) (Session, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if in.Preset == "" {
		in.Preset = sess.Preset
	}
	if in.Preset != PresetCustom && in.Preset != sess.Preset {
		spec, err := PresetFor(in.Preset)
		if err != nil {
			return Session{}, err
		}
		in.WidthMm = spec.WidthMm
		in.HeightMm = spec.HeightMm
		in.ComplianceNote = "" // reset; merchant re-supplies per preset
	}
	if in.Preset == PresetCustom {
		if in.WidthMm <= 0 || in.HeightMm <= 0 {
			return Session{}, fmt.Errorf("%w: custom preset needs width_mm and height_mm > 0", ErrInvalid)
		}
	}
	if in.Background != "" {
		if _, err := BackgroundColor(in.Background); err != nil {
			return Session{}, err
		}
	}
	if in.Sheet == "" {
		in.Sheet = SheetA4
	}
	if _, _, err := SheetDimensions(in.Sheet); err != nil {
		return Session{}, err
	}
	if in.DPI <= 0 {
		in.DPI = 300
	}
	if err := s.updateLayoutRow(ctx, id, in); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, id)
}

// Compose renders the passport photo sheet and persists an output row. The
// function is safe to call repeatedly; each invocation writes a new PNG and
// leaves prior outputs in place so the merchant can compare layouts.
func (s *Service) Compose(ctx context.Context, id string) (Session, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if sess.FaceRegion == nil {
		return Session{}, ErrBadFaceRegion
	}
	docBody, err := s.readDocument(ctx, sess.DocumentID)
	if err != nil {
		return Session{}, fmt.Errorf("%w: source document: %v", ErrInvalid, err)
	}
	img, err := Decode(docBody)
	if err != nil {
		return Session{}, fmt.Errorf("%w: decode source: %v", ErrInvalid, err)
	}
	layoutRow, err := s.loadLayoutRow(ctx, id)
	if err != nil {
		return Session{}, err
	}
	spec, err := PresetFor(sess.Preset)
	if err != nil {
		return Session{}, err
	}
	if sess.Preset == PresetCustom {
		spec.WidthMm = sess.WidthMm
		spec.HeightMm = sess.HeightMm
	}
	composed, err := ComposePhoto(img, *sess.FaceRegion, spec, sess.Background, layoutRow.DPI)
	if err != nil {
		return Session{}, fmt.Errorf("%w: render photo: %v", ErrCompose, err)
	}
	layout, err := PlanSheet(layoutRow.Sheet, sess.WidthMm, sess.HeightMm, layoutRow.DPI)
	if err != nil {
		return Session{}, fmt.Errorf("%w: plan sheet: %v", ErrCompose, err)
	}
	if layoutRow.PhotosPerSheet > 0 && layoutRow.PhotosPerSheet < layout.PhotoCount {
		layout.PhotoCount = layoutRow.PhotosPerSheet
	}
	out, err := ComposeSheet(ctx, composed, layout, s.files, s.now, sess.ID)
	if err != nil {
		return Session{}, err
	}
	if err := s.insertOutput(ctx, out); err != nil {
		return Session{}, err
	}
	if err := s.updateStatus(ctx, id, "composed"); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, id)
}

// Remove soft-deletes the session. The underlying document blob is retained
// per the document retention policy; the rendered PNG stays on disk for the
// audit trail.
func (s *Service) Remove(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE passport_sessions SET removed_at=?, updated_at=? WHERE id=? AND removed_at IS NULL`, s.now().Unix(), s.now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns a session including its face region and outputs. Soft-deleted
// sessions are filtered out so the studio cannot resurrect a session the
// merchant has removed.
func (s *Service) Get(ctx context.Context, id string) (Session, error) {
	var sess Session
	var presetStr, bgStr string
	var widthMm, heightMm, headMm, eyeMm sql.NullFloat64
	var faceX, faceY, faceW, faceH, faceConf sql.NullFloat64
	var faceManual sql.NullBool
	var removedAt sql.NullInt64
	var orderID sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, order_id, document_id, preset, width_mm, height_mm, background,
	head_height_mm, eye_line_from_bottom_mm, status, error, created_at, updated_at, removed_at
FROM passport_sessions WHERE id=? AND removed_at IS NULL`, id).Scan(
		&sess.ID, &orderID, &sess.DocumentID, &presetStr, &widthMm, &heightMm, &bgStr,
		&headMm, &eyeMm, &sess.Status, &sess.Error, &sess.CreatedAt, &sess.UpdatedAt, &removedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	sess.OrderID = orderID.String
	sess.Preset = DocumentPreset(presetStr)
	sess.Background = BackgroundKind(bgStr)
	sess.WidthMm = widthMm.Float64
	sess.HeightMm = heightMm.Float64
	sess.HeadHeightMm = headMm.Float64
	sess.EyeLineFromBottomMm = eyeMm.Float64
	sess.RemovedAt = removedAt.Int64
	if err := s.db.QueryRowContext(ctx, `
SELECT x, y, width, height, confidence, manual
FROM passport_face_regions WHERE session_id=?`, id).Scan(
		&faceX, &faceY, &faceW, &faceH, &faceConf, &faceManual,
	); err == nil {
		sess.FaceRegion = &FaceRegion{
			X:          int(faceX.Float64),
			Y:          int(faceY.Float64),
			Width:      int(faceW.Float64),
			Height:     int(faceH.Float64),
			Confidence: faceConf.Float64,
			Manual:     faceManual.Bool,
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	outputs, err := s.loadOutputs(ctx, id)
	if err != nil {
		return Session{}, err
	}
	sess.Outputs = outputs
	return sess, nil
}

// List returns every non-removed session, newest first. The implementation
// reads the IDs into memory and closes the rows handle before iterating so
// it does not deadlock the single-connection SQLite pool when called
// concurrently with other queries.
func (s *Service) List(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM passport_sessions WHERE removed_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]Session, 0, len(ids))
	for _, id := range ids {
		sess, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, nil
}

// OutputBlobPath resolves the supplied storage path to its absolute location
// inside the protected data root. Returns ErrInvalid when the supplied path
// tries to traverse outside the data root.
func (s *Service) OutputBlobPath(rel string) (string, error) {
	if rel == "" || strings.Contains(rel, "..") {
		return "", ErrInvalid
	}
	full := filepath.Join(s.files.DataRoot(), rel)
	if _, err := os.Stat(full); err != nil {
		return "", err
	}
	return full, nil
}

// NewID returns a fresh identifier for the supplied session. The HTTP
// surface uses this when it has to mint ids outside the persistence layer
// (e.g. pre-creating a session before knowing the source document).
func (s *Service) NewID() string { return s.newID() }

// newID returns a stable hex identifier based on the supplied clock. Tests
// inject a fixed clock so the generated ids are deterministic; production
// code uses time.Now.
func (s *Service) newID() string { return newID(s.now) }

// readDocument fetches the source blob from the documents table.
func (s *Service) readDocument(ctx context.Context, id string) ([]byte, error) {
	if id == "" {
		return nil, ErrNoDocument
	}
	var rel string
	if err := s.db.QueryRowContext(ctx, `SELECT storage_path FROM documents WHERE id=?`, id).Scan(&rel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoDocument
		}
		return nil, err
	}
	if rel == "" {
		return nil, ErrNoDocument
	}
	full := filepath.Join(s.files.DataRoot(), rel)
	body, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Service) insertSession(ctx context.Context, sess Session, sheet SheetPreset, photosPerSheet, dpi int) error {
	width := nullFloat(sess.WidthMm)
	height := nullFloat(sess.HeightMm)
	head := nullFloat(sess.HeadHeightMm)
	eye := nullFloat(sess.EyeLineFromBottomMm)
	bg := string(sess.Background)
	if bg == "" {
		bg = string(BackgroundReplaceWhite)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO passport_sessions
	(id, order_id, document_id, preset, width_mm, height_mm, background,
	 head_height_mm, eye_line_from_bottom_mm, sheet, photos_per_sheet, dpi,
	 compliance_note, status, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		sess.ID, nullable(sess.OrderID), sess.DocumentID, string(sess.Preset), width, height, bg,
		head, eye, string(sheet), photosPerSheet, dpi,
		sess.ComplianceNote, sess.Status, sess.CreatedAt, sess.UpdatedAt,
	)
	return err
}

func (s *Service) updateLayoutRow(ctx context.Context, id string, in CreateSessionInput) error {
	width := nullFloat(in.WidthMm)
	height := nullFloat(in.HeightMm)
	_, err := s.db.ExecContext(ctx, `
UPDATE passport_sessions
   SET preset=?, width_mm=?, height_mm=?, background=?,
       sheet=?, photos_per_sheet=?, dpi=?, compliance_note=?, updated_at=?
 WHERE id=? AND removed_at IS NULL`,
		string(in.Preset), width, height, string(in.Background),
		string(in.Sheet), in.PhotosPerSheet, in.DPI, in.ComplianceNote, s.now().Unix(), id,
	)
	return err
}

func (s *Service) upsertFaceRegion(ctx context.Context, sessionID string, region *FaceRegion) error {
	if region == nil {
		_, err := s.db.ExecContext(ctx, `DELETE FROM passport_face_regions WHERE session_id=?`, sessionID)
		return err
	}
	manual := 0
	if region.Manual {
		manual = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO passport_face_regions (session_id, x, y, width, height, confidence, manual)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
	x=excluded.x, y=excluded.y, width=excluded.width, height=excluded.height,
	confidence=excluded.confidence, manual=excluded.manual`,
		sessionID, region.X, region.Y, region.Width, region.Height, region.Confidence, manual,
	)
	return err
}

func (s *Service) updateStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE passport_sessions SET status=?, updated_at=? WHERE id=? AND removed_at IS NULL`, status, s.now().Unix(), id)
	return err
}

func (s *Service) loadLayoutRow(ctx context.Context, id string) (layoutRow, error) {
	var row layoutRow
	var presetStr, sheetStr, bgStr string
	err := s.db.QueryRowContext(ctx, `
SELECT preset, width_mm, height_mm, background, sheet, photos_per_sheet, dpi, head_height_mm, eye_line_from_bottom_mm
FROM passport_sessions WHERE id=?`, id).Scan(
		&presetStr, &row.PhotoWMm, &row.PhotoHMm, &bgStr, &sheetStr, &row.PhotosPerSheet, &row.DPI,
		&row.HeadHeightMm, &row.EyeLineFromBottomMm,
	)
	if err != nil {
		return row, err
	}
	row.Preset = DocumentPreset(presetStr)
	row.Sheet = SheetPreset(sheetStr)
	row.Background = BackgroundKind(bgStr)
	return row, nil
}

func (s *Service) loadOutputs(ctx context.Context, sessionID string) ([]Output, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, side, sheet_width_mm, sheet_height_mm, pixel_width, pixel_height, storage_path, sha256, photo_count, created_at
FROM passport_outputs WHERE session_id=? ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Output{}
	for rows.Next() {
		var o Output
		var side string
		if err := rows.Scan(&o.ID, &o.SessionID, &side, &o.SheetWidthMm, &o.SheetHeightMm, &o.PixelWidth, &o.PixelHeight, &o.StoragePath, &o.SHA256, &o.PhotoCount, &o.CreatedAt); err != nil {
			return nil, err
		}
		o.Side = Side(side)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Service) insertOutput(ctx context.Context, o Output) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO passport_outputs
	(id, session_id, side, sheet_width_mm, sheet_height_mm, pixel_width, pixel_height, storage_path, sha256, photo_count, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.SessionID, string(o.Side), o.SheetWidthMm, o.SheetHeightMm, o.PixelWidth, o.PixelHeight, o.StoragePath, o.SHA256, o.PhotoCount, o.CreatedAt,
	)
	return err
}

// layoutRow is the persisted layout configuration for a session.
type layoutRow struct {
	Preset              DocumentPreset
	Sheet               SheetPreset
	Background          BackgroundKind
	PhotoWMm            float64
	PhotoHMm            float64
	HeadHeightMm        float64
	EyeLineFromBottomMm float64
	PhotosPerSheet      int
	DPI                 int
}

// randomHex returns a 16-character hex identifier based on crypto/rand. The
// service uses this for any id that needs to be unique across multiple
// processes (e.g. backups); ordinary sessions use the time-derived id so
// the rendered filenames stay deterministic per session.
func randomHex(n int) string {
	if n <= 0 {
		n = 8
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(buf)
}

// filepathRel is a tiny helper to ensure exported paths are relative.
func filepathRel(p string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(".", p); err == nil {
			return rel
		}
	}
	return p
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(v float64) sql.NullFloat64 {
	if v == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: v, Valid: true}
}