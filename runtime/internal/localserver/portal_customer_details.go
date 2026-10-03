package localserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type customerDetailsPolicy struct {
	Enabled bool              `json:"enabled"`
	Fields  map[string]string `json:"fields"`
}

var detailNames = map[string]string{"customerName": "Name", "customerPhone": "Phone number", "customerEmail": "Email", "customerNotes": "Notes"}

func (s *Server) readCustomerDetails(ctx context.Context) (customerDetailsPolicy, error) {
	var p customerDetailsPolicy
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT settings FROM portal_customer_details WHERE singleton=1").Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	return p, err
}
func (p customerDetailsPolicy) apply(values map[string]*string) error {
	for key, label := range detailNames {
		value := values[key]
		if !p.Enabled || p.Fields[key] == "hidden" {
			*value = ""
			continue
		}
		*value = strings.TrimSpace(*value)
		if p.Fields[key] == "required" && *value == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	return nil
}
func (s *Server) registerCustomerDetails(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/customer-details", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.readCustomerDetails(r.Context())
		if err != nil {
			http.Error(w, "Could not load customer details settings", 500)
			return
		}
		writeJSON(w, p)
	}))
	mux.HandleFunc("PUT /api/v1/owner/customer-details", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var p customerDetailsPolicy
		if !decodeOwnerJSON(w, r, &p, 2048) {
			return
		}
		if len(p.Fields) != len(detailNames) {
			http.Error(w, "Choose a setting for each field", 400)
			return
		}
		for key := range detailNames {
			v := p.Fields[key]
			if v != "hidden" && v != "optional" && v != "required" {
				http.Error(w, "Choose Hidden, Optional or Required for each field", 400)
				return
			}
		}
		raw, _ := json.Marshal(p)
		if _, err := s.db.ExecContext(r.Context(), "UPDATE portal_customer_details SET settings=? WHERE singleton=1", string(raw)); err != nil {
			http.Error(w, "Could not save customer details settings", 500)
			return
		}
		writeJSON(w, p)
	}))
}
