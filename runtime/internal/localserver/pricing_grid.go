package localserver

import (
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"net/http"
)

func (s *Server) registerPricingGrid(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/pricing/grid/{scope}", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("scope")
		if id == "all" {
			id = ""
		}
		book, err := s.pricing.GridBook(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		shared, err := s.pricing.GridBook(r.Context(), "")
		if err != nil {
			ownerError(w, err)
			return
		}
		fleet, err := printers.Eligible(r.Context(), s.db, "", "")
		if err != nil {
			ownerError(w, err)
			return
		}
		list, err := s.printers.List(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		overrides := map[string]pricing.Book{}
		for _, printer := range fleet {
			rates, err := s.pricing.GridBook(r.Context(), printer.ID)
			if err != nil {
				ownerError(w, err)
				return
			}
			if len(rates.Entries) > 0 {
				overrides[printer.ID] = rates
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"book": book, "shared": shared, "fleet": fleet, "printers": list, "overrides": overrides})
	}))
	mux.HandleFunc("PUT /api/v1/owner/pricing/grid/{scope}", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input pricing.Input
		if !decodeOwnerJSON(w, r, &input, pricingBodyLimit) {
			return
		}
		id := r.PathValue("scope")
		if id == "all" {
			id = ""
		}
		book, err := s.pricing.SaveGrid(r.Context(), id, input)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(book)
	}))
}
