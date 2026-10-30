package localserver

import (
	"crypto/subtle"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"net/http"
	"time"
)

const remoteCookie = "__Secure-pc_remote"

func (s *Server) remoteSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	c, err := r.Cookie(remoteCookie)
	if err != nil || len(c.Value) != 64 {
		http.Error(w, "Sign in to manage your kiosk.", 401)
		return "", false
	}
	var username string
	err = s.db.QueryRowContext(r.Context(), `SELECT o.username FROM remote_sessions m JOIN owner_sessions a ON a.token_hash=m.owner_session_hash JOIN local_owner o ON o.singleton=a.owner_id WHERE m.token_hash=? AND m.expires_at>? AND a.expires_at>?`, owner.Digest(c.Value), time.Now().Unix(), time.Now().Unix()).Scan(&username)
	if err != nil {
		http.Error(w, "Session expired. Sign in again.", 401)
		return "", false
	}
	if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(owner.Digest("remote-csrf:"+c.Value))) != 1 {
		http.Error(w, "Invalid CSRF token", 403)
		return "", false
	}
	return c.Value, true
}

func (s *Server) registerRemote(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/remote/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "This remote action is not available.", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("GET /remote/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/remote/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		body, err := dashboardAssets.ReadFile("web/remote.html")
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
	mux.HandleFunc("POST /api/v1/remote/login", protect(func(w http.ResponseWriter, r *http.Request) {
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
		_, err = s.db.ExecContext(r.Context(), "DELETE FROM remote_sessions WHERE expires_at<=? OR owner_session_hash NOT IN(SELECT token_hash FROM owner_sessions)", time.Now().Unix())
		if err != nil {
			http.Error(w, "Could not sign in", 500)
			return
		}
		_, err = s.db.ExecContext(r.Context(), "INSERT INTO remote_sessions VALUES(?,?,?)", owner.Digest(token), owner.Digest(adminToken), time.Now().Add(8*time.Hour).Unix())
		if err != nil {
			http.Error(w, "Could not sign in", 500)
			return
		}
		keep = true
		http.SetCookie(w, &http.Cookie{Name: remoteCookie, Value: token, Path: "/api/v1/remote/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
		writeJSON(w, map[string]string{"csrfToken": owner.Digest("remote-csrf:" + token)})
	}))
	mux.HandleFunc("GET /api/v1/remote/orders", protect(func(w http.ResponseWriter, r *http.Request) {
		token, ok := s.remoteSession(w, r)
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
		writeJSON(w, map[string]any{"businessName": name, "orders": filtered, "paperStock": stock, "paperStockUnavailable": stockErr != nil, "csrfToken": owner.Digest("remote-csrf:" + token)})
	}))
	mux.HandleFunc("GET /api/v1/remote/pricing/grid/{scope}", protect(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.remoteSession(w, r); !ok {
			return
		}
		s.getPricingGrid(w, r)
	}))
	mux.HandleFunc("PUT /api/v1/remote/pricing/grid/{scope}", protect(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.remoteSession(w, r); !ok {
			return
		}
		s.savePricingGrid(w, r)
	}))

	mux.HandleFunc("POST /api/v1/remote/logout", protect(func(w http.ResponseWriter, r *http.Request) {
		token, ok := s.remoteSession(w, r)
		if !ok {
			return
		}
		_, err := s.db.ExecContext(r.Context(), "DELETE FROM owner_sessions WHERE token_hash IN(SELECT owner_session_hash FROM remote_sessions WHERE token_hash=?)", owner.Digest(token))
		if err != nil {
			http.Error(w, "Could not sign out", 500)
			return
		}
		s.db.ExecContext(r.Context(), "DELETE FROM remote_sessions WHERE token_hash=?", owner.Digest(token))
		http.SetCookie(w, &http.Cookie{Name: remoteCookie, Path: "/api/v1/remote/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, map[string]bool{"signedOut": true})
	}))
}
