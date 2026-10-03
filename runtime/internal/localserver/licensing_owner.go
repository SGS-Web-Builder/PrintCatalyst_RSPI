package localserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
)

// WithLicensing wires the licensing service. Required for the
// /api/v1/owner/licence endpoints; safe to omit in builds that
// disable licensing (the routes silently 503).
func WithLicensing(svc *licensing.Service) Option {
	return func(s *Server) { s.licensing = svc }
}

// registerOwnerLicence wires the licence endpoints onto the owner mux.
func (s *Server) registerOwnerLicence(mux *http.ServeMux) {
	if s.licenseGate != nil {
		s.registerSoftwareLicense(mux)
		return
	}
	mux.HandleFunc("/api/v1/owner/licence", s.handleOwnerLicenceCollection)
	mux.HandleFunc("/api/v1/owner/licence/activate", s.handleOwnerLicenceActivate)
	mux.HandleFunc("/api/v1/owner/licence/refresh", s.handleOwnerLicenceRefresh)
	mux.HandleFunc("/api/v1/owner/licence/revoke", s.handleOwnerLicenceRevoke)
	mux.HandleFunc("/api/v1/owner/licence/transfer", s.handleOwnerLicenceTransfer)
	mux.HandleFunc("/api/v1/owner/licence/events", s.handleOwnerLicenceEvents)
}

func (s *Server) handleOwnerLicenceCollection(w http.ResponseWriter, r *http.Request) {
	if s.licensing == nil {
		http.Error(w, "licence service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(s.handleOwnerLicenceStatus)(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerLicenceStatus(w http.ResponseWriter, r *http.Request) {
	// Ensure the device key pair exists so the dashboard always sees an
	// installationId; the activation form renders for the unconfigured
	// case but the merchant still needs the id to identify the machine.
	if _, err := s.licensing.Ensure(r.Context()); err != nil {
		licensingOwnerError(w, err)
		return
	}
	status, err := s.licensing.Status(r.Context())
	if err != nil {
		licensingOwnerError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(status)
}

// requireOwnerOrSetupToken guards the licence bootstrap endpoint. On a
// fresh installation no owner session exists yet, so the licence gate
// cannot be completed through an owner-gated handler — that would create
// the "activate licence requires owner, owner requires licence" deadlock
// the rest of this package is designed to avoid. Instead, the handler
// accepts either:
//   - an active owner session (the normal post-bootstrap flow), or
//   - the installation setup token (the first-time bootstrap flow, when no
//     owner row exists yet).
//
// Once any owner row is persisted the setup-token path is rejected so a
// later compromised installation cannot use the token to re-activate a
// licence behind the owner's back.
func (s *Server) requireOwnerOrSetupToken(w http.ResponseWriter, r *http.Request) bool {
	if !s.localOwnerRequest(w, r) {
		return false
	}
	if s.owner == nil {
		http.Error(w, "owner service unavailable", 503)
		return false
	}
	exists, err := s.owner.Exists(r.Context())
	if err != nil {
		ownerError(w, err)
		return false
	}
	if exists {
		// Owner exists — behave exactly like protectOwnerView so an operator
		// cannot bypass the role gate by going through the licence endpoint.
		_, _, ok := s.ownerSession(w, r)
		if !ok {
			return false
		}
		return true
	}
	// No owner yet — the only thing that proves the request originates from
	// the operator sitting at the machine is the setup token.
	token := r.Header.Get("X-Setup-Token")
	if token == "" {
		http.Error(w, "installation setup token required for first-time licence activation", 403)
		return false
	}
	if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) != 1 {
		http.Error(w, "invalid installation setup token", 403)
		return false
	}
	return true
}

func (s *Server) handleOwnerLicenceActivate(w http.ResponseWriter, r *http.Request) {
	if s.licensing == nil {
		http.Error(w, "licence service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.requireOwnerOrSetupToken(w, r) {
		return
	}
	license, err := s.licensing.Activate(r.Context())
	if err != nil {
		licensingOwnerError(w, err)
		return
	}
	// Complete the licence provisioning gate inside the same
	// handler so the activation round trip also opens the next
	// setup step. The gate is idempotent, so a duplicate call
	// after a successful activation is a no-op.
	if s.provisioning != nil {
		if state, ok := s.provisioning.(interface {
			CompleteGate(context.Context, provisioning.Gate, string) error
		}); ok {
			_ = state.CompleteGate(r.Context(), provisioning.GateLicence, "signed entitlement verified")
		}
	}
	_ = json.NewEncoder(w).Encode(license)
}

func (s *Server) handleOwnerLicenceRefresh(w http.ResponseWriter, r *http.Request) {
	if s.licensing == nil {
		http.Error(w, "licence service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if err := s.licensing.Refresh(r.Context()); err != nil {
			licensingOwnerError(w, err)
			return
		}
		status, err := s.licensing.Status(r.Context())
		if err != nil {
			licensingOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})(w, r)
}

type licensingRevokeRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handleOwnerLicenceRevoke(w http.ResponseWriter, r *http.Request) {
	if s.licensing == nil {
		http.Error(w, "licence service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var request licensingRevokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid JSON request", 400)
			return
		}
		if err := s.licensing.Revoke(r.Context(), request.Reason); err != nil {
			licensingOwnerError(w, err)
			return
		}
		status, err := s.licensing.Status(r.Context())
		if err != nil {
			licensingOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})(w, r)
}

func (s *Server) handleOwnerLicenceTransfer(w http.ResponseWriter, r *http.Request) {
	if s.licensing == nil {
		http.Error(w, "licence service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if err := s.licensing.Transfer(r.Context()); err != nil {
			licensingOwnerError(w, err)
			return
		}
		status, err := s.licensing.Status(r.Context())
		if err != nil {
			licensingOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})(w, r)
}

func (s *Server) handleOwnerLicenceEvents(w http.ResponseWriter, r *http.Request) {
	if s.licensing == nil {
		http.Error(w, "licence service unavailable", 503)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := parseLimit(raw)
			if err != nil {
				http.Error(w, "invalid limit", 400)
				return
			}
			limit = parsed
		}
		events, err := s.licensing.Events(r.Context(), limit)
		if err != nil {
			licensingOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(events)
	})(w, r)
}

// licensingOwnerError maps sentinel errors to HTTP status codes. The
// mapping mirrors ownerError; it is split into its own function so
// the licensing endpoints never accidentally regress to a generic 500.
func licensingOwnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, licensing.ErrInvalid):
		http.Error(w, err.Error(), 400)
	case errors.Is(err, licensing.ErrUnconfigured):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, licensing.ErrSignature), errors.Is(err, licensing.ErrMismatch), errors.Is(err, licensing.ErrExpired), errors.Is(err, licensing.ErrRevoked), errors.Is(err, licensing.ErrTransfer), errors.Is(err, licensing.ErrClockRollback):
		http.Error(w, err.Error(), 422)
	default:
		http.Error(w, err.Error(), 500)
	}
}

// parseLimit is a tiny helper that decodes a positive decimal query
// parameter without pulling in strconv at the call site.
func parseLimit(raw string) (int, error) {
	value := 0
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, errors.New("not a number")
		}
		value = value*10 + int(char-'0')
	}
	return value, nil
}
