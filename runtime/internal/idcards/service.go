package idcards

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

// Side identifies which card a corner set or output belongs to. The
// canonical values are "front" and "back".
type Side string

const (
	SideFront Side = "front"
	SideBack  Side = "back"
)

// Session is the durable record of one ID Card Studio session. The original
// customer documents are referenced by ID; the perspective-correct corners,
// chosen layout, calibration and (after a compose call) the rendered sheet
// live on the related tables.
type Session struct {
	ID            string
	OrderID       string
	FrontDocID    string
	BackDocID     string
	Sheet         SheetPreset
	Card          CardPreset
	CardWidthMm   float64
	CardHeightMm  float64
	LayoutKind    LayoutKind
	FlipEdge      FlipEdge
	Rows          int
	Cols          int
	ActualSize    bool
	DPI           int
	CalibrationID string
	Status        string
	Error         string
	CreatedAt     int64
	UpdatedAt     int64
	RemovedAt     int64

	FrontCorners *Corners
	BackCorners  *Corners
	Outputs      []Output
}

// Corners is a four-point corner set for one document. Confidence is 0..1;
// Manual is true when the operator accepted or replaced the auto-detected
// corners via the UI.
type Corners struct {
	Side       Side    `json:"side"`
	TL         Point   `json:"tl"`
	TR         Point   `json:"tr"`
	BR         Point   `json:"br"`
	BL         Point   `json:"bl"`
	Confidence float64 `json:"confidence"`
	Manual     bool    `json:"manual"`
}

// Output is the durable record of a composed sheet. StoragePath is relative
// to the data root; SHA256 is the hex digest of the rendered PNG.
type Output struct {
	ID            string `json:"id"`
	SessionID     string `json:"sessionId"`
	Side          Side   `json:"side"`
	SheetWidthMm  float64 `json:"sheetWidthMm"`
	SheetHeightMm float64 `json:"sheetHeightMm"`
	PixelWidth    int    `json:"pixelWidth"`
	PixelHeight   int    `json:"pixelHeight"`
	StoragePath   string `json:"storagePath"`
	SHA256        string `json:"sha256"`
	CreatedAt     int64  `json:"createdAt"`
}

