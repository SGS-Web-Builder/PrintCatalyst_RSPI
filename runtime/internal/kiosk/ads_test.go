package kiosk

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdPlaylistAndVideoRange(t *testing.T) {
	s, _ := fixture(t)
	s.adsDir = t.TempDir()
	for name, body := range map[string]string{"01 image.png": "image", "02 video.mp4": "0123456789", "secret.txt": "secret", "bad.png": ""} {
		if err := os.WriteFile(filepath.Join(s.adsDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string, span string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+Address+path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		if span != "" {
			r.Header.Set("Range", span)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	w := get("/ads/playlist", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "01%20image.png") || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "bad.png") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = get("/ads/02%20video.mp4", "bytes=2-5")
	if w.Code != 206 || w.Body.String() != "2345" || w.Header().Get("Content-Type") != "video/mp4" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	for _, path := range []string{"/ads/secret.txt", "/ads/../secret.png", "/ads/bad.png"} {
		if w = get(path, ""); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://"+Address+"/ads/playlist", nil)
	r.RemoteAddr = "192.168.1.2:1234"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("ads bypassed local restriction", w.Code)
	}
}
func TestMissingAdDirectoryKeepsKioskUsable(t *testing.T) {
	s, _ := fixture(t)
	s.adsDir = filepath.Join(t.TempDir(), "missing")
	r := httptest.NewRequest("GET", "http://"+Address+"/ads/playlist", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestManagedCampaignsDoNotLeakExpiredFilesIntoPlaylist(t *testing.T) {
	s, _ := fixture(t)
	s.adsDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.adsDir, "expired.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := `{"enabled":true,"default":false,"start":"2000-01-01T00:00","zone":"UTC","duration":1,"unit":"days","repeat":"none","imageSeconds":10}`
	if _, err := s.db.Exec("INSERT INTO ad_campaigns VALUES('old','Expired','expired.png','image/png',5,?,0)", raw); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://"+Address+"/ads/playlist", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.db.Exec(`UPDATE ad_campaigns SET schedule=json_set(schedule,'$.default',json('true'))`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"default":true`) {
		t.Fatal(w.Body.String())
	}
}
