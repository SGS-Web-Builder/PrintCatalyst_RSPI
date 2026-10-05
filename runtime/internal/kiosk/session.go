package kiosk

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
)

//go:embed ui/*
var assets embed.FS

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) bool {
	names := map[string]string{"/": "index.html", "/kiosk.js": "kiosk.js", "/kiosk.css": "kiosk.css"}
	name, ok := names[r.URL.Path]
	if !ok {
		return false
	}
	if r.Method != "GET" || r.URL.RawQuery != "" {
		respond(w, 404, "not_found")
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+Address {
		respond(w, 403, "forbidden")
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" && site != "same-origin" {
		respond(w, 403, "forbidden")
		return true
	}
	data, err := assets.ReadFile("ui/" + name)
	if err != nil {
		respond(w, 503, "unavailable")
		return true
	}
	types := map[string]string{"index.html": "text/html; charset=utf-8", "kiosk.js": "text/javascript; charset=utf-8", "kiosk.css": "text/css; charset=utf-8"}
	w.Header().Set("Content-Type", types[name])
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	_, _ = w.Write(data)
	return true
}

func randomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) validSession(r *http.Request) bool {
	cookies := r.CookiesNamed("pc_kiosk")
	if len(cookies) != 1 || len(cookies[0].Value) != 64 {
		return false
	}
	hash := sha256.Sum256([]byte(cookies[0].Value))
	var saved, credential []byte
	var until int64
	if err := s.db.QueryRowContext(r.Context(), `SELECT token_hash,credential_hash,expires_at FROM kiosk_screen_session WHERE singleton=1`).Scan(&saved, &credential, &until); err != nil {
		return false
	}
	return s.now().Unix() < until && subtle.ConstantTimeCompare(hash[:], saved) == 1 && subtle.ConstantTimeCompare(s.credential[:], credential) == 1

}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, bearer bool) bool {
	path := r.URL.Path
	if path != "/api/v1/kiosk/pairing-ticket" && path != "/api/v1/kiosk/session" {
		return false
	}
	if r.URL.RawQuery != "" || r.Method != "POST" {
		respond(w, 405, "method_not_allowed")
		return true
	}
	if path == "/api/v1/kiosk/pairing-ticket" {
		if !bearer {
			respond(w, 403, "forbidden")
			return true
		}
		ticket, err := randomSecret(6)
		if err != nil {
			respond(w, 503, "unavailable")
			return true
		}
		s.sessionMu.Lock()
		s.ticketHash = sha256.Sum256([]byte(ticket))
		s.ticketUntil = s.now().Add(2 * time.Minute)
		s.sessionMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"ticket": ticket})
		return true
	}
	// Pairing attempts share the durable installation budget; restarting cannot reset it.
	if wait, err := s.reserve(r.Context()); err != nil {
		respond(w, 503, "unavailable")
		return true
	} else if wait > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(wait, 10))
		respond(w, 429, "cooldown")
		return true
	}
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		respond(w, 415, "json_required")
		return true
	}
	var input struct {
		Ticket string `json:"ticket"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.Ticket) != 12 {
		respond(w, 403, "pairing_required")
		return true
	}
	token, err := randomSecret(32)
	if err != nil {
		respond(w, 503, "unavailable")
		return true
	}
	hash := sha256.Sum256([]byte(input.Ticket))
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if !s.now().Before(s.ticketUntil) || subtle.ConstantTimeCompare(hash[:], s.ticketHash[:]) != 1 {
		respond(w, 403, "pairing_required")
		return true
	}
	s.ticketUntil = time.Time{}
	s.ticketHash = [32]byte{}
	s.sessionHash = sha256.Sum256([]byte(token))
	s.sessionUntil = s.now().Add(10 * 365 * 24 * time.Hour)
	if _, err = s.db.ExecContext(r.Context(), `INSERT INTO kiosk_screen_session VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET token_hash=excluded.token_hash,credential_hash=excluded.credential_hash,expires_at=excluded.expires_at`, s.sessionHash[:], s.credential[:], s.sessionUntil.Unix()); err != nil {
		respond(w, 503, "unavailable")
		return true
	}
	// Secure is intentionally absent: this listener is literal HTTP loopback only.
	http.SetCookie(w, &http.Cookie{Name: "pc_kiosk", Value: token, Path: "/api/v1/kiosk/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 10 * 365 * 24 * 3600})
	respond(w, 200, "paired")
	return true
}
