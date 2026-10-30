package localserver

import (
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/advertising"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"net/http"
	"time"
)

func (s *Server) registerRemoteAds(mux *http.ServeMux) {
	mux.HandleFunc("GET /remote/ads/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/remote/ads/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		body, _ := dashboardAssets.ReadFile("web/ads-admin.html")
		w.Write(body)
	})
	protect := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.adGuard(w, r) {
				return
			}
			if _, ok := s.remoteSession(w, r); !ok {
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/remote/ads/campaigns", protect(func(w http.ResponseWriter, r *http.Request) {
		token, _ := r.Cookie(remoteCookie)
		list, err := advertising.List(r.Context(), s.db)
		if err != nil {
			ownerError(w, err)
			return
		}
		writeJSON(w, map[string]any{"campaigns": list, "csrfToken": owner.Digest("remote-csrf:" + token.Value), "serverTime": time.Now().Format(time.RFC3339)})
	}))
	mux.HandleFunc("POST /api/v1/remote/ads/media", protect(s.uploadAd))
	mux.HandleFunc("PUT /api/v1/remote/ads/campaigns/{id}", protect(func(w http.ResponseWriter, r *http.Request) { s.saveAd(w, r, r.PathValue("id")) }))
}
