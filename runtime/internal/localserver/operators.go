package localserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
)

// operatorBodyLimit covers the new-operator request payload (username +
// password) and is small because no large fields are involved. A separate
// constant leaves room for tightening without touching the shared owner
// body limit, which is sized for the much larger business profile and
// pricing requests.
const operatorBodyLimit = 16 << 10

// operatorView is the persisted operator row plus its enabled flag and
// last-login timestamp. The password hash is never returned to the client —
// the dashboard only ever needs to display the username and the enable
// toggle, and a leaked hash would let an attacker with read access to the
// loopback API offline-brute-force the password offline.
type operatorView struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	LastLoginAt int64  `json:"lastLoginAt"`
}

func newOperatorView(op owner.Operator) operatorView {
	return operatorView{
		ID:          op.ID,
		Username:    op.Username,
		Role:        string(op.Role),
		Enabled:     op.Enabled,
		CreatedAt:   op.CreatedAt,
		UpdatedAt:   op.UpdatedAt,
		LastLoginAt: op.LastLoginAt,
	}
}

// registerOperators wires the operator CRUD endpoints under the same
// loopback + CSRF guard as the rest of the owner surface. Every route here
// requires CanManageOperators: the only role that may add, disable, re-enable
// or remove operator accounts is the owner.
func (s *Server) registerOperators(mux *http.ServeMux) {
	protect := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.localOwnerRequest(w, r) {
				return
			}
			if s.owner == nil {
				http.Error(w, "owner service unavailable", 503)
				return
			}
			_, subject, ok := s.ownerSession(w, r)
			if !ok {
				return
			}
			if !owner.RequirePermission(w, subject, owner.CanManageOperators) {
				return
			}
			handler(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/owner/operators", protect(func(w http.ResponseWriter, r *http.Request) {
		ops, err := s.owner.ListOperators(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		views := make([]operatorView, 0, len(ops))
		for _, op := range ops {
			views = append(views, newOperatorView(op))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"operators": views})
	}))
	mux.HandleFunc("POST /api/v1/owner/operators", protect(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeOwnerJSON(w, r, &input, operatorBodyLimit) {
			return
		}
		input.Username = strings.ToLower(strings.TrimSpace(input.Username))
		if err := s.owner.CreateOperator(r.Context(), input.Username, input.Password); err != nil {
			ownerError(w, err)
			return
		}
		ops, err := s.owner.ListOperators(r.Context())
		if err != nil || len(ops) == 0 {
			ownerError(w, err)
			return
		}
		// Find the row we just inserted to echo back. ListOperators orders by
		// username so we can match by case-insensitive comparison.
		for _, op := range ops {
			if strings.EqualFold(op.Username, input.Username) {
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(newOperatorView(op))
				return
			}
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]bool{"created": true})
	}))
	mux.HandleFunc("PATCH /api/v1/owner/operators/{id}", protect(func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseOperatorID(w, r)
		if !ok {
			return
		}
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeOwnerJSON(w, r, &input, operatorBodyLimit) {
			return
		}
		if input.Enabled == nil {
			http.Error(w, "enabled flag is required", 400)
			return
		}
		if err := s.owner.SetOperatorEnabled(r.Context(), id, *input.Enabled); err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"updated": true})
	}))
	mux.HandleFunc("DELETE /api/v1/owner/operators/{id}", protect(func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseOperatorID(w, r)
		if !ok {
			return
		}
		if err := s.owner.DeleteOperator(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
	}))
}

// parseOperatorID extracts the {id} path parameter and validates it is a
// positive integer. Returning the boolean keeps the handler concise: callers
// can `if !ok { return }` after writing the 400 response here.
func parseOperatorID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		http.Error(w, "operator id is required", 400)
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "operator id must be a positive integer", 400)
		return 0, false
	}
	return id, true
}