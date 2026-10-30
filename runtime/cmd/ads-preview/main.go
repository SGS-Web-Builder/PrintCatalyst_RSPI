// ads-preview is an isolated, loopback-only development fixture, never packaged.
// It creates disposable owner/ad sessions, with no real customer or printer data.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/kiosk"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	root, e := os.MkdirTemp("", "pc-ad-preview-")
	if e != nil {
		log.Fatal(e)
	}
	ctx := context.Background()
	db, e := store.Open(ctx, filepath.Join(root, "preview.db"))
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	files, e := localfiles.New(root)
	if e != nil {
		log.Fatal(e)
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	accounts := owner.New(db.DB())
	password := hex.EncodeToString(secret)
	if e = accounts.Create(ctx, "preview-owner", password); e != nil {
		log.Fatal(e)
	}
	admin, _, e := accounts.Login(ctx, "preview-owner", password)
	if e != nil {
		log.Fatal(e)
	}
	token, e := owner.RandomToken()
	if e != nil {
		log.Fatal(e)
	}
	if _, e = db.DB().Exec("INSERT INTO ad_admin_sessions VALUES(?,?,?)", owner.Digest(token), owner.Digest(admin), time.Now().Add(8*time.Hour).Unix()); e != nil {
		log.Fatal(e)
	}
	screen, e := kiosk.New(db.DB(), previewClaimer{}, secret, func(context.Context) error { return errors.New("preview only") })
	if e != nil {
		log.Fatal(e)
	}
	screen.SetAdsDirectory(filepath.Join(root, "ads"))
	handler := localserver.New("127.0.0.1:18085", "ad-preview", localserver.WithDB(db.DB()), localserver.WithFiles(files), localserver.WithOwner(accounts, "")).Handler()
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:18085" {
			http.Error(w, "Loopback preview only", 403)
			return
		}
		if r.Method == "GET" || r.Method == "HEAD" {
			switch r.URL.Path {
			case "/screen/", "/kiosk.css", "/kiosk.js", "/ads.js":
				copy := r.Clone(r.Context())
				copy.Host = kiosk.Address
				if copy.URL.Path == "/screen/" {
					copy.URL.Path = "/"
				}
				screen.Handler().ServeHTTP(w, copy)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/ads/") {
				copy := r.Clone(r.Context())
				copy.Host = kiosk.Address
				screen.Handler().ServeHTTP(w, copy)
				return
			}
		}
		if r.URL.Path == "/preview/" {
			http.SetCookie(w, &http.Cookie{Name: "pc_ad_admin", Value: token, Path: "/api/v1/ads-admin/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
			http.Redirect(w, r, "/ads-admin/", http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/ads-admin/" {
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, r)
			for key, values := range out.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(out.Code)
			w.Write([]byte(strings.Replace(out.Body.String(), "<main>", "<main><p class=\"card\">Local demo · temporary ad library · not connected to your Pi</p>", 1)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/ads-admin/") || r.URL.Path == "/ads-admin.css" || r.URL.Path == "/ads-admin.mjs" {
			handler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	log.Print("Temporary ad-management preview: http://127.0.0.1:18085/preview/")
	log.Fatal(http.ListenAndServe("127.0.0.1:18085", mux))
}

// Defence in depth: the preview never mounts kiosk API routes or dispatch workers.
type previewClaimer struct{}

func (previewClaimer) Claim(context.Context, string) (string, error) {
	return "", errors.New("printing is disabled in the preview")
}