// Calibration is one printer calibration row. Sheet + flip-edge uniquely
// identifies the printer setup; dx_mm and dy_mm are the back-card offsets
// in millimetres.
type Calibration struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Sheet     SheetPreset `json:"sheet"`
	FlipEdge  FlipEdge `json:"flipEdge"`
	DXmm      float64 `json:"dxMm"`
	DYmm      float64 `json:"dyMm"`
	Notes     string  `json:"notes"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

// CreateSessionInput is the payload accepted by Service.CreateSession.
type CreateSessionInput struct {
	OrderID       string
	FrontDocID    string
	BackDocID     string
	Sheet         SheetPreset
	Card          CardPreset
	CardWidthMm   float64
	CardHeightMm  float64
	LayoutKind    LayoutKind
	FlipEdge      FlipEdge
	Rows          int
	Cols          int
	ActualSize    bool
	DPI           int
	CalibrationID string
}

// Sentinel errors mapped to HTTP status codes by the portal / owner handlers.
var (
	ErrInvalid      = errors.New("id-cards: invalid input")
	ErrNotFound     = errors.New("id-cards: not found")
	ErrBadCorners   = errors.New("id-cards: corners do not match the document")
	ErrCompose      = errors.New("id-cards: failed to compose output")
	ErrBadCalibration = errors.New("id-cards: calibration not found")
)

// Service is the persistence + composition surface for the ID Card Studio.
// It owns the SQLite handle, the local files abstraction and a clock for
// timestamps. Tests inject their own clock via WithClock.
type Service struct {
	db    *sql.DB
	files *localfiles.Files
	now   func() time.Time
}

// New constructs a Service. The db is the SQLite handle from store.Open; the
// files is the protected local data root so composed PNGs live inside the
// install's data tree.
func New(database *sql.DB, files *localfiles.Files) *Service {
	return &Service{db: database, files: files, now: time.Now}
}

// WithClock replaces the timestamp source. Tests use a fixed clock so the
// created_at columns are stable across runs.
func (s *Service) WithClock(now func() time.Time) *Service {
	clone := *s
	clone.now = now
	return &clone
}

// CreateSession writes a new session row, runs the quadrilateral detection
// on the front document (and back document when present), and returns the
// fully-populated session.
//
// Detection failures are not fatal: the session is still created, but with
// an empty corners row and a status of "pending" so the operator is forced
// to lay down manual corners via the UI. This matches the spec rule "never
// crop automatically when confidence is below the configured threshold".
func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (Session, error) {
	// Default canonical dimensions for known presets so the SQL CHECK
	// constraints always see positive card_width_mm / card_height_mm.
	if in.Card != CardCustom {
		w, h, err := CardDimensions(in.Card)
		if err == nil {
			if in.CardWidthMm == 0 {
				in.CardWidthMm = w
			}
			if in.CardHeightMm == 0 {
				in.CardHeightMm = h
			}
		}
	}
	if in.DPI == 0 {
		in.DPI = 300
	}
	if in.FlipEdge == "" {
		in.FlipEdge = FlipLongEdge
	}
	if in.Rows == 0 {
		in.Rows = 1
	}
	if in.Cols == 0 {
		in.Cols = 1
	}
	if err := validateCreateInput(in); err != nil {
		return Session{}, err
	}
	frontBody, err := s.readDocument(ctx, in.FrontDocID)
	if err != nil {
		return Session{}, fmt.Errorf("%w: front document: %v", ErrInvalid, err)
	}
	frontImg, _, _, _, err := Decode(frontBody)
	if err != nil {
		return Session{}, fmt.Errorf("%w: decode front: %v", ErrInvalid, err)
	}
	var backImg image.Image
	var backBody []byte
	if in.BackDocID != "" {
		backBody, err = s.readDocument(ctx, in.BackDocID)
		if err != nil {
			return Session{}, fmt.Errorf("%w: back document: %v", ErrInvalid, err)
		}
		backImg, _, _, _, err = Decode(backBody)
		if err != nil {
			return Session{}, fmt.Errorf("%w: decode back: %v", ErrInvalid, err)
		}
	}
	sessionID, err := randomID()
	if err != nil {
		return Session{}, err
	}
	dpi := in.DPI
	if dpi <= 0 {
		dpi = 300
	}
	flip := in.FlipEdge
	if flip == "" {
		flip = FlipLongEdge
	}
	rows := in.Rows
	if rows < 1 {
		rows = 1
	}
	cols := in.Cols
	if cols < 1 {
		cols = 1
	}
	nowUnix := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var backDoc sql.NullString
	if in.BackDocID != "" {
		backDoc = sql.NullString{String: in.BackDocID, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO id_card_sessions (id, order_id, front_doc_id, back_doc_id, sheet, card,
	card_width_mm, card_height_mm, layout_kind, flip_edge, rows, cols, actual_size, dpi,
	calibration_id, status, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		sessionID, nullString(in.OrderID), in.FrontDocID, backDoc, string(in.Sheet), string(in.Card),
		in.CardWidthMm, in.CardHeightMm, string(in.LayoutKind), string(flip), rows, cols,
		boolToInt(in.ActualSize), dpi, nullString(in.CalibrationID), "auto_detected", nowUnix, nowUnix,
	); err != nil {
		return Session{}, err
	}
	// Detect corners for the front document.
	frontResult, frontErr := DetectQuadrilateral(frontImg)
	if frontErr != nil {
		// Detection failure is recorded as a manual-required status.
		if _, err := tx.ExecContext(ctx, `UPDATE id_card_sessions SET status='pending', error=?, updated_at=? WHERE id=?`,
			"front detection: "+frontErr.Error(), nowUnix, sessionID); err != nil {
			return Session{}, err
		}
	} else {
		if err := writeCorners(ctx, tx, sessionID, in.FrontDocID, SideFront, frontResult.Quad, frontResult.Confidence, false); err != nil {
			return Session{}, err
		}
	}
	// Detect corners for the back document when present.
	if in.BackDocID != "" && backImg != nil {
		backResult, backErr := DetectQuadrilateral(backImg)
		if backErr != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE id_card_sessions SET status='pending', error=?, updated_at=? WHERE id=?`,
				"back detection: "+backErr.Error(), nowUnix, sessionID); err != nil {
				return Session{}, err
			}
		} else {
			if err := writeCorners(ctx, tx, sessionID, in.BackDocID, SideBack, backResult.Quad, backResult.Confidence, false); err != nil {
				return Session{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, sessionID)
}

// SetCorners replaces the corners for one side of a session. Used by the UI
// to record manual adjustments.
func (s *Service) SetCorners(ctx context.Context, sessionID string, side Side, c Corners) (Session, error) {
	if c.Manual == false {
		c.Manual = true // explicit operator action always marks manual
	}
	for _, p := range []Point{c.TL, c.TR, c.BR, c.BL} {
		if !isFinite(p.X) || !isFinite(p.Y) {
			return Session{}, fmt.Errorf("%w: non-finite corner", ErrInvalid)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var docID string
	if err := tx.QueryRowContext(ctx, `SELECT front_doc_id FROM id_card_sessions WHERE id=? AND removed_at IS NULL`, sessionID).Scan(&docID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	docIDToUse := docID
	if side == SideBack {
		var backDoc sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT back_doc_id FROM id_card_sessions WHERE id=?`, sessionID).Scan(&backDoc); err != nil {
			return Session{}, err
		}
		if !backDoc.Valid {
			return Session{}, fmt.Errorf("%w: session has no back document", ErrInvalid)
		}
		docIDToUse = backDoc.String
	}
	quad, err := MakeQuad(c.TL, c.TR, c.BR, c.BL)
	if err != nil {
		return Session{}, err
	}
	if err := upsertCorners(ctx, tx, sessionID, docIDToUse, side, quad, c.Confidence, c.Manual); err != nil {
		return Session{}, err
	}
	now := s.now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE id_card_sessions SET status='manual_confirmed', updated_at=? WHERE id=?`, now, sessionID); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, sessionID)
}

// UpdateLayout mutates the layout configuration of an existing session. It
// does not trigger re-detection; the operator updates corners separately
// when they change.
func (s *Service) UpdateLayout(ctx context.Context, sessionID string, in CreateSessionInput) (Session, error) {
	if in.LayoutKind == "" || in.Sheet == "" || in.Card == "" {
		return Session{}, fmt.Errorf("%w: layout, sheet and card are required", ErrInvalid)
	}
	dpi := in.DPI
	if dpi <= 0 {
		dpi = 300
	}
	flip := in.FlipEdge
	if flip == "" {
		flip = FlipLongEdge
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
UPDATE id_card_sessions SET sheet=?, card=?, card_width_mm=?, card_height_mm=?, layout_kind=?,
	flip_edge=?, rows=?, cols=?, actual_size=?, dpi=?, calibration_id=?, updated_at=?
WHERE id=? AND removed_at IS NULL`,
		string(in.Sheet), string(in.Card), in.CardWidthMm, in.CardHeightMm, string(in.LayoutKind),
		string(flip), in.Rows, in.Cols, boolToInt(in.ActualSize), dpi, nullString(in.CalibrationID),
		s.now().Unix(), sessionID,
	)
	if err != nil {
		return Session{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Session{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, sessionID)
}

// ComposeInput drives a sheet render. The front document is required; the
// back is required when the layout is side_by_side, vertical or duplex_back.
type ComposeInput struct {
	SessionID  string
	Side       Side // which output to render; front layouts render front, duplex_back renders back
}

// Compose renders the session's chosen layout to a PNG in the protected data
// directory and records the output row. Idempotent: a second call replaces
// the previous output for the same (session, side).
func (s *Service) Compose(ctx context.Context, in ComposeInput) (Session, error) {
	if in.SessionID == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	session, err := s.Get(ctx, in.SessionID)
	if err != nil {
		return Session{}, err
	}
	if session.FrontCorners == nil {
		return Session{}, fmt.Errorf("%w: front corners are required before compose", ErrInvalid)
	}
	side := in.Side
	if side == "" {
		side = SideFront
	}
	if side == SideBack && session.LayoutKind != LayoutSideBySide && session.LayoutKind != LayoutVertical && session.LayoutKind != LayoutDuplexBack {
		return Session{}, fmt.Errorf("%w: layout %q does not render a back side", ErrInvalid, session.LayoutKind)
	}
	frontBody, err := s.readDocument(ctx, session.FrontDocID)
	if err != nil {
		return Session{}, fmt.Errorf("%w: read front: %v", ErrInvalid, err)
	}
	srcFront, _, _, _, err := Decode(frontBody)
	if err != nil {
		return Session{}, fmt.Errorf("%w: decode front: %v", ErrInvalid, err)
	}
	var srcBack image.Image
	if side == SideBack && session.BackDocID != "" {
		body, err := s.readDocument(ctx, session.BackDocID)
		if err != nil {
			return Session{}, fmt.Errorf("%w: read back: %v", ErrInvalid, err)
		}
		srcBack, _, _, _, err = Decode(body)
		if err != nil {
			return Session{}, fmt.Errorf("%w: decode back: %v", ErrInvalid, err)
		}
	}
	dx, dy, err := s.calibrationOffsets(ctx, session)
	if err != nil {
		return Session{}, err
	}
	layout, err := ComputeLayout(LayoutInput{
		Sheet: session.Sheet,
		Card: session.Card,
		CardWidthMm: session.CardWidthMm,
		CardHeightMm: session.CardHeightMm,
		LayoutKind: session.LayoutKind,
		FlipEdge: session.FlipEdge,
		Rows: session.Rows,
		Columns: session.Cols,
		CalibrationDX: dx,
		CalibrationDY: dy,
		ActualSize: session.ActualSize,
	})
	if err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrCompose, err)
	}
	frontQuad := quadFromCorners(*session.FrontCorners)
	var backQuad Quad
	if session.BackCorners != nil {
		backQuad = quadFromCorners(*session.BackCorners)
	}
	// For duplex layouts only the matching side is rendered onto the sheet.
	// side==front renders the front placement; side==back renders the back.
	effectiveLayout := layout
	if session.LayoutKind == LayoutDuplexFront && side == SideFront {
		effectiveLayout.Placements = layout.Placements[:1]
	} else if session.LayoutKind == LayoutDuplexBack && side == SideBack {
		effectiveLayout.Placements = []Placement{layout.Placements[len(layout.Placements)-1]}
	} else if session.LayoutKind == LayoutSideBySide || session.LayoutKind == LayoutVertical {
		// One sheet side renders both placements; output side is just a label.
	} else {
		// Front-only or single placement; nothing to do.
	}
	sheet, err := RenderSheet(effectiveLayout, srcFront, frontQuad, srcBack, backQuad, float64(session.DPI))
	if err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrCompose, err)
	}
	// Write the PNG to a side-specific sub-directory.
	sub := filepath.Join("id-cards", session.ID, string(side))
	if err := s.files.MkdirAll(sub); err != nil {
		return Session{}, fmt.Errorf("%w: mkdir: %v", ErrCompose, err)
	}
	outputID, err := randomID()
	if err != nil {
		return Session{}, err
	}
	storageRel := filepath.Join(sub, outputID+".png")
	fullPath := filepath.Join(s.files.DataRoot(), storageRel)
	tmp := fullPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return Session{}, fmt.Errorf("%w: create tmp: %v", ErrCompose, err)
	}
	digest := sha256.New()
	mw := &countingWriter{w: f, hash: digest}
	if err := EncodePNG(mw, sheet); err != nil {
		f.Close()
		os.Remove(tmp)
		return Session{}, fmt.Errorf("%w: encode png: %v", ErrCompose, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Session{}, fmt.Errorf("%w: close: %v", ErrCompose, err)
	}
	if err := os.Rename(tmp, fullPath); err != nil {
		os.Remove(tmp)
		return Session{}, fmt.Errorf("%w: rename: %v", ErrCompose, err)
	}
	shaHex := hex.EncodeToString(digest.Sum(nil))
	nowUnix := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM id_card_outputs WHERE session_id=? AND side=?`, session.ID, string(side)); err != nil {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO id_card_outputs (id, session_id, side, sheet_width_mm, sheet_height_mm, pixel_width, pixel_height, storage_path, sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		outputID, session.ID, string(side),
		effectiveLayout.SheetWidthMm, effectiveLayout.SheetHeightMm,
		sheet.Bounds().Dx(), sheet.Bounds().Dy(),
		storageRel, shaHex, nowUnix,
	); err != nil {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE id_card_sessions SET status='composed', updated_at=? WHERE id=?`, nowUnix, session.ID); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, session.ID)
}

// Get returns a session including its corner rows and outputs.
//
// Soft-deleted sessions are filtered out so the studio cannot accidentally
// bring back a session the merchant has removed.
func (s *Service) Get(ctx context.Context, id string) (Session, error) {
	var sess Session
	var orderID, backDoc, calibration sql.NullString
	var removedAt sql.NullInt64
	var cardW, cardH float64
	var sheetStr, cardStr, layoutStr, flipStr, statusStr string
	err := s.db.QueryRowContext(ctx, `
SELECT id, order_id, front_doc_id, back_doc_id, sheet, card,
	card_width_mm, card_height_mm, layout_kind, flip_edge, rows, cols, actual_size, dpi,
	calibration_id, status, error, created_at, updated_at, removed_at
FROM id_card_sessions WHERE id=? AND removed_at IS NULL`, id).Scan(
		&sess.ID, &orderID, &sess.FrontDocID, &backDoc, &sheetStr, &cardStr,
		&cardW, &cardH, &layoutStr, &flipStr, &sess.Rows, &sess.Cols,
		&sess.ActualSize, &sess.DPI, &calibration, &statusStr, &sess.Error,
		&sess.CreatedAt, &sess.UpdatedAt, &removedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	sess.OrderID = orderID.String
	sess.BackDocID = backDoc.String
	sess.CalibrationID = calibration.String
	sess.RemovedAt = removedAt.Int64
	sess.Sheet = SheetPreset(sheetStr)
	sess.Card = CardPreset(cardStr)
	sess.LayoutKind = LayoutKind(layoutStr)
	sess.FlipEdge = FlipEdge(flipStr)
	sess.Status = statusStr
	sess.CardWidthMm = cardW
	sess.CardHeightMm = cardH
	// Load corners.
	corners, err := s.loadCorners(ctx, id)
	if err != nil {
		return Session{}, err
	}
	for i := range corners {
		c := corners[i]
		if c.Side == SideFront {
			sess.FrontCorners = &c
		} else {
			sess.BackCorners = &c
		}
	}
	outputs, err := s.loadOutputs(ctx, id)
	if err != nil {
		return Session{}, err
	}
	sess.Outputs = outputs
	return sess, nil
}

// List returns every non-removed session, newest first.
//
// The SQLite connection pool is intentionally small (max-open=1), so this
// implementation reads the ID list into memory and closes the rows handle
// before iterating. Each session then opens its own connection for the
// Get call. Calling Get while holding the rows open deadlocks because
// every subsequent query is blocked on the single connection.
func (s *Service) List(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM id_card_sessions WHERE removed_at IS NULL ORDER BY created_at DESC`)
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

// Remove soft-deletes the session. Outputs and corner rows are removed by
// ON DELETE CASCADE; the underlying customer document rows are retained so
// the order's audit trail stays intact.
func (s *Service) Remove(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE id_card_sessions SET removed_at=?, updated_at=? WHERE id=? AND removed_at IS NULL`, s.now().Unix(), s.now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Calibration CRUD -------------------------------------------------------------

// CreateCalibration persists a new calibration row.
func (s *Service) CreateCalibration(ctx context.Context, c Calibration) (Calibration, error) {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return Calibration{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if c.Sheet == "" {
		c.Sheet = SheetA4
	}
	if c.FlipEdge == "" {
		c.FlipEdge = FlipLongEdge
	}
	if c.DXmm < -25 || c.DXmm > 25 {
		return Calibration{}, fmt.Errorf("%w: dx_mm must be between -25 and 25", ErrInvalid)
	}
	if c.DYmm < -25 || c.DYmm > 25 {
		return Calibration{}, fmt.Errorf("%w: dy_mm must be between -25 and 25", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return Calibration{}, err
	}
	now := s.now().Unix()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO id_card_calibrations (id, name, sheet, flip_edge, dx_mm, dy_mm, notes, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, string(c.Sheet), string(c.FlipEdge), c.DXmm, c.DYmm, c.Notes, now, now,
	)
	if err != nil {
		return Calibration{}, err
	}
	c.ID = id
	c.CreatedAt = now
	c.UpdatedAt = now
	return c, nil
}

// ListCalibrations returns all calibration rows newest-first.
func (s *Service) ListCalibrations(ctx context.Context) ([]Calibration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, sheet, flip_edge, dx_mm, dy_mm, notes, created_at, updated_at FROM id_card_calibrations ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Calibration
	for rows.Next() {
		var c Calibration
		var sheetStr, flipStr string
		if err := rows.Scan(&c.ID, &c.Name, &sheetStr, &flipStr, &c.DXmm, &c.DYmm, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Sheet = SheetPreset(sheetStr)
		c.FlipEdge = FlipEdge(flipStr)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCalibration removes a calibration. Returns ErrNotFound when the id
// is unknown.
func (s *Service) DeleteCalibration(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM id_card_calibrations WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OutputBlobPath returns the absolute path on disk for a given output row.
// The portal uses this to serve the composed PNG back to the browser.
func (s *Service) OutputBlobPath(storagePath string) (string, error) {
	if storagePath == "" || strings.Contains(storagePath, "..") {
		return "", fmt.Errorf("%w: invalid storage path", ErrInvalid)
	}
	full := filepath.Join(s.files.DataRoot(), storagePath)
	if _, err := os.Stat(full); err != nil {
		return "", err
	}
	return full, nil
}

// helpers ---------------------------------------------------------------------

func validateCreateInput(in CreateSessionInput) error {
	if in.FrontDocID == "" {
		return fmt.Errorf("%w: front document id is required", ErrInvalid)
	}
	if in.Sheet == "" {
		return fmt.Errorf("%w: sheet is required", ErrInvalid)
	}
	if _, _, err := SheetDimensions(in.Sheet); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if in.Card == "" {
		return fmt.Errorf("%w: card preset is required", ErrInvalid)
	}
	if in.Card == CardCustom {
		if in.CardWidthMm < 10 || in.CardWidthMm > 400 {
			return fmt.Errorf("%w: custom card width must be between 10 and 400 mm", ErrInvalid)
		}
		if in.CardHeightMm < 10 || in.CardHeightMm > 400 {
			return fmt.Errorf("%w: custom card height must be between 10 and 400 mm", ErrInvalid)
		}
	} else {
		w, h, err := CardDimensions(in.Card)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if in.CardWidthMm == 0 {
			in.CardWidthMm = w
		}
		if in.CardHeightMm == 0 {
			in.CardHeightMm = h
		}
	}
	if in.LayoutKind == "" {
		return fmt.Errorf("%w: layout kind is required", ErrInvalid)
	}
	if in.Rows < 1 {
		in.Rows = 1
	}
	if in.Cols < 1 {
		in.Cols = 1
	}
	if in.Rows > 8 || in.Cols > 8 {
		return fmt.Errorf("%w: rows and cols must each be at most 8", ErrInvalid)
	}
	if in.DPI == 0 {
		in.DPI = 300
	}
	if in.DPI < 72 || in.DPI > 1200 {
		return fmt.Errorf("%w: dpi must be between 72 and 1200", ErrInvalid)
	}
	return nil
}

// readDocument reads the document blob from disk via the documents service.
// We do not import the documents package to avoid a circular import path:
// the idcards service is meant to be independent and uses only the storage
// path stored on the documents row.
func (s *Service) readDocument(ctx context.Context, docID string) ([]byte, error) {
	var path string
	if err := s.db.QueryRowContext(ctx, `SELECT storage_path FROM documents WHERE id=?`, docID).Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("document %q not found", docID)
		}
		return nil, err
	}
	if strings.Contains(path, "..") || !strings.HasPrefix(filepath.ToSlash(path), "documents/") {
		return nil, fmt.Errorf("invalid document path")
	}
	full := filepath.Join(s.files.DataRoot(), path)
	return os.ReadFile(full)
}

func (s *Service) calibrationOffsets(ctx context.Context, sess Session) (float64, float64, error) {
	if sess.CalibrationID == "" {
		return 0, 0, nil
	}
	var dx, dy float64
	err := s.db.QueryRowContext(ctx, `SELECT dx_mm, dy_mm FROM id_card_calibrations WHERE id=?`, sess.CalibrationID).Scan(&dx, &dy)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrBadCalibration
	}
	if err != nil {
		return 0, 0, err
	}
	return dx, dy, nil
}

func writeCorners(ctx context.Context, tx *sql.Tx, sessionID, docID string, side Side, quad Quad, confidence float64, manual bool) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	return upsertCorners(ctx, tx, sessionID, docID, side, quad, confidence, manual, id)
}

func upsertCorners(ctx context.Context, tx *sql.Tx, sessionID, docID string, side Side, quad Quad, confidence float64, manual bool, id ...string) error {
	rowID := ""
	if len(id) > 0 {
		rowID = id[0]
	}
	if rowID == "" {
		newID, err := randomID()
		if err != nil {
			return err
		}
		rowID = newID
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO id_card_corners (id, session_id, document_id, side, tl_x, tl_y, tr_x, tr_y, br_x, br_y, bl_x, bl_y, confidence, manual)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, side) DO UPDATE SET
	tl_x=excluded.tl_x, tl_y=excluded.tl_y, tr_x=excluded.tr_x, tr_y=excluded.tr_y,
	br_x=excluded.br_x, br_y=excluded.br_y, bl_x=excluded.bl_x, bl_y=excluded.bl_y,
	confidence=excluded.confidence, manual=excluded.manual,
	document_id=excluded.document_id`,
		rowID, sessionID, docID, string(side),
		quad.TL.X, quad.TL.Y, quad.TR.X, quad.TR.Y,
		quad.BR.X, quad.BR.Y, quad.BL.X, quad.BL.Y,
		confidence, boolToInt(manual),
	); err != nil {
		return err
	}
	return nil
}

func (s *Service) loadCorners(ctx context.Context, sessionID string) ([]Corners, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT side, tl_x, tl_y, tr_x, tr_y, br_x, br_y, bl_x, bl_y, confidence, manual
FROM id_card_corners WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Corners
	for rows.Next() {
		var c Corners
		var side string
		var manual int
		if err := rows.Scan(&side, &c.TL.X, &c.TL.Y, &c.TR.X, &c.TR.Y, &c.BR.X, &c.BR.Y, &c.BL.X, &c.BL.Y, &c.Confidence, &manual); err != nil {
			return nil, err
		}
		c.Side = Side(side)
		c.Manual = manual != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) loadOutputs(ctx context.Context, sessionID string) ([]Output, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, side, sheet_width_mm, sheet_height_mm, pixel_width, pixel_height, storage_path, sha256, created_at
FROM id_card_outputs WHERE session_id=? ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Output
	for rows.Next() {
		var o Output
		var side string
		if err := rows.Scan(&o.ID, &o.SessionID, &side, &o.SheetWidthMm, &o.SheetHeightMm, &o.PixelWidth, &o.PixelHeight, &o.StoragePath, &o.SHA256, &o.CreatedAt); err != nil {
			return nil, err
		}
		o.Side = Side(side)
		out = append(out, o)
	}
	return out, rows.Err()
}

func quadFromCorners(c Corners) Quad {
	return Quad{TL: c.TL, TR: c.TR, BR: c.BR, BL: c.BL}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// countingWriter hashes while writing so we can record the SHA256 of the
// composed PNG without re-reading the file from disk.
type countingWriter struct {
	w    interface{ Write([]byte) (int, error) }
	hash interface{ Write([]byte) (int, error) }
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.hash.Write(p[:n])
	}
	return n, err
}

// Compile-time guards.
var _ = json.Marshal
