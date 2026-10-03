package localserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
	"unicode/utf8"
)

type portalBranding struct {
	BusinessName string `json:"businessName,omitempty"`
	Title        string `json:"title"`
	Subtitle     string `json:"subtitle"`
	Logo         string `json:"logo"`
	Icon         string `json:"icon"`
}

func validateBrandImage(raw string, icon bool) error {
	if raw == "" {
		return nil
	}
	parts := strings.SplitN(raw, ",", 2)
	if len(parts) != 2 || (parts[0] != "data:image/png;base64" && parts[0] != "data:image/jpeg;base64") {
		return fmt.Errorf("Use a PNG or JPG image")
	}
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(data) > 512*1024 {
		return fmt.Errorf("Each image must be at most 512 KB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "data:image/"+format+";base64" != parts[0] {
		return fmt.Errorf("Invalid PNG or JPG image")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 512 {
		return fmt.Errorf("Logo must fit within 2048 × 512 pixels")
	}
	if icon && (cfg.Width != cfg.Height || cfg.Width > 512) {
		return fmt.Errorf("Icon must be square, at most 512 × 512 pixels")
	}
	return nil
}
func (s *Server) registerPortalBranding(mux *http.ServeMux) {
	read := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.db == nil {
			http.Error(w, "Branding unavailable", 503)
			return
		}
		var raw string
		if err := s.db.QueryRowContext(r.Context(), "SELECT settings FROM portal_branding WHERE singleton=1").Scan(&raw); err != nil {
			http.Error(w, "Could not load branding", 500)
			return
		}
		var value portalBranding
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			http.Error(w, "Could not load branding", 500)
			return
		}
		if s.owner != nil {
			if profile, err := s.owner.Profile(r.Context()); err == nil {
				value.BusinessName = strings.TrimSpace(profile.Name)
			}
		}
		writeJSON(w, value)
	}
	mux.HandleFunc("GET /api/v1/portal/branding", func(w http.ResponseWriter, r *http.Request) {
		if s.portalGuard(w, r, false) {
			read(w, r)
		}
	})
	mux.HandleFunc("GET /api/v1/owner/portal-branding", s.protectOwnerView(read))
	mux.HandleFunc("PUT /api/v1/owner/portal-branding", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var value portalBranding
		if !decodeOwnerJSON(w, r, &value, 1500*1024) {
			return
		}
		value.Title = strings.TrimSpace(value.Title)
		value.Subtitle = strings.TrimSpace(value.Subtitle)
		if value.Title == "" || utf8.RuneCountInString(value.Title) > 80 || utf8.RuneCountInString(value.Subtitle) > 160 {
			http.Error(w, "Enter a title up to 80 characters and subtitle up to 160 characters", 400)
			return
		}
		for _, entry := range []struct {
			raw  string
			icon bool
		}{{value.Logo, false}, {value.Icon, true}} {
			if err := validateBrandImage(entry.raw, entry.icon); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		raw, _ := json.Marshal(value)
		if _, err := s.db.ExecContext(r.Context(), "UPDATE portal_branding SET settings=? WHERE singleton=1", string(raw)); err != nil {
			http.Error(w, "Could not save branding", 500)
			return
		}
		writeJSON(w, value)
	}))
}
