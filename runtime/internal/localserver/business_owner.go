package localserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
)

// WithBusiness wires the business settings service. The owner dashboard's
// "Business" panel reads from it.
func WithBusiness(svc *business.Service) Option {
	return func(s *Server) { s.business = svc }
}

func (s *Server) registerOwnerBusiness(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/owner/business/settings", s.handleOwnerBusinessSettings)
	mux.HandleFunc("/api/v1/owner/business/services", s.handleOwnerBusinessServices)
	mux.HandleFunc("/api/v1/owner/business/services/{id}", s.handleOwnerBusinessServiceItem)
	mux.HandleFunc("/api/v1/owner/business/discounts", s.handleOwnerBusinessDiscounts)
	mux.HandleFunc("/api/v1/owner/business/discounts/{id}", s.handleOwnerBusinessDiscountItem)
}

func (s *Server) handleOwnerBusinessSettings(w http.ResponseWriter, r *http.Request) {
	if s.business == nil {
		http.Error(w, "business service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
			settings, err := s.business.Get(r.Context())
			if err != nil {
				businessError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(settings)
		})(w, r)
	case http.MethodPut:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			var input business.UpdateSettingsInput
			if !decodeOwnerJSON(w, r, &input, 16<<10) {
				return
			}
			settings, err := s.business.UpdateSettings(r.Context(), input)
			if err != nil {
				businessError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(settings)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerBusinessServices(w http.ResponseWriter, r *http.Request) {
	if s.business == nil {
		http.Error(w, "business service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
			services, err := s.business.ListServices(r.Context())
			if err != nil {
				businessError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"services": services})
		})(w, r)
	case http.MethodPost:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			var input business.CreateServiceInput
			if !decodeOwnerJSON(w, r, &input, 16<<10) {
				return
			}
			svc, err := s.business.CreateService(r.Context(), input)
			if err != nil {
				businessError(w, err)
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(svc)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerBusinessServiceItem(w http.ResponseWriter, r *http.Request) {
	if s.business == nil {
		http.Error(w, "business service unavailable", 503)
		return
	}
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodPut:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			var input business.UpdateServiceInput
			if !decodeOwnerJSON(w, r, &input, 16<<10) {
				return
			}
			svc, err := s.business.UpdateService(r.Context(), id, input)
			if err != nil {
				businessError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(svc)
		})(w, r)
	case http.MethodDelete:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			if err := s.business.DeleteService(r.Context(), id); err != nil {
				businessError(w, err)
				return
			}
			w.WriteHeader(204)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerBusinessDiscounts(w http.ResponseWriter, r *http.Request) {
	if s.business == nil {
		http.Error(w, "business service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
			discounts, err := s.business.ListDiscounts(r.Context())
			if err != nil {
				businessError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"discounts": discounts})
		})(w, r)
	case http.MethodPost:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			var input business.CreateDiscountInput
			if !decodeOwnerJSON(w, r, &input, 16<<10) {
				return
			}
			discount, err := s.business.CreateDiscount(r.Context(), input)
			if err != nil {
				businessError(w, err)
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(discount)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerBusinessDiscountItem(w http.ResponseWriter, r *http.Request) {
	if s.business == nil {
		http.Error(w, "business service unavailable", 503)
		return
	}
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodPut:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			var input business.UpdateDiscountInput
			if !decodeOwnerJSON(w, r, &input, 16<<10) {
				return
			}
			discount, err := s.business.UpdateDiscount(r.Context(), id, input)
			if err != nil {
				businessError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(discount)
		})(w, r)
	case http.MethodDelete:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			if err := s.business.DeleteDiscount(r.Context(), id); err != nil {
				businessError(w, err)
				return
			}
			w.WriteHeader(204)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// businessError maps the business package sentinels to HTTP status
// codes. The handler is small and unambiguous on purpose: the dashboard
// reads the JSON body to surface a clear inline error.
func businessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, business.ErrInvalid):
		http.Error(w, err.Error(), 400)
	case errors.Is(err, business.ErrDuplicate):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, business.ErrNotFound):
		http.Error(w, err.Error(), 404)
	case errors.Is(err, business.ErrTransition):
		http.Error(w, err.Error(), 409)
	default:
		http.Error(w, err.Error(), 500)
	}
}
