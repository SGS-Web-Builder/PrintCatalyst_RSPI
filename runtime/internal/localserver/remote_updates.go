package localserver

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Signed staging deliberately does not execute an uploaded installer in the web
// process. The local administrator installs staged archives during maintenance.
func verifyUpdate(data []byte, signature string, key []byte) (string, error) {
	public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(key)))
	if err != nil || len(public) != ed25519.PublicKeySize {
		return "", errors.New("invalid publisher public key")
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return "", errors.New("invalid release signature")
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if !ed25519.Verify(public, []byte("PrintCatalyst-Pi-update-v1\n"+digest+"\n"), sig) {
		return "", errors.New("release signature does not match archive")
	}
	return digest, nil
}
func (s *Server) registerRemoteUpdates(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/remote/updates", func(w http.ResponseWriter, r *http.Request) {
		if !s.adGuard(w, r) {
			return
		}
		if _, ok := s.remoteSession(w, r); !ok {
			return
		}
		path := os.Getenv("PC_UPDATE_PUBLIC_KEY_FILE")
		if path == "" {
			http.Error(w, "Publisher public key is not configured on this Pi", 409)
			return
		}
		key, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "Publisher public key unavailable", 503)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 80<<20)
		data, err := io.ReadAll(r.Body)
		if err != nil || len(data) == 0 {
			http.Error(w, "Archive must be non-empty and at most 80 MiB", 400)
			return
		}
		digest, err := verifyUpdate(data, r.Header.Get("X-Update-Signature"), key)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err = s.files.WriteAtomic("updates/"+digest+".tar.gz", bytes.NewReader(data)); err != nil {
			http.Error(w, "Could not stage archive", 503)
			return
		}
		_, err = s.db.ExecContext(r.Context(), "INSERT INTO remote_commands(id,kind,state,detail,created_at,updated_at) VALUES(?,'update','staged','Signature verified; local installation required',?,?) ON CONFLICT(id) DO NOTHING", digest, time.Now().Unix(), time.Now().Unix())
		if err != nil {
			ownerError(w, err)
			return
		}
		writeJSON(w, map[string]string{"sha256": digest, "state": "staged", "message": "Verified archive staged for local administrator installation; running software is unchanged"})
	})
}
