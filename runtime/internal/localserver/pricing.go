package localserver

import (
	"encoding/json"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
)

// pricingBodyLimit covers a full merchant price book (MaxEntries rows, each
// with up to MaxTiersPerEntry tiers) with room for merchant-entered paper
// identifiers. The previous limit was sized for Phase 3B; tiers double the
// maximum request size, so the cap is raised accordingly.
const pricingBodyLimit = 256 << 10

func WithPricing(service *pricing.Service) Option {
	return func(server *Server) { server.pricing = service }
}

// Pricing endpoints reuse the owner guards: loopback peer, local Host, no
// forwarded headers, same-origin JSON writes, session cookie and CSRF token.
// Reading is permitted for both owner and operator sessions; writing is
// reserved for the owner only — operators need to quote prices to customers
// but changing the price book is the merchant's call.
func (s *Server) registerPricing(mux *http.ServeMux) {
	s.registerPricingGrid(mux)
	protect := func(permission owner.Permission) func(handler http.HandlerFunc) http.HandlerFunc {
		return func(handler http.HandlerFunc) http.HandlerFunc {
			return func(response http.ResponseWriter, request *http.Request) {
				if !s.localOwnerRequest(response, request) {
					return
				}
				if s.pricing == nil {
					http.Error(response, "pricing service unavailable", 503)
					return
				}
				_, subject, ok := s.ownerSession(response, request)
				if !ok {
					return
				}
				if !owner.RequirePermission(response, subject, permission) {
					return
				}
				handler(response, request)
			}
		}
	}
	mux.HandleFunc("GET /api/v1/owner/pricing", protect(owner.CanViewPricing)(func(response http.ResponseWriter, request *http.Request) {
		book, err := s.pricing.Load(request.Context())
		if err != nil {
			ownerError(response, err)
			return
		}
		_ = json.NewEncoder(response).Encode(book)
	}))
	mux.HandleFunc("PUT /api/v1/owner/pricing", protect(owner.CanEditPricing)(func(response http.ResponseWriter, request *http.Request) {
		var input pricing.Input
		if !decodeOwnerJSON(response, request, &input, pricingBodyLimit) {
			return
		}
		book, err := s.pricing.Save(request.Context(), input)
		if err != nil {
			ownerError(response, err)
			return
		}
		_ = json.NewEncoder(response).Encode(book)
	}))
}
