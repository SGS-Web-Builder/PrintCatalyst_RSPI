package localserver

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/advertising"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const adCookie = "pc_ad_admin"
const adLimit = 80 << 20

var adWrite sync.Mutex

// Advertising has its own owner-only session. Monitor cookies never authorize writes.
func (s *Server) adGuard(w http.ResponseWriter, r *http.Request) bool {
	if s.db == nil || s.files == nil || s.owner == nil {
		http.Error(w, "Advertising unavailable", 503)
		return false
	}
	// Existing host/origin validation is reused; raw media has its own type checks.
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Content-Type", "application/json")
	if strings.HasPrefix(r.Host, "localhost:") || strings.HasPrefix(r.Host, "127.0.0.1:") || strings.HasPrefix(r.Host, "[::1]:") {
		return s.localOwnerRequest(w, copy)
	}
	return s.monitorGuard(w, copy)
}
func (s *Server) adSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	cs := r.CookiesNamed(adCookie)
	if len(cs) != 1 || len(cs[0].Value) != 64 {
		http.Error(w, "Sign in to manage advertisements", 401)
		return "", false
	}
	token := cs[0].Value
	var found int
	e := s.db.QueryRowContext(r.Context(), `SELECT 1 FROM ad_admin_sessions m JOIN owner_sessions a ON a.token_hash=m.owner_session_hash JOIN local_owner o ON o.singleton=a.owner_id WHERE m.token_hash=? AND m.expires_at>? AND a.expires_at>?`, owner.Digest(token), time.Now().Unix(), time.Now().Unix()).Scan(&found)
	if e != nil {
		http.Error(w, "Session expired", 401)
		return "", false
	}
	if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(owner.Digest("ad-csrf:"+token))) != 1 {
		http.Error(w, "Invalid CSRF token", 403)
		return "", false
	}
	return token, true
}
func (s *Server) registerAds(mux *http.ServeMux) {
	mux.HandleFunc("GET /ads-admin/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ads-admin/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		body, _ := dashboardAssets.ReadFile("web/ads-admin.html")
		w.Write(body)
	})
	mux.HandleFunc("/api/v1/ads-admin/", func(w http.ResponseWriter, r *http.Request) {
		if !s.adGuard(w, r) {
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/api/v1/ads-admin/")
		if action == "login" && r.Method == "POST" {
			var in struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if !decodeOwner(w, r, &in) {
				return
			}
			admin, role, e := s.owner.Login(r.Context(), in.Username, in.Password)
			if e != nil {
				ownerError(w, e)
				return
			}
			if role != owner.RoleOwner {
				s.owner.Logout(r.Context(), admin)
				http.Error(w, "Owner login required", 403)
				return
			}
			token, e := owner.RandomToken()
			if e != nil {
				s.owner.Logout(r.Context(), admin)
				http.Error(w, "Sign-in failed", 500)
				return
			}
			_, e = s.db.ExecContext(r.Context(), "INSERT INTO ad_admin_sessions VALUES(?,?,?)", owner.Digest(token), owner.Digest(admin), time.Now().Add(8*time.Hour).Unix())
			if e != nil {
				s.owner.Logout(r.Context(), admin)
				http.Error(w, "Sign-in failed", 500)
				return
			}
			secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			http.SetCookie(w, &http.Cookie{Name: adCookie, Value: token, Path: "/api/v1/ads-admin/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
			writeJSON(w, map[string]string{"csrfToken": owner.Digest("ad-csrf:" + token)})
			return
		}
		token, ok := s.adSession(w, r)
		if !ok {
			return
		}
		if action == "logout" && r.Method == "POST" {
			_, e := s.db.ExecContext(r.Context(), "DELETE FROM ad_admin_sessions WHERE token_hash=?", owner.Digest(token))
			if e != nil {
				http.Error(w, "Logout failed", 500)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: adCookie, Path: "/api/v1/ads-admin/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
		if action == "campaigns" && r.Method == "GET" {
			list, e := advertising.List(r.Context(), s.db)
			if e != nil {
				http.Error(w, "Could not load ads", 500)
				return
			}
			writeJSON(w, map[string]any{"campaigns": list, "csrfToken": owner.Digest("ad-csrf:" + token), "serverTime": time.Now().Format(time.RFC3339)})
			return
		}
		if r.Method == "POST" && action == "media" {
			s.uploadAd(w, r)
			return
		}
		if r.Method == "PUT" && strings.HasPrefix(action, "campaigns/") {
			s.saveAd(w, r, strings.TrimPrefix(action, "campaigns/"))
			return
		}
		http.Error(w, "Unknown advertisement action", 404)
	})
}
func (s *Server) uploadAd(w http.ResponseWriter, r *http.Request) {
	// Keep uploads serialized, bounded and outside all print workers.
	if !adWrite.TryLock() {
		http.Error(w, "Another ad is being saved. Retry shortly.", 409)
		return
	}
	defer adWrite.Unlock()
	var count int
	var total int64
	if e := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*),COALESCE(SUM(size),0) FROM ad_campaigns").Scan(&count, &total); e != nil {
		http.Error(w, "Storage unavailable", 500)
		return
	}
	if count >= 50 || total+adLimit > 1<<30 {
		http.Error(w, "Ad library limit reached (50 files / 1 GiB)", 409)
		return
	}
	kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "video/mp4": ".mp4", "video/webm": ".webm"}[kind]
	if e != nil || ext == "" {
		http.Error(w, "Use JPEG, PNG, WebP, MP4 or WebM", 415)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, adLimit)
	prefix := make([]byte, 512)
	n, e := io.ReadFull(r.Body, prefix)
	if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
		http.Error(w, "Upload failed", 400)
		return
	}
	prefix = prefix[:n]
	detected := http.DetectContentType(prefix)
	valid := detected == kind
	if kind == "video/mp4" {
		valid = n >= 12 && string(prefix[4:8]) == "ftyp"
	}
	if kind == "video/webm" {
		valid = n >= 4 && bytes.Equal(prefix[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) && bytes.Contains(prefix, []byte("webm"))
	}
	if !valid {
		http.Error(w, "File content does not match its media type", 415)
		return
	}
	id, e := owner.RandomToken()
	if e != nil {
		http.Error(w, "Upload failed", 500)
		return
	}
	id = id[:32]
	filename := id + ext
	relative := "ads/" + filename
	if e = s.files.WriteAtomic(relative, io.MultiReader(bytes.NewReader(prefix), r.Body)); e != nil {
		http.Error(w, "Upload failed: check size (80 MiB maximum) and free disk space", 400)
		return
	}
	file, e := s.files.Open(relative)
	if e != nil {
		http.Error(w, "Upload failed", 500)
		return
	}
	info, e := file.Stat()
	file.Close()
	if e != nil {
		s.files.Remove(relative)
		http.Error(w, "Upload failed", 500)
		return
	}
	decoded, _ := url.QueryUnescape(r.Header.Get("X-Ad-Name"))
	name := strings.TrimSpace(decoded)
	if name == "" {
		name = "New advertisement"
	}
	if len(name) > 160 {
		name = name[:160]
	}
	zone, _ := time.LoadLocation("Asia/Kolkata")
	schedule := advertising.Schedule{Zone: "Asia/Kolkata", Start: time.Now().In(zone).Format("2006-01-02T15:04"), Duration: 1, Unit: "days", Repeat: "none", ImageSeconds: 10}
	raw, _ := json.Marshal(schedule)
	_, e = s.db.ExecContext(r.Context(), "INSERT INTO ad_campaigns VALUES(?,?,?,?,?,?,?)", id, name, filename, kind, info.Size(), string(raw), time.Now().Unix())
	if e != nil {
		s.files.Remove(relative)
		http.Error(w, "Could not save advertisement", 500)
		return
	}
	w.WriteHeader(201)
	writeJSON(w, map[string]string{"id": id})
}
func (s *Server) saveAd(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		Name     string               `json:"name"`
		Schedule advertising.Schedule `json:"schedule"`
	}
	if !decodeOwner(w, r, &in) {
		return
	}
	if _, _, e := in.Schedule.Bounds(); e != nil || len(strings.TrimSpace(in.Name)) == 0 || len(in.Name) > 160 {
		http.Error(w, "Invalid schedule, timezone or name", 400)
		return
	}
	if !adWrite.TryLock() {
		http.Error(w, "Another ad is being saved. Retry shortly.", 409)
		return
	}
	defer adWrite.Unlock()
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		http.Error(w, "Save failed", 500)
		return
	}
	defer tx.Rollback()
	if in.Schedule.Default {
		// One default, selected atomically with the edited campaign.
		_, e = tx.ExecContext(r.Context(), `UPDATE ad_campaigns SET schedule=json_set(schedule,'$.default',json('false')) WHERE id<>?`, id)
		if e != nil {
			http.Error(w, "Save failed", 500)
			return
		}
	}
	raw, _ := json.Marshal(in.Schedule)
	result, e := tx.ExecContext(r.Context(), "UPDATE ad_campaigns SET name=?,schedule=? WHERE id=?", strings.TrimSpace(in.Name), string(raw), id)
	if e != nil {
		http.Error(w, "Save failed", 500)
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		http.Error(w, "Advertisement not found", 404)
		return
	}
	if e = tx.Commit(); e != nil {
		http.Error(w, "Save failed", 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
