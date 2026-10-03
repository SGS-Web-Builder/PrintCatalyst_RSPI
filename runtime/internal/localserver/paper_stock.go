package localserver

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// Completed jobs only. Duplex and N-up consume physical sheets, not image/page count.
const paperUsageQuery = `SELECT p.id AS printer_id,l.id AS line_id,(((CASE WHEN json_type(l.selected_pages_json)='array' THEN json_array_length(l.selected_pages_json) ELSE l.page_range_end-l.page_range_start+1 END)+l.pages_per_sheet*(CASE WHEN l.sides='one-sided' THEN 1 ELSE 2 END)-1)/(l.pages_per_sheet*(CASE WHEN l.sides='one-sided' THEN 1 ELSE 2 END)))*l.copies
 FROM printers p JOIN print_submissions j ON j.queue_name=p.queue_name JOIN order_lines l ON l.id=j.line_id JOIN orders o ON o.id=l.order_id
 WHERE j.state='submitted' AND (j.progress='completed' OR o.status='completed')
 UNION ALL SELECT p.id,'invoice:'||i.order_id||':'||i.queue_name,i.sheets FROM separator_invoices i JOIN printers p ON p.queue_name=i.queue_name JOIN orders o ON o.id=i.order_id WHERE i.state='submitted' AND (i.progress='completed' OR o.status='completed')`

type paperStockView struct {
	PrinterID string `json:"printerId"`
	Name      string `json:"name"`
	Tracked   bool   `json:"tracked"`
	Inserted  int64  `json:"inserted"`
	Printed   int64  `json:"printed"`
	Remaining int64  `json:"remaining"`
}

func syncPaperUsage(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO paper_usage(printer_id,line_id,sheets) SELECT u.* FROM (`+paperUsageQuery+`) u JOIN paper_stock s ON s.printer_id=u.printer_id`)
	return err
}
func (s *Server) readPaperStock(ctx context.Context) ([]paperStockView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = syncPaperUsage(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,COALESCE(NULLIF(p.display_name,''),p.queue_name),s.printer_id IS NOT NULL,COALESCE((SELECT SUM(sheets) FROM paper_loads WHERE printer_id=p.id),0),COALESCE((SELECT SUM(sheets) FROM paper_usage WHERE printer_id=p.id),0) FROM printers p LEFT JOIN paper_stock s ON s.printer_id=p.id WHERE p.removed_at IS NULL ORDER BY p.display_name,p.queue_name`)
	if err != nil {
		return nil, err
	}
	result := []paperStockView{}
	for rows.Next() {
		var v paperStockView
		if err = rows.Scan(&v.PrinterID, &v.Name, &v.Tracked, &v.Inserted, &v.Printed); err != nil {
			rows.Close()
			return nil, err
		}
		v.Remaining = v.Inserted - v.Printed
		result = append(result, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
func (s *Server) registerPaperStock(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/paper-stock", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		v, err := s.readPaperStock(r.Context())
		if err != nil {
			http.Error(w, "Could not load paper stock", 500)
			return
		}
		writeJSON(w, v)
	}))
	mux.HandleFunc("POST /api/v1/owner/paper-stock", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			PrinterID string `json:"printerId"`
			Sheets    int    `json:"sheets"`
			RequestID string `json:"requestId"`
		}
		if !decodeOwnerJSON(w, r, &input, 2048) {
			return
		}
		if input.Sheets < 1 || input.Sheets > 1000000 || len(input.RequestID) < 16 || len(input.RequestID) > 80 {
			http.Error(w, "Enter 1–1,000,000 sheets", 400)
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "Stock unavailable", 500)
			return
		}
		defer tx.Rollback()
		var exists bool
		if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM printers WHERE id=? AND removed_at IS NULL)", input.PrinterID).Scan(&exists); err != nil || !exists {
			http.Error(w, "Select an available printer", 400)
			return
		}
		var priorPrinter string
		var priorSheets int
		err = tx.QueryRowContext(r.Context(), "SELECT printer_id,sheets FROM paper_loads WHERE id=?", input.RequestID).Scan(&priorPrinter, &priorSheets)
		if err == nil {
			if priorPrinter != input.PrinterID || priorSheets != input.Sheets {
				http.Error(w, "This submission was already used", 409)
				return
			}
			writeJSON(w, map[string]bool{"saved": true})
			return
		}
		if err != sql.ErrNoRows {
			http.Error(w, "Could not check stock submission", 500)
			return
		}
		res, err := tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO paper_stock(printer_id) VALUES(?)", input.PrinterID)
		if err != nil {
			http.Error(w, "Could not start tracking", 500)
			return
		}
		count, _ := res.RowsAffected()
		if count == 1 {
			_, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO paper_usage(printer_id,line_id,sheets) SELECT u.printer_id,u.line_id,0 FROM (`+paperUsageQuery+`) u WHERE u.printer_id=?`, input.PrinterID)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO paper_loads(id,printer_id,sheets,created_at) VALUES(?,?,?,?)", input.RequestID, input.PrinterID, input.Sheets, time.Now().Unix())
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			http.Error(w, "Could not save paper stock", 500)
			return
		}
		writeJSON(w, map[string]bool{"saved": true})
	}))
}
