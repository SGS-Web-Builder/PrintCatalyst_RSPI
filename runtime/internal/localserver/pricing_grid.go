package localserver

import (
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"log"
	"net/http"
)

func (s *Server) registerPricingGrid(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/pricing/grid/{scope}", s.protectOwnerView(s.getPricingGrid))
	mux.HandleFunc("PUT /api/v1/owner/pricing/grid/{scope}", s.protectOwnerEdit(s.savePricingGrid))
}
func (s *Server) getPricingGrid(w http.ResponseWriter, r *http.Request) {
	if s.pricing == nil || s.printers == nil {
		http.Error(w, "Pricing unavailable", 503)
		return
	}

	id := r.PathValue("scope")
	if id == "all" {
		id = ""
	}
	revision, err := s.pricing.Revision(r.Context())
	if err != nil {
		ownerError(w, err)
		return
	}
	book, err := s.pricing.GridBook(r.Context(), id)
	if err != nil {
		log.Printf("pricing grid: load selected book: %v", err)
		ownerError(w, err)
		return
	}
	shared, err := s.pricing.GridBook(r.Context(), "")
	if err != nil {
		log.Printf("pricing grid: load shared book: %v", err)
		ownerError(w, err)
		return
	}
	fleet, err := printers.Eligible(r.Context(), s.db, "", "")
	if err != nil {
		log.Printf("pricing grid: load eligible printers: %v", err)
		ownerError(w, err)
		return
	}
	list, err := s.printers.List(r.Context())
	if err != nil {
		log.Printf("pricing grid: list printers: %v", err)
		ownerError(w, err)
		return
	}
	overrides := map[string]pricing.Book{}
	for _, printer := range fleet {
		rates, err := s.pricing.GridBook(r.Context(), printer.ID)
		if err != nil {
			log.Printf("pricing grid: load printer overrides: %v", err)
			ownerError(w, err)
			return
		}
		if len(rates.Entries) > 0 {
			overrides[printer.ID] = rates
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"revision": revision, "book": book, "shared": shared, "fleet": fleet, "printers": list, "overrides": overrides})
}
func (s *Server) savePricingGrid(w http.ResponseWriter, r *http.Request) {
	if s.pricing == nil {
		http.Error(w, "Pricing unavailable", 503)
		return
	}

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
}
