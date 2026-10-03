package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/configparser"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/qr_theme"
)

func (s *Server) saveDirectOrigin(ctx context.Context, origin string) error {
	if s.files == nil {
		return fmt.Errorf("local configuration storage unavailable")
	}
	values, err := configparser.Load(s.files.DataRoot(), nil)
	if err != nil {
		return err
	}
	values["PC_PUBLIC_ORIGIN"] = origin
	values["PC_BIND_HOST"] = "0.0.0.0"
	parsed, _ := url.Parse(origin)
	if strings.Contains(parsed.Hostname(), ":") {
		values["PC_BIND_HOST"] = "::"
	}
	var keys []string
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&body, "%s=%s\n", k, values[k])
	}
	if err = s.files.WriteAtomic(configparser.ConfigFileName, strings.NewReader(body.String())); err != nil {
		return err
	}
	if s.configReload != nil {
		_, _, err = s.configReload(ctx)
		return err
	}
	return nil
}

type qrThemeView struct {
	Dark  string `json:"dark"`
	Light string `json:"light"`
	Frame string `json:"frame"`
	Logo  []byte `json:"logo"`
}

func (s *Server) readQRTheme(ctx context.Context) (qrThemeView, error) {
	v := qrThemeView{Dark: "#0a0a0a", Light: "#ffffff"}
	if s.db == nil {
		return v, nil
	}
	err := s.db.QueryRowContext(ctx, "SELECT dark,light,frame,logo FROM qr_theme WHERE singleton=1").Scan(&v.Dark, &v.Light, &v.Frame, &v.Logo)
	return v, err
}
func (s *Server) themedQR(ctx context.Context, url string) (string, error) {
	v, err := s.readQRTheme(ctx)
	if err != nil {
		return "", err
	}
	return qr_theme.EncodeThemed(url, qr_theme.Theme{Dark: v.Dark, Light: v.Light, Frame: v.Frame, LogoPNG: v.Logo})
}
func (s *Server) registerShopSettings(mux *http.ServeMux) {
	s.registerPortalPaymentButtons(mux)
	s.registerCustomerDetails(mux)
	s.registerPortalBranding(mux)
	s.registerPaperStock(mux)
	mux.HandleFunc("GET /api/v1/owner/orders/{id}/documents/{documentID}", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		var path, name, kind string
		err := s.db.QueryRowContext(r.Context(), "SELECT storage_path,original_filename,mime_type FROM documents WHERE id=? AND order_id=? AND purged_at=0", r.PathValue("documentID"), r.PathValue("id")).Scan(&path, &name, &kind)
		if err != nil {
			http.Error(w, "document unavailable", 404)
			return
		}
		body, err := documents.New(s.files, s.db).FetchAt(r.Context(), path)
		if err != nil {
			http.Error(w, "document unavailable", 404)
			return
		}
		w.Header().Set("Content-Type", kind)
		disposition := "attachment"
		if kind == "application/pdf" || kind == "image/png" || kind == "image/jpeg" {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	}))

	mux.HandleFunc("GET /api/v1/owner/qr-theme", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		v, err := s.readQRTheme(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(v)
	}))
	mux.HandleFunc("PUT /api/v1/owner/qr-theme", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var v qrThemeView
		if !decodeOwnerJSON(w, r, &v, 2<<20) {
			return
		}
		theme := qr_theme.Theme{Dark: v.Dark, Light: v.Light, Frame: v.Frame, LogoPNG: v.Logo}
		if err := theme.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE qr_theme SET dark=?,light=?,frame=?,logo=? WHERE singleton=1", v.Dark, v.Light, v.Frame, v.Logo); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(v)
	}))
	mux.HandleFunc("POST /api/v1/owner/orders/{id}/print", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if s.dispatcher == nil {
			http.Error(w, "dispatcher unavailable", 503)
			return
		}
		var in struct {
			Retry bool `json:"retry"`
		}
		if !decodeOwnerJSON(w, r, &in, 1024) {
			return
		}
		if err := s.dispatcher.RequestPrint(r.Context(), r.PathValue("id"), in.Retry); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"queued": true})
	}))
	mux.HandleFunc("GET /api/v1/owner/orders/{id}/print", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.QueryContext(r.Context(), `SELECT l.id,COALESCE(j.state,'waiting'),COALESCE(j.error,''),COALESCE(j.job_id,'') FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=? ORDER BY l.id`, r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		lines := []map[string]string{}
		for rows.Next() {
			var id, state, detail, job string
			if err = rows.Scan(&id, &state, &detail, &job); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			lines = append(lines, map[string]string{"id": id, "state": state, "error": detail, "jobId": job})
		}
		if err = rows.Err(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(lines)
	}))
}
