package localserver

import (
	"encoding/json"
	"net/http"
)

func (s *Server) registerPrinterSetup(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/owner/printers/{id}/paper", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Paper   string `json:"paper"`
			Enabled bool   `json:"enabled"`
		}
		if !decodeOwnerJSON(w, r, &input, 4096) {
			return
		}
		actor, _ := s.subjectName(w, r)
		if err := s.printers.SetPaperEnabled(r.Context(), r.PathValue("id"), input.Paper, input.Enabled, actor); err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": input.Enabled})
	}))
	mux.HandleFunc("POST /api/v1/owner/printers/{id}/test-print", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Paper string `json:"paper"`
		}
		if !decodeOwnerJSON(w, r, &input, 4096) {
			return
		}
		p, err := s.printers.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			ownerError(w, err)
			return
		}
		if s.dispatcher == nil {
			http.Error(w, "Printer service unavailable", 503)
			return
		}
		if p.Backend != "windows" {
			http.Error(w, "Install this printer in Windows to test it from this application", 409)
			return
		}
		if p.Capabilities != nil {
			for _, paper := range p.Capabilities.PaperSizes {
				if paper.Key == input.Paper {
					result, err := s.dispatcher.TestPrintOn(r.Context(), p.QueueName, paper.Key, paper.WidthMM, paper.HeightMM)
					if err != nil {
						http.Error(w, err.Error(), 502)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"queue": result.Queue, "jobId": result.JobID})
					return
				}
			}
		}
		http.Error(w, "Choose a paper size reported by this printer", 400)
	}))
}
