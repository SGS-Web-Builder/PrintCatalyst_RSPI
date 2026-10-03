package localserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pairing"
)

// WithPairing wires the pairing service. Required for the
// /api/v1/owner/pairing and /api/v1/pair/* endpoints; safe to omit in
// builds that disable the companion applications (the routes silently
// 503).
func WithPairing(svc *pairing.Service) Option {
	return func(s *Server) { s.pairing = svc }
}

// registerOwnerPairing wires the owner-side pairing endpoints onto the
// owner mux.
func (s *Server) registerOwnerPairing(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/owner/pairing", s.handleOwnerPairingCollection)
	mux.HandleFunc("/api/v1/owner/pairing/initiate", s.handleOwnerPairingInitiate)
	mux.HandleFunc("/api/v1/owner/pairing/revoke", s.handleOwnerPairingRevoke)
}

// registerPublicPairing wires the public-facing pairing endpoints
// onto the root mux. The exchange endpoint is unauthenticated by
// design — the merchant is still holding the phone, and the bearer
// token the exchange returns is what gates subsequent calls.
func (s *Server) registerPublicPairing(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/pair/exchange", s.handlePublicPairExchange)
}

func (s *Server) handleOwnerPairingCollection(w http.ResponseWriter, r *http.Request) {
	if s.pairing == nil {
		http.Error(w, "pairing service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
			status, err := s.pairing.List(r.Context())
			if err != nil {
				pairingOwnerError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(status)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

type pairingInitiateRequest struct {
	DeepLinkTemplate string `json:"deepLinkTemplate"`
}

func (s *Server) handleOwnerPairingInitiate(w http.ResponseWriter, r *http.Request) {
	if s.pairing == nil {
		http.Error(w, "pairing service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var request pairingInitiateRequest
		// The body is optional; a missing or empty body means the
		// server should use the default deep-link template.
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
				http.Error(w, "invalid JSON request", 400)
				return
			}
		}
		code, err := s.pairing.Initiate(r.Context(), request.DeepLinkTemplate)
		if err != nil {
			pairingOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(code)
	})(w, r)
}

type pairingRevokeRequest struct {
	DeviceID string `json:"deviceId"`
	Reason   string `json:"reason"`
}

func (s *Server) handleOwnerPairingRevoke(w http.ResponseWriter, r *http.Request) {
	if s.pairing == nil {
		http.Error(w, "pairing service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var request pairingRevokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid JSON request", 400)
			return
		}
		if request.Reason == "" {
			http.Error(w, "revocation reason is required", 400)
			return
		}
		if err := s.pairing.Revoke(r.Context(), request.DeviceID, request.Reason); err != nil {
			pairingOwnerError(w, err)
			return
		}
		status, err := s.pairing.List(r.Context())
		if err != nil {
			pairingOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})(w, r)
}

type pairingExchangeRequest struct {
	Code        string `json:"code"`
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
}

func (s *Server) handlePublicPairExchange(w http.ResponseWriter, r *http.Request) {
	if s.pairing == nil {
		http.Error(w, "pairing service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var request pairingExchangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	result, err := s.pairing.Exchange(r.Context(), pairing.ExchangeInput{
		Code:        request.Code,
		Fingerprint: request.Fingerprint,
		Label:       request.Label,
	})
	if err != nil {
		pairingOwnerError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

// pairingOwnerError maps sentinel errors from the pairing package to
// HTTP status codes. The mapping mirrors licensingOwnerError so the
// dashboard can use a single client-side error decoder.
func pairingOwnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pairing.ErrInvalid):
		http.Error(w, err.Error(), 400)
	case errors.Is(err, pairing.ErrUnconfigured):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, pairing.ErrSignature):
		http.Error(w, err.Error(), 401)
	case errors.Is(err, pairing.ErrMismatch):
		http.Error(w, err.Error(), 422)
	case errors.Is(err, pairing.ErrExpired):
		http.Error(w, err.Error(), 410)
	case errors.Is(err, pairing.ErrRevoked):
		http.Error(w, err.Error(), 423)
	case errors.Is(err, pairing.ErrRateLimit):
		http.Error(w, err.Error(), 429)
	default:
		http.Error(w, err.Error(), 500)
	}
}
