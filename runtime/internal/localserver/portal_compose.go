package localserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
)

func (s *Server) registerPortalComposition(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/portal/compose-images", func(w http.ResponseWriter, r *http.Request) {
		if !s.portalGuard(w, r, true) {
			return
		}
		if reason := s.notReadyReason(r.Context()); reason != "" {
			portalJSONError(w, reason, 503)
			return
		}
		var input struct {
			OrderID     string   `json:"orderId"`
			IDs         []string `json:"documentIds"`
			PerPage     int      `json:"perPage"`
			Paper       string   `json:"paperSize"`
			Orientation string   `json:"orientation"`
			Split       int      `json:"split"`
		}
		if !portalDecode(w, r, &input, 8192) {
			return
		}
		if len(input.IDs) < 2 || len(input.IDs) > 10 {
			portalJSONError(w, "Select between 2 and 10 images", 400)
			return
		}
		tx, err := s.db.BeginTx(r.Context(), nil)
		if err != nil {
			portalJSONError(w, "Storage is busy. Retry shortly.", 503)
			return
		}
		defer tx.Rollback()
		var token, status string
		var submitted int64
		err = tx.QueryRowContext(r.Context(), "SELECT share_token,status,submitted_at FROM orders WHERE id=?", input.OrderID).Scan(&token, &status, &submitted)
		if err != nil || token == "" || token != r.Header.Get("X-Upload-Token") {
			portalJSONError(w, "Upload session not found", 404)
			return
		}
		if submitted != 0 || status != "pending_payment" {
			portalJSONError(w, "This order has already been submitted", 409)
			return
		}
		// The source identities and options make retries return the same document.
		signature, _ := json.Marshal(input)
		sum := sha256.Sum256(signature)
		name := "Merged images-" + hex.EncodeToString(sum[:8]) + ".pdf"
		if input.PerPage > 1 {
			name = "Photo collage-" + hex.EncodeToString(sum[:8]) + ".pdf"
		}
		var output documents.Document
		err = tx.QueryRowContext(r.Context(), "SELECT id,original_filename,mime_type,size_bytes,page_count FROM documents WHERE order_id=? AND original_filename=? AND purged_at=0", input.OrderID, name).Scan(&output.ID, &output.OriginalFilename, &output.MIMEType, &output.SizeBytes, &output.PageCount)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			portalJSONError(w, "Could not check previous composition. Retry shortly.", 503)
			return
		}
		if err != nil {
			var sources [][]byte
			var paths []string
			seen := map[string]bool{}
			total := 0
			docs := documents.New(s.files, s.db)
			for _, id := range input.IDs {
				if seen[id] {
					portalJSONError(w, "Select each image only once", 400)
					return
				}
				seen[id] = true
				var path, mime string
				if err = tx.QueryRowContext(r.Context(), "SELECT storage_path,mime_type FROM documents WHERE id=? AND order_id=? AND purged_at=0", id, input.OrderID).Scan(&path, &mime); err != nil {
					portalJSONError(w, "Selected image is no longer available. Reload the portal.", 409)
					return
				}
				if mime != "image/jpeg" && mime != "image/png" {
					portalJSONError(w, "Merge and collage support JPG and PNG images only", 400)
					return
				}
				body, err := docs.FetchAt(r.Context(), path)
				if err != nil {
					portalJSONError(w, "Could not read selected image", 500)
					return
				}
				total += len(body)
				if total > 100<<20 {
					portalJSONError(w, "Select fewer images: the combined source limit is 100 MB", 400)
					return
				}
				sources = append(sources, body)
				paths = append(paths, path)
			}
			pdf, err := documents.ComposeImages(sources, input.PerPage, strings.ToUpper(input.Paper), input.Orientation, input.Split)
			if err != nil {
				portalJSONError(w, err.Error(), 400)
				return
			}
			var multipartBody bytes.Buffer
			writer := multipart.NewWriter(&multipartBody)
			part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="files"; filename="%s"`, name)}, "Content-Type": {"application/pdf"}})
			if err != nil {
				portalJSONError(w, "Could not create combined document", 500)
				return
			}
			part.Write(pdf)
			writer.Close()
			form, err := multipart.NewReader(bytes.NewReader(multipartBody.Bytes()), writer.Boundary()).ReadForm(int64(len(pdf)) + 1024)
			if err != nil {
				portalJSONError(w, "Could not create combined document", 500)
				return
			}
			defer form.RemoveAll()
			output, err = documents.NewWithTransaction(s.files, tx).Save(r.Context(), input.OrderID, form.File["files"][0])
			if err != nil {
				portalJSONError(w, "Could not store combined document", 500)
				return
			}
			committed := false
			defer func() {
				if !committed {
					_ = docs.Delete(context.Background(), output.StoragePath)
				}
			}()
			for _, id := range input.IDs {
				if _, err = tx.ExecContext(r.Context(), "DELETE FROM documents WHERE id=? AND order_id=?", id, input.OrderID); err != nil {
					portalJSONError(w, "Could not update documents", 500)
					return
				}
			}
			if err = tx.Commit(); err != nil {
				portalJSONError(w, "Could not save combined document", 500)
				return
			}
			committed = true
			for _, path := range paths {
				_ = docs.Delete(context.Background(), path)
			}
		} else {
			tx.Rollback()
		}
		writeJSON(w, map[string]any{"documentId": output.ID, "originalFilename": output.OriginalFilename, "mimeType": output.MIMEType, "sizeBytes": output.SizeBytes, "pageCount": output.PageCount})
	})
}
