package localserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// registerOwnerReports wires the owner-facing reports endpoints.
func (s *Server) registerOwnerReports(mux *http.ServeMux) {
	protect := s.protectOwnerView

	// GET /api/v1/owner/reports/summary?from=<unix>&to=<unix>
	// Returns aggregated order stats for the inclusive range [from, to].
	mux.HandleFunc("GET /api/v1/owner/reports/summary", protect(func(w http.ResponseWriter, r *http.Request) {
		from := parseUnixParam(r, "from", thirtyDaysAgo())
		to := parseUnixParam(r, "to", int64(time.Now().Unix()))
		summary, err := s.reports.Summary(r.Context(), from, to)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(summary)
	}))

	// GET /api/v1/owner/reports/top-combinations?from=<unix>&to=<unix>&limit=<n>
	// Returns the most-ordered print configurations in the range.
	mux.HandleFunc("GET /api/v1/owner/reports/top-combinations", protect(func(w http.ResponseWriter, r *http.Request) {
		from := parseUnixParam(r, "from", thirtyDaysAgo())
		to := parseUnixParam(r, "to", int64(time.Now().Unix()))
		limit := parseIntParam(r, "limit", 10)
		combos, err := s.reports.TopCombinations(r.Context(), from, to, limit)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"combinations": combos})
	}))
}

func thirtyDaysAgo() int64 {
	return time.Now().AddDate(0, 0, -30).Unix()
}

func parseUnixParam(r *http.Request, key string, fallback int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func parseIntParam(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
