package kiosk

import (
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/advertising"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultAdsDirectory = "/var/lib/printcatalyst-kiosk/ads"

var adTypes = map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".mp4": "video/mp4", ".webm": "video/webm"}

type screenAd struct {
	URL     string `json:"url"`
	Type    string `json:"type"`
	Default bool   `json:"default"`
	Seconds int    `json:"seconds"`
}

// Media is administrator-installed on disk, never uploaded by a customer.
// OpenRoot confines file access to the ad directory, including symlink targets.
func (s *Server) serveAds(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/ads/") {
		return false
	}
	if (r.Method != "GET" && r.Method != "HEAD") || r.URL.RawQuery != "" {
		respond(w, 404, "not_found")
		return true
	}
	dir := s.adsDir
	if dir == "" {
		dir = defaultAdsDirectory
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if r.URL.Path == "/ads/playlist" {
			_ = json.NewEncoder(w).Encode([]screenAd{})
		} else {
			respond(w, 404, "not_found")
		}
		return true
	}
	defer root.Close()
	if r.URL.Path == "/ads/playlist" {
		list := []screenAd{}
		campaigns, campaignErr := advertising.List(r.Context(), s.db)
		if campaignErr != nil {
			respond(w, 503, "unavailable")
			return true
		}
		if len(campaigns) > 0 {
			for _, c := range campaigns {
				if c.Schedule.Active(time.Now()) || (c.Schedule.Enabled && c.Schedule.Default) {
					list = append(list, screenAd{URL: "/ads/" + url.PathEscape(c.Filename), Type: c.Type, Default: c.Schedule.Default, Seconds: c.Schedule.ImageSeconds})
				}
			}
			_ = json.NewEncoder(w).Encode(list)
			return true
		}
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				kind := adTypes[strings.ToLower(filepath.Ext(entry.Name()))]
				if kind == "" || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 250<<20 {
					continue
				}
				list = append(list, screenAd{URL: "/ads/" + url.PathEscape(entry.Name()), Type: kind})
				if len(list) == 50 {
					break
				}
			}
		}
		_ = json.NewEncoder(w).Encode(list)
		return true
	}
	name := strings.TrimPrefix(r.URL.Path, "/ads/")
	kind := adTypes[strings.ToLower(filepath.Ext(name))]
	if kind == "" || strings.ContainsAny(name, "/\\") || name == "" {
		respond(w, 404, "not_found")
		return true
	}
	file, err := root.Open(name)
	if err != nil {
		respond(w, 404, "not_found")
		return true
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 250<<20 {
		respond(w, 404, "not_found")
		return true
	}
	w.Header().Set("Content-Type", kind)
	// Media streaming can outlive the short API timeout; this deadline applies only to this response.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	http.ServeContent(w, r, name, info.ModTime(), file)
	return true
}
