package localserver

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
)

// idcardSessionView is the JSON shape the dashboard renders. It mirrors the
// idcards.Session struct but exposes stable field names and hides nothing
// the operator or owner is allowed to see. Server-side fields that are not
// useful in the UI (e.g. the removed_at column) are not serialized.
type idcardSessionView struct {
	ID            string                `json:"id"`
	OrderID       string                `json:"orderId"`
	FrontDocID    string                `json:"frontDocId"`
	BackDocID     string                `json:"backDocId"`
	Sheet         string                `json:"sheet"`
	Card          string                `json:"card"`
	CardWidthMm   float64               `json:"cardWidthMm"`
	CardHeightMm  float64               `json:"cardHeightMm"`
	LayoutKind    string                `json:"layoutKind"`
	FlipEdge      string                `json:"flipEdge"`
	Rows          int                   `json:"rows"`
	Cols          int                   `json:"cols"`
	ActualSize    bool                  `json:"actualSize"`
	DPI           int                   `json:"dpi"`
	CalibrationID string                `json:"calibrationId"`
	Status        string                `json:"status"`
	Error         string                `json:"error"`
	CreatedAt     int64                 `json:"createdAt"`
	UpdatedAt     int64                 `json:"updatedAt"`
	FrontCorners  *idcards.Corners      `json:"frontCorners,omitempty"`
	BackCorners   *idcards.Corners      `json:"backCorners,omitempty"`
	Outputs       []idcards.Output      `json:"outputs"`
}

func toIDCardSessionView(s idcards.Session) idcardSessionView {
	return idcardSessionView{
		ID:            s.ID,
		OrderID:       s.OrderID,
		FrontDocID:    s.FrontDocID,
		BackDocID:     s.BackDocID,
		Sheet:         string(s.Sheet),
		Card:          string(s.Card),
		CardWidthMm:   s.CardWidthMm,
		CardHeightMm:  s.CardHeightMm,
		LayoutKind:    string(s.LayoutKind),
		FlipEdge:      string(s.FlipEdge),
		Rows:          s.Rows,
		Cols:          s.Cols,
		ActualSize:    s.ActualSize,
		DPI:           s.DPI,
		CalibrationID: s.CalibrationID,
		Status:        s.Status,
		Error:         s.Error,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
		FrontCorners:  s.FrontCorners,
		BackCorners:   s.BackCorners,
		Outputs:       s.Outputs,
	}
}

// toIDCardCalibrationView copies the persisted calibration row.
func toIDCardCalibrationView(c idcards.Calibration) idcards.Calibration {
	return c
}

