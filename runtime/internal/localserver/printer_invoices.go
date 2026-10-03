package localserver

import (
	"database/sql"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"net/http"
)

type printerInvoiceSettings struct {
	Enabled   bool   `json:"enabled"`
	Threshold int    `json:"threshold"`
	Paper     string `json:"paper"`
	Tray      string `json:"tray"`
}

func (s *Server) registerPrinterInvoices(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/printers/{id}/invoice-settings", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.printers.Get(r.Context(), r.PathValue("id")); err != nil {
			ownerError(w, err)
			return
		}
		v := printerInvoiceSettings{Threshold: 4}
		err := s.db.QueryRowContext(r.Context(), `SELECT enabled,threshold,paper,tray FROM printer_invoice_settings WHERE printer_id=?`, r.PathValue("id")).Scan(&v.Enabled, &v.Threshold, &v.Paper, &v.Tray)
		if err != nil && err != sql.ErrNoRows {
			ownerError(w, err)
			return
		}
		writeJSON(w, v)
	}))
	mux.HandleFunc("PUT /api/v1/owner/printers/{id}/invoice-settings", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var v printerInvoiceSettings
		if !decodeOwnerJSON(w, r, &v, 4096) {
			return
		}
		p, err := s.printers.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			ownerError(w, err)
			return
		}
		if v.Threshold < 0 || v.Threshold > 10000 {
			http.Error(w, "Enter a whole-number threshold from 0 to 10,000", 400)
			return
		}
		if v.Enabled {
			paperOK, trayOK := false, false
			if p.Capabilities != nil {
				for _, size := range p.Capabilities.PaperSizes {
					if size.Key == v.Paper {
						paperOK = true
					}
				}
				for _, tray := range p.Capabilities.Trays {
					if tray.RawLabel == v.Tray && v.Tray != "" {
						trayOK = true
					}
				}
			}
			if p.Backend != printers.BackendWindows || !paperOK || !trayOK {
				http.Error(w, "Choose a paper size and tray reported by this Windows printer", 400)
				return
			}
		}
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO printer_invoice_settings(printer_id,enabled,threshold,paper,tray) VALUES(?,?,?,?,?) ON CONFLICT(printer_id) DO UPDATE SET enabled=excluded.enabled,threshold=excluded.threshold,paper=excluded.paper,tray=excluded.tray`, p.ID, v.Enabled, v.Threshold, v.Paper, v.Tray)
		if err != nil {
			ownerError(w, err)
			return
		}
		writeJSON(w, v)
	}))
}
