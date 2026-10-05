package kiosk

import (
	"crypto/sha256"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"io"
	"mime"
	"net/http"
)

func (s *Server) accepted(w http.ResponseWriter, r *http.Request, order string) {
	token, err := randomSecret(32)
	if err != nil {
		respond(w, 503, "assistance")
		return
	}
	hash := sha256.Sum256([]byte(token))
	if _, err = s.db.ExecContext(r.Context(), `INSERT INTO kiosk_screen_receipts VALUES(?,?,?)`, hash[:], order, s.now().Unix()+86400); err != nil {
		respond(w, 503, "assistance")
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM kiosk_screen_receipts WHERE expires_at<=?`, s.now().Unix())
	w.WriteHeader(202)
	_ = json.NewEncoder(w).Encode(map[string]string{"state": "accepted", "receipt": token})
}

// Progress is scoped to a random release receipt, never an enumerable order ID.
func (s *Server) progress(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.RawQuery != "" {
		respond(w, 405, "method_not_allowed")
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		respond(w, 415, "json_required")
		return
	}
	var input struct {
		Receipt string `json:"receipt"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.Receipt) != 64 {
		respond(w, 400, "unavailable")
		return
	}
	hash := sha256.Sum256([]byte(input.Receipt))
	var id string
	if err = s.db.QueryRowContext(r.Context(), `SELECT order_id FROM kiosk_screen_receipts WHERE token_hash=? AND expires_at>?`, hash[:], s.now().Unix()).Scan(&id); err != nil {
		respond(w, 404, "unavailable")
		return
	}
	state, err := orders.New(s.db, nil).PrintProgress(r.Context(), id)
	if err != nil {
		respond(w, 503, "assistance")
		return
	}
	var review int
	if err = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM print_submissions j JOIN order_lines l ON l.id=j.line_id WHERE l.order_id=? AND j.progress='review'`, id).Scan(&review); err != nil {
		respond(w, 503, "assistance")
		return
	}
	if review > 0 {
		state = "assistance"
	}
	respond(w, 200, state)
}
