package localserver

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
)

// passportSessionView is the JSON shape the dashboard renders. It mirrors
// the passport.Session struct but exposes stable field names and hides
// nothing the operator or owner is allowed to see. Server-side fields that
// are not useful in the UI (e.g. the removed_at column) are not serialized.
type passportSessionView struct {
	ID                 string               `json:"id"`
	OrderID            string               `json:"orderId"`
	DocumentID         string               `json:"documentId"`
	Preset             string               `json:"preset"`
	PresetDisplayName  string               `json:"presetDisplayName"`
	WidthMm            float64              `json:"widthMm"`
	HeightMm           float64              `json:"heightMm"`
	Background         string               `json:"background"`
	HeadHeightMm       float64              `json:"headHeightMm"`
	EyeLineFromBottomMm float64             `json:"eyeLineFromBottomMm"`
	Status             string               `json:"status"`
	Error              string               `json:"error"`
	CreatedAt          int64                `json:"createdAt"`
	UpdatedAt          int64                `json:"updatedAt"`
	FaceRegion         *passport.FaceRegion `json:"faceRegion,omitempty"`
	Outputs            []passport.Output    `json:"outputs"`
}

func toPassportSessionView(s passport.Session) passportSessionView {
	display := string(s.Preset)
	if spec, err := passport.PresetFor(s.Preset); err == nil {
		display = spec.DisplayName
	}
	return passportSessionView{
		ID:                 s.ID,
		OrderID:            s.OrderID,
		DocumentID:         s.DocumentID,
		Preset:             string(s.Preset),
		PresetDisplayName:  display,
		WidthMm:            s.WidthMm,
		HeightMm:           s.HeightMm,
		Background:         string(s.Background),
		HeadHeightMm:       s.HeadHeightMm,
		EyeLineFromBottomMm: s.EyeLineFromBottomMm,
		Status:             s.Status,
		Error:              s.Error,
		CreatedAt:          s.CreatedAt,
		UpdatedAt:          s.UpdatedAt,
		FaceRegion:         s.FaceRegion,
		Outputs:            s.Outputs,
	}
}

// registerOwnerPassports wires the Passport Photo Studio HTTP surface. The
// studio is owner + operator readable; only the owner may create sessions,
// override the face region, render output or remove sessions. The studio
// never stores image bytes itself: every request carries a document id that
// references the existing documents table.
func (s *Server) registerOwnerPassports(mux *http.ServeMux) {
	if s.passports == nil {
		return
	}

	// GET /api/v1/owner/passports/sessions — list sessions
	mux.HandleFunc("GET /api/v1/owner/passports/sessions", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		sessions, err := s.passports.List(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		views := make([]passportSessionView, 0, len(sessions))
		for _, sess := range sessions {
			views = append(views, toPassportSessionView(sess))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": views})
	}))

	// GET /api/v1/owner/passports/presets — list known country presets
	mux.HandleFunc("GET /api/v1/owner/passports/presets", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		presets := make([]map[string]any, 0)
		for _, p := range passport.KnownPresets() {
			spec, err := passport.PresetFor(p)
			if err != nil {
				continue
			}
			presets = append(presets, map[string]any{
				"preset":              string(spec.Preset),
				"displayName":         spec.DisplayName,
				"countryCode":         spec.CountryCode,
				"widthMm":             spec.WidthMm,
				"heightMm":            spec.HeightMm,
				"headHeightMm":        spec.HeadHeightMm,
				"eyeLineFromBottomMm": spec.EyeLineFromBottomMm,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"presets": presets})
	}))

	// POST /api/v1/owner/passports/sessions — create one session
	mux.HandleFunc("POST /api/v1/owner/passports/sessions", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input passport.CreateSessionInput
		if !decodeOwnerJSON(w, r, &input, 16<<10) {
			return
		}
		sess, err := s.passports.CreateSession(r.Context(), input)
		if err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(toPassportSessionView(sess))
	}))

	// GET /api/v1/owner/passports/sessions/{id} — fetch one session
	mux.HandleFunc("GET /api/v1/owner/passports/sessions/{id}", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		sess, err := s.passports.Get(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toPassportSessionView(sess))
	}))

	// PUT /api/v1/owner/passports/sessions/{id}/face-region — operator-confirmed face region
	mux.HandleFunc("PUT /api/v1/owner/passports/sessions/{id}/face-region", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		var input passport.FaceRegion
		if !decodeOwnerJSON(w, r, &input, 4<<10) {
			return
		}
		sess, err := s.passports.SetFaceRegion(r.Context(), id, input)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toPassportSessionView(sess))
	}))

	// PUT /api/v1/owner/passports/sessions/{id}/layout — change preset, background, sheet layout
	mux.HandleFunc("PUT /api/v1/owner/passports/sessions/{id}/layout", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		var input passport.CreateSessionInput
		if !decodeOwnerJSON(w, r, &input, 16<<10) {
			return
		}
		sess, err := s.passports.UpdateLayout(r.Context(), id, input)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toPassportSessionView(sess))
	}))

	// POST /api/v1/owner/passports/sessions/{id}/compose — render the PNG sheet
	mux.HandleFunc("POST /api/v1/owner/passports/sessions/{id}/compose", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		sess, err := s.passports.Compose(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(toPassportSessionView(sess))
	}))

	// DELETE /api/v1/owner/passports/sessions/{id} — soft-delete the session
	mux.HandleFunc("DELETE /api/v1/owner/passports/sessions/{id}", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "session id is required", 400)
			return
		}
		if err := s.passports.Remove(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(204)
	}))

	// GET /api/v1/owner/passports/sessions/{id}/outputs/{outputID}/image — serve
	// the composed PNG. The handler resolves the storage path through the
	// service so path traversal is rejected, then streams the file with the
	// correct content-type.
	mux.HandleFunc("GET /api/v1/owner/passports/sessions/{id}/outputs/{outputID}/image", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		outputID := r.PathValue("outputID")
		if id == "" || outputID == "" {
			http.Error(w, "session id and output id are required", 400)
			return
		}
		sess, err := s.passports.Get(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		var storagePath string
		for _, o := range sess.Outputs {
			if o.ID == outputID {
				storagePath = o.StoragePath
				break
			}
		}
		if storagePath == "" {
			http.Error(w, "no composed output for this session", 404)
			return
		}
		full, err := s.passports.OutputBlobPath(storagePath)
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
		// Read through a buffered reader so the body never enters the
		// kernel-level sendfile path. macOS's sendfile is restricted when the
		// caller cannot prove the descriptor pair is compatible, and that
		// restriction manifests as a half-written body and an EOF on the
		// client. The composed PNG is always small enough to fit in memory
		// (an A4 page at 300 DPI is well under 1 MiB), so an in-process
		// copy is both correct and cheaper than diagnosing a per-platform
		// sendfile quirk in production.
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

	// GET /api/v1/owner/passports/documents — list customer documents the
	// studio can use as the source photo. Always returns a JSON array (never
	// null) so the dashboard renders an empty state without an extra null
	// check.
	mux.HandleFunc("GET /api/v1/owner/passports/documents", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
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