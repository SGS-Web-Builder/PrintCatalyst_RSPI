package localserver

import (
	"crypto/subtle"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const monitorCookie = "__Secure-pc_monitor"

// Monitoring is a separate read-only surface. Local administration guards and
// cookies are deliberately not reused or relaxed for tunnel traffic.
func (s *Server) monitorGuard(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil || s.owner == nil || s.orders == nil {
		http.Error(w, "Monitoring unavailable", 503)
		return false
	}
	origin, err := s.paymentOrigin(r.Context())
	if err != nil {
		http.Error(w, "Save and verify your HTTPS domain in QR setup before using mobile monitoring.", 403)
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		http.Error(w, "Invalid domain configuration", 403)
		return false
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	if !strings.EqualFold(host, u.Host) || (r.TLS == nil && !(isLoopbackClient(r.RemoteAddr) && r.Header.Get("X-Forwarded-Proto") == "https")) {
		http.Error(w, "Use the connected HTTPS domain for monitoring.", 403)
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Same-origin access required", 403)
		return false
	}
	if r.Method != "GET" && r.Header.Get("Origin") != origin {
		http.Error(w, "Same-origin access required", 403)
		return false
	}
	return s.portalGuard(w, r, r.Method != "GET")
}

func (s *Server) monitorSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	c, err := r.Cookie(monitorCookie)
	if err != nil || len(c.Value) != 64 {
		http.Error(w, "Sign in to monitor your shop.", 401)
		return "", false
	}
	var username string
	err = s.db.QueryRowContext(r.Context(), `SELECT o.username FROM monitor_sessions m JOIN owner_sessions a ON a.token_hash=m.owner_session_hash JOIN local_owner o ON o.singleton=a.owner_id WHERE m.token_hash=? AND m.expires_at>? AND a.expires_at>?`, owner.Digest(c.Value), time.Now().Unix(), time.Now().Unix()).Scan(&username)
	if err != nil {
		http.Error(w, "Session expired. Sign in again.", 401)
		return "", false
	}
	if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(owner.Digest("monitor-csrf:"+c.Value))) != 1 {
		http.Error(w, "Invalid CSRF token", 403)
		return "", false
	}
	return c.Value, true
}

func (s *Server) registerMonitor(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/monitor/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Monitoring is read-only; this action is not available.", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("GET /monitor/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monitor/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		body, err := dashboardAssets.ReadFile("web/monitor.html")
		if err != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		w.Write(body)
	})
	protect := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.monitorGuard(w, r) {
				fn(w, r)
			}
		}
	}
	mux.HandleFunc("POST /api/v1/monitor/login", protect(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeOwner(w, r, &input) {
			return
		}
		adminToken, role, err := s.owner.Login(r.Context(), input.Username, input.Password)
		if err != nil {
			ownerError(w, err)
			return
		}
		keep := false
		defer func() {
			if !keep {
				s.owner.Logout(r.Context(), adminToken)
			}
		}()
		if role != owner.RoleOwner {
			http.Error(w, "Merchant owner login required", 403)
			return
		}
		token, err := owner.RandomToken()
		if err != nil {
			http.Error(w, "Could not sign in", 500)
			return
		}
		_, err = s.db.ExecContext(r.Context(), "DELETE FROM monitor_sessions WHERE expires_at<=? OR owner_session_hash NOT IN(SELECT token_hash FROM owner_sessions)", time.Now().Unix())
		if err != nil {
			http.Error(w, "Could not sign in", 500)
			return
		}
		_, err = s.db.ExecContext(r.Context(), "INSERT INTO monitor_sessions VALUES(?,?,?)", owner.Digest(token), owner.Digest(adminToken), time.Now().Add(8*time.Hour).Unix())
		if err != nil {
			http.Error(w, "Could not sign in", 500)
			return
		}
		keep = true
		http.SetCookie(w, &http.Cookie{Name: monitorCookie, Value: token, Path: "/api/v1/monitor/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
		writeJSON(w, map[string]string{"csrfToken": owner.Digest("monitor-csrf:" + token)})
	}))
	mux.HandleFunc("GET /api/v1/monitor/orders", protect(func(w http.ResponseWriter, r *http.Request) {
		token, ok := s.monitorSession(w, r)
		if !ok {
			return
		}
		list, err := s.orders.List(r.Context())
		if err != nil {
			http.Error(w, "Could not load orders", 500)
			return
		}
		views := make([]orderListView, 0, len(list))
		for _, o := range list {
			views = append(views, orderListView{ID: o.ID, Status: o.Status, TotalMinor: o.TotalMinor, Currency: o.Currency, CurrencyMU: o.CurrencyMU, CustomerName: o.CustomerName, CustomerPhone: o.CustomerPhone, PaymentMethod: o.PaymentMethod, CreatedAt: o.CreatedAt})
		}
		if err = s.enrichOrderQueue(r.Context(), views); err != nil {
			http.Error(w, "Could not load orders", 500)
			return
		}
		filtered := []orderListView{}
		for _, v := range views {
			if len(v.Documents) > 0 {
				filtered = append(filtered, v)
			}
		}
		name := "Your shop"
		if profile, e := s.owner.Profile(r.Context()); e == nil && profile.Name != "" {
			name = profile.Name
		}
		stock, stockErr := s.readPaperStock(r.Context())
		if stockErr != nil {
			stock = []paperStockView{}
		}
		writeJSON(w, map[string]any{"businessName": name, "orders": filtered, "paperStock": stock, "paperStockUnavailable": stockErr != nil, "csrfToken": owner.Digest("monitor-csrf:" + token)})
	}))
	mux.HandleFunc("POST /api/v1/monitor/logout", protect(func(w http.ResponseWriter, r *http.Request) {
		token, ok := s.monitorSession(w, r)
		if !ok {
			return
		}
		_, err := s.db.ExecContext(r.Context(), "DELETE FROM owner_sessions WHERE token_hash IN(SELECT owner_session_hash FROM monitor_sessions WHERE token_hash=?)", owner.Digest(token))
		if err != nil {
			http.Error(w, "Could not sign out", 500)
			return
		}
		s.db.ExecContext(r.Context(), "DELETE FROM monitor_sessions WHERE token_hash=?", owner.Digest(token))
		http.SetCookie(w, &http.Cookie{Name: monitorCookie, Path: "/api/v1/monitor/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, map[string]bool{"signedOut": true})
	}))
}
