package localserver

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Draft identity is persisted by the browser before the first large upload.
// Retrying a lost upload response therefore reuses the same batch destination.
func (s *Server) registerPortalDocuments(mux *http.ServeMux) {
	s.registerPortalComposition(mux)
	mux.HandleFunc("DELETE /api/v1/portal/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			portalJSONError(w, "storage unavailable", 503)
			return
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(r.Context(), "UPDATE orders SET updated_at=updated_at WHERE id=? AND share_token=? AND submitted_at=0 AND status!='cancelled'", r.URL.Query().Get("orderId"), r.Header.Get("X-Upload-Token"))
		if err != nil {
			portalJSONError(w, "Could not remove document", 500)
			return
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			portalJSONError(w, "draft not found", 404)
			return
		}
		var path string
		err = tx.QueryRowContext(r.Context(), "SELECT storage_path FROM documents WHERE id=? AND order_id=?", r.PathValue("id"), r.URL.Query().Get("orderId")).Scan(&path)
		if err == sql.ErrNoRows {
			w.WriteHeader(204)
			return
		}
		if err != nil {
			portalJSONError(w, "Could not find document", 500)
			return
		}
		// Hold the draft write lock so checkout cannot claim this file during deletion.
		if err = documents.New(s.files, s.db).Delete(r.Context(), path); err != nil && !os.IsNotExist(err) {
			portalJSONError(w, "Could not delete the file. Please retry.", 500)
			return
		}
		if _, err = tx.ExecContext(r.Context(), "DELETE FROM documents WHERE id=? AND order_id=?", r.PathValue("id"), r.URL.Query().Get("orderId")); err != nil {
			portalJSONError(w, "Could not remove document. Please retry.", 500)
			return
		}
		if err = tx.Commit(); err != nil {
			portalJSONError(w, "Could not save document removal. Please retry.", 500)
			return
		}
		w.WriteHeader(204)
	})

	mux.HandleFunc("POST /api/v1/portal/drafts", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, true) {
			return
		}
		if reason := s.notReadyReason(r.Context()); reason != "" {
			portalJSONError(w, reason, 503)
			return
		}
		id, err := portalRandomID()
		if err != nil {
			portalJSONError(w, "create draft failed", 500)
			return
		}
		token, err := portalRandomID()
		if err != nil {
			portalJSONError(w, "create draft failed", 500)
			return
		}
		currency, mu, err := s.loadCurrency(r.Context())
		if err != nil {
			portalJSONError(w, "currency unavailable", 503)
			return
		}
		now := time.Now().Unix()
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,customer_email,customer_notes,created_at,updated_at,portal_gate,submitted_at) VALUES(?,?,'pending_payment',?,?,0,'','','','',?,?,0,0)`, id, token, currency, mu, now, now)
		if err != nil {
			portalJSONError(w, "create draft failed", 500)
			return
		}
		writeJSON(w, map[string]string{"orderId": id, "uploadToken": token})
	})
	mux.HandleFunc("GET /api/v1/portal/drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		var token, status, method, currency string
		var submitted, total int64
		var mu int
		err := s.db.QueryRowContext(r.Context(), "SELECT share_token,status,submitted_at,payment_method,total_minor,currency,currency_minor_units FROM orders WHERE id=?", r.PathValue("id")).Scan(&token, &status, &submitted, &method, &total, &currency, &mu)
		if err != nil || token != r.Header.Get("X-Upload-Token") || status == "cancelled" {
			portalJSONError(w, "draft not found", 404)
			return
		}
		rows, err := s.db.QueryContext(r.Context(), "SELECT id,original_filename,mime_type,size_bytes,page_count FROM documents WHERE order_id=? AND purged_at=0 ORDER BY created_at,id", r.PathValue("id"))
		if err != nil {
			portalJSONError(w, "draft unavailable", 503)
			return
		}
		files := []map[string]any{}
		for rows.Next() {
			var id, name, mime string
			var size int64
			var pages int
			if err = rows.Scan(&id, &name, &mime, &size, &pages); err != nil {
				break
			}
			files = append(files, map[string]any{"documentId": id, "originalFilename": name, "mimeType": mime, "sizeBytes": size, "pageCount": pages})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			portalJSONError(w, "draft unavailable", 503)
			return
		}
		writeJSON(w, map[string]any{"orderId": r.PathValue("id"), "uploadToken": token, "shareToken": token, "files": files, "submitted": submitted > 0, "status": status, "paymentMethod": method, "totalMinor": total, "currency": currency, "currencyMU": mu})
	})
	mux.HandleFunc("GET /api/v1/portal/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		var path, mime string
		var pageCount int
		err := s.db.QueryRowContext(r.Context(), `SELECT d.storage_path,d.mime_type,d.page_count FROM documents d JOIN orders o ON o.id=d.order_id WHERE d.id=? AND o.id=? AND o.share_token=? AND d.purged_at=0`, r.PathValue("id"), r.URL.Query().Get("orderId"), r.Header.Get("X-Upload-Token")).Scan(&path, &mime, &pageCount)
		if err != nil {
			portalJSONError(w, "document not found", 404)
			return
		}
		body, err := documents.New(s.files, s.db).FetchAt(r.Context(), path)
		if err != nil {
			portalJSONError(w, "document is no longer available", 404)
			return
		}

		if r.URL.Query().Get("preview") == "sheet" {
			q := r.URL.Query()
			start, e1 := strconv.Atoi(q.Get("from"))
			end, e2 := strconv.Atoi(q.Get("to"))
			n, e3 := strconv.Atoi(q.Get("pagesPerSheet"))
			side, e4 := strconv.Atoi(q.Get("side"))
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || start < 1 || end < start || end > pageCount || (n != 1 && n != 2 && n != 4) || side < 1 || side > (end-start+n)/n {
				portalJSONError(w, "invalid preview range", 400)
				return
			}
			var selected []int
			if raw := q.Get("pages"); raw != "" {
				selected, err = pageselection.Decode(raw)
				if err != nil {
					portalJSONError(w, "invalid selected pages", 400)
					return
				}
			}
			selected, err = pageselection.Resolve(selected, start, end, pageCount)
			if err != nil || side > (len(selected)+n-1)/n {
				portalJSONError(w, "invalid selected pages or sheet side", 400)
				return
			}
			orientation, colour, sides := q.Get("orientation"), q.Get("colourMode"), q.Get("sides")
			if (orientation != "auto" && orientation != "portrait" && orientation != "landscape") || (colour != "colour" && colour != "monochrome") || (sides != "one-sided" && sides != "two-sided-long-edge" && sides != "two-sided-short-edge") {
				portalJSONError(w, "invalid preview options", 400)
				return
			}
			paper := q.Get("paperSize")
			var width, height float64
			// Prefer the registered driver's dimensions, with standard paper
			// dimensions for legacy snapshots lacking physical measurements.
			_ = s.db.QueryRowContext(r.Context(), "SELECT width_mm,height_mm FROM printer_paper_sizes WHERE paper_key=? AND removed_at IS NULL AND width_mm>0 AND height_mm>0 LIMIT 1", paper).Scan(&width, &height)
			if width <= 0 || height <= 0 {
				sizes := map[string][2]float64{"A3": {297, 420}, "A4": {210, 297}, "A5": {148, 210}, "A6": {105, 148}, "Letter": {215.9, 279.4}, "Legal": {215.9, 355.6}}
				size, ok := sizes[paper]
				if !ok {
					portalJSONError(w, "paper dimensions are unavailable for this preview", 422)
					return
				}
				width, height = size[0], size[1]
			}
			body, err = dispatch.RenderSheetPreview(r.Context(), body, mime, dispatch.DocumentRef{Pages: selected, PaperSize: paper, Copies: 1, PageStart: start, PageEnd: end, PagesPerSheet: n, Orientation: orientation, ColourMode: colour, Sides: sides}, pageCount, side, width, height)
			if err != nil {
				portalJSONError(w, err.Error(), 503)
				return
			}
			mime = "image/png"
		}
		if r.URL.Query().Get("preview") == "1" && mime == "application/pdf" {
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 {
				portalJSONError(w, "invalid preview page", 400)
				return
			}
			body, err = dispatch.RenderPreview(r.Context(), body, page)
			if err != nil {
				portalJSONError(w, err.Error(), 503)
				return
			}
			mime = "image/png"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.ServeContent(w, r, "preview", time.Time{}, bytes.NewReader(body))
	})
	mux.HandleFunc("DELETE /api/v1/portal/drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, false) {
			return
		}
		// Mark cancelled inside the transaction before deleting files, preventing
		// a concurrent submit from claiming the discarded draft.
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			portalJSONError(w, "storage unavailable", 503)
			return
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(r.Context(), "UPDATE orders SET status='cancelled' WHERE id=? AND share_token=? AND submitted_at=0", r.PathValue("id"), r.Header.Get("X-Upload-Token"))
		if err != nil {
			portalJSONError(w, "discard failed", 500)
			return
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			portalJSONError(w, "draft not found", 404)
			return
		}
		if err = tx.Commit(); err != nil {
			portalJSONError(w, "discard failed", 500)
			return
		}
		if err = s.removeDraftFiles(r.Context(), r.PathValue("id")); err != nil {
			portalJSONError(w, "draft discarded; file cleanup will be retried", 500)
			return
		}
		w.WriteHeader(204)
	})
}

func (s *Server) removeDraftFiles(ctx context.Context, id string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT storage_path FROM documents WHERE order_id=? AND purged_at=0", id)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			break
		}
		paths = append(paths, path)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	docs := documents.New(s.files, s.db)
	for _, path := range paths {
		if err := docs.Delete(ctx, path); err != nil {
			return fmt.Errorf("remove draft file: %w", err)
		}
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM orders WHERE id=? AND submitted_at=0 AND status='cancelled'", id)
	return err
}