// registerOwnerIDCards wires the ID Card Studio HTTP surface. The studio is
// owner + operator readable; only the owner may create sessions, set manual
// corners, render output, manage calibrations or remove sessions. Owner
// limits come from the existing CanEditOrders / CanViewOrders grants which
// the studio reuses for parity with the rest of the owner UI.
func (s *Server) registerOwnerIDCards(mux *http.ServeMux) {
	if s.idcards == nil {
		return
	}

	// GET /api/v1/owner/id-cards/sessions — list sessions
	mux.HandleFunc("GET /api/v1/owner/id-cards/sessions", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		sessions, err := s.idcards.List(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		views := make([]idcardSessionView, 0, len(sessions))
		for _, sess := range sessions {
			views = append(views, toIDCardSessionView(sess))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": views})
	}))

	// POST /api/v1/owner/id-cards/sessions — create one session
	mux.HandleFunc("POST /api/v1/owner/id-cards/sessions", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input idcards.CreateSessionInput
		if !decodeOwnerJSON(w, r, &input, 64<<10) {
			return
		}
		sess, err := s.idcards.CreateSession(r.Context(), input)
		if err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(toIDCardSessionView(sess))
	}))

	// GET /api/v1/owner/id-cards/sessions/{id} — fetch one session
	mux.HandleFunc("GET /api/v1/owner/id-cards/sessions/{id}", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		sess, err := s.idcards.Get(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toIDCardSessionView(sess))
	}))

	// PUT /api/v1/owner/id-cards/sessions/{id}/corners — operator-confirmed corners
	mux.HandleFunc("PUT /api/v1/owner/id-cards/sessions/{id}/corners", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		var input struct {
			Side       string          `json:"side"`
			Corners    idcards.Corners `json:"corners"`
		}
		if !decodeOwnerJSON(w, r, &input, 16<<10) {
			return
		}
		side := idcards.SideFront
		switch input.Side {
		case string(idcards.SideFront):
			side = idcards.SideFront
		case string(idcards.SideBack):
			side = idcards.SideBack
		default:
			http.Error(w, `side must be "front" or "back"`, 400)
			return
		}
		sess, err := s.idcards.SetCorners(r.Context(), id, side, input.Corners)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toIDCardSessionView(sess))
	}))

	// PUT /api/v1/owner/id-cards/sessions/{id}/layout — update layout configuration
	mux.HandleFunc("PUT /api/v1/owner/id-cards/sessions/{id}/layout", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		var input idcards.CreateSessionInput
		if !decodeOwnerJSON(w, r, &input, 16<<10) {
			return
		}
		sess, err := s.idcards.UpdateLayout(r.Context(), id, input)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toIDCardSessionView(sess))
	}))

	// POST /api/v1/owner/id-cards/sessions/{id}/compose — render the PNG sheet
	mux.HandleFunc("POST /api/v1/owner/id-cards/sessions/{id}/compose", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		var input struct {
			Side string `json:"side"`
		}
		if !decodeOwnerJSON(w, r, &input, 4<<10) {
			return
		}
		side := idcards.SideFront
		switch input.Side {
		case "", string(idcards.SideFront):
			side = idcards.SideFront
		case string(idcards.SideBack):
			side = idcards.SideBack
		default:
			http.Error(w, `side must be "front" or "back"`, 400)
			return
		}
		sess, err := s.idcards.Compose(r.Context(), idcards.ComposeInput{SessionID: id, Side: side})
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toIDCardSessionView(sess))
	}))

	// DELETE /api/v1/owner/id-cards/sessions/{id} — soft-delete the session
	mux.HandleFunc("DELETE /api/v1/owner/id-cards/sessions/{id}", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		if err := s.idcards.Remove(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(204)
	}))

	// GET /api/v1/owner/id-cards/sessions/{id}/outputs/{side}/image — serve the
	// composed PNG. The handler resolves the storage path through the service
	// so path traversal is rejected, then streams the file with the correct
	// content-type.
	mux.HandleFunc("GET /api/v1/owner/id-cards/sessions/{id}/outputs/{side}/image", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		side := r.PathValue("side")
		if id == "" || (side != string(idcards.SideFront) && side != string(idcards.SideBack)) {
			http.Error(w, "session id and side (front/back) are required", 400)
			return
		}
		sess, err := s.idcards.Get(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		var storagePath string
		for _, o := range sess.Outputs {
			if string(o.Side) == side {
				storagePath = o.StoragePath
				break
			}
		}
		if storagePath == "" {
			http.Error(w, "no composed output for this side", 404)
			return
		}
		full, err := s.idcards.OutputBlobPath(storagePath)
		if err != nil {
			ownerError(w, err)
			return
		}
		fi, err := os.Stat(full)
		if err != nil {
			ownerError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		f, err := os.Open(full)
		if err != nil {
			ownerError(w, err)
			return
		}
		defer f.Close()
		// Read through userspace rather than sendfile: macOS restricts
		// sendfile across the descriptor pairs the test environment creates,
		// which manifests as a half-written body and an EOF on the client.
		// The composed ID-card PNG is always small enough to fit in memory.
		buf, err := io.ReadAll(f)
		if err != nil {
			ownerError(w, err)
			return
		}
		if _, err := w.Write(buf); err != nil {
			ownerError(w, err)
			return
		}
	}))

	// GET /api/v1/owner/id-cards/calibrations — list rows
	mux.HandleFunc("GET /api/v1/owner/id-cards/calibrations", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.idcards.ListCalibrations(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		out := make([]idcards.Calibration, 0, len(rows))
		for _, c := range rows {
			out = append(out, toIDCardCalibrationView(c))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"calibrations": out})
	}))

	// POST /api/v1/owner/id-cards/calibrations — create / overwrite a row
	mux.HandleFunc("POST /api/v1/owner/id-cards/calibrations", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input idcards.Calibration
		if !decodeOwnerJSON(w, r, &input, 4<<10) {
			return
		}
		row, err := s.idcards.CreateCalibration(r.Context(), input)
		if err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(toIDCardCalibrationView(row))
	}))

	// DELETE /api/v1/owner/id-cards/calibrations/{id} — remove a row
	mux.HandleFunc("DELETE /api/v1/owner/id-cards/calibrations/{id}", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "calibration id is required", 400)
			return
		}
		if err := s.idcards.DeleteCalibration(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(204)
	}))

	// GET /api/v1/owner/id-cards/documents — list customer documents the
	// studio can use as front/back sources. Mirrors the rows from the
	// documents table (id + original filename + mime type). Operators may
	// pick from this list when assembling a new session.
	mux.HandleFunc("GET /api/v1/owner/id-cards/documents", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.QueryContext(r.Context(), `SELECT id, original_filename, mime_type, page_count FROM documents ORDER BY created_at DESC LIMIT 200`)
		if err != nil {
			ownerError(w, err)
			return
		}
		defer rows.Close()
		type docSummary struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
			MIME     string `json:"mimeType"`
			Pages    int    `json:"pageCount"`
		}
		// Initialise to an empty slice so the JSON encoder emits [] not null
		// when no documents exist (the dashboard iterates the array unconditionally).
		out := []docSummary{}
		for rows.Next() {
			var d docSummary
			if err := rows.Scan(&d.ID, &d.Filename, &d.MIME, &d.Pages); err != nil {
				ownerError(w, err)
				return
			}
			out = append(out, d)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"documents": out})
	}))
}
