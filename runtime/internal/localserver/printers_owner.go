package localserver

import (
	"encoding/json"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

// registerOwnerPrinters wires the owner-facing printer CRUD endpoints.
func (s *Server) registerOwnerPrinters(mux *http.ServeMux) {
	s.registerPrinterSetup(mux)
	s.registerPrinterInvoices(mux)
	// GET /api/v1/owner/printers — list every discovered printer
	mux.HandleFunc("GET /api/v1/owner/printers", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		list, err := s.printers.List(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"printers": list})
	}))

	// GET /api/v1/owner/printers/{id} — single printer detail
	mux.HandleFunc("GET /api/v1/owner/printers/{id}", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" || !hexIDPattern.MatchString(id) {
			http.Error(w, "printer id must be a valid hex identifier", 400)
			return
		}
		p, err := s.printers.Get(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	}))

	// POST /api/v1/owner/printers — register / re-discover a printer (owner only).
	// Used both by the discovery layer (with normalized attributes already
	// computed) and by manual URI enrollment (where the server probes the
	// endpoint on the merchant's behalf).
	mux.HandleFunc("POST /api/v1/owner/printers", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Backend       printers.Backend    `json:"backend"`
			QueueName     string              `json:"queueName"`
			DisplayName   string              `json:"displayName"`
			DriverName    string              `json:"driverName"`
			DriverVersion string              `json:"driverVersion"`
			URI           string              `json:"uri"`
			Location      string              `json:"location"`
			IsDefault     bool                `json:"isDefault"`
			Attributes    map[string][]string `json:"attributes"`
		}
		if !decodeOwnerJSON(w, r, &input, 64<<10) {
			return
		}
		snap := printers.NormalizeIPPAttributes(printers.RawAttributes(input.Attributes))
		p, err := s.printers.Register(r.Context(), printers.RegisterInput{
			Backend:       input.Backend,
			QueueName:     input.QueueName,
			DisplayName:   input.DisplayName,
			DriverName:    input.DriverName,
			DriverVersion: input.DriverVersion,
			URI:           input.URI,
			Location:      input.Location,
			IsDefault:     input.IsDefault,
			Capabilities:  snap,
		})
		if err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(p)
	}))

	// PUT /api/v1/owner/printers/{id}/enable — owner marks a printer customer-visible
	mux.HandleFunc("PUT /api/v1/owner/printers/{id}/enable", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" || !hexIDPattern.MatchString(id) {
			http.Error(w, "printer id must be a valid hex identifier", 400)
			return
		}
		if err := s.printers.Enable(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": true})
	}))

	// PUT /api/v1/owner/printers/{id}/disable — owner marks a printer hidden
	mux.HandleFunc("PUT /api/v1/owner/printers/{id}/disable", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" || !hexIDPattern.MatchString(id) {
			http.Error(w, "printer id must be a valid hex identifier", 400)
			return
		}
		if err := s.printers.Disable(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": false})
	}))

	// DELETE /api/v1/owner/printers/{id} — soft-remove a printer
	mux.HandleFunc("DELETE /api/v1/owner/printers/{id}", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" || !hexIDPattern.MatchString(id) {
			http.Error(w, "printer id must be a valid hex identifier", 400)
			return
		}
		if err := s.printers.Remove(r.Context(), id); err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(204)
	}))

	// GET /api/v1/owner/printers/{id}/verifications — list verification rows
	mux.HandleFunc("GET /api/v1/owner/printers/{id}/verifications", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" || !hexIDPattern.MatchString(id) {
			http.Error(w, "printer id must be a valid hex identifier", 400)
			return
		}
		verifs, err := s.printers.ListVerifications(r.Context(), id)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"verifications": verifs})
	}))

	// POST /api/v1/owner/printers/{id}/verifications — record a new verification
	mux.HandleFunc("POST /api/v1/owner/printers/{id}/verifications", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" || !hexIDPattern.MatchString(id) {
			http.Error(w, "printer id must be a valid hex identifier", 400)
			return
		}
		var input struct {
			CapabilityType string `json:"capabilityType"`
			CapabilityKey  string `json:"capabilityKey"`
			Status         string `json:"status"`
			Evidence       string `json:"evidence"`
		}
		if !decodeOwnerJSON(w, r, &input, 16<<10) {
			return
		}
		actor, _ := s.subjectName(w, r)
		err := s.printers.RecordVerification(r.Context(), printers.Verification{
			PrinterID:      id,
			CapabilityType: input.CapabilityType,
			CapabilityKey:  input.CapabilityKey,
			Status:         printers.VerificationStatus(input.Status),
			Evidence:       input.Evidence,
			TestedAt:       NowFunc(),
			VerifiedAt:     NowFunc(),
			VerifiedBy:     actor,
		})
		if err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]bool{"recorded": true})
	}))
}

// NowFunc is an alias for printers.Now so the localserver package does not
// have to depend on printers.Now directly (it's a package-level var).
func NowFunc() int64 {
	return printers.Now()
}

// ownerRequireCanEditPrinter is a no-op placeholder kept for parity with
// other owner subcommands. Printer management is owner-only for now; future
// phases may grant operators a read-only view.
func ownerRequireCanEditPrinter(w http.ResponseWriter, subject owner.Subject) bool {
	return owner.RequirePermission(w, subject, owner.CanEditBusiness)
}
