//go:build windows

package licensegate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func TestOneTimeActivationAndDevicePersistence(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "license.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(db.DB(), "installation", "https://licenses.example", base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	c.now = func() time.Time { return now }
	active, badNonce, badSignature := true, false, false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var fields map[string]any
		if err := json.Unmarshal(request.Payload, &fields); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		device, _ := base64.StdEncoding.DecodeString(fields["deviceKey"].(string))
		if !ed25519.Verify(device, request.Payload, request.Signature) {
			t.Error("request signature invalid")
			w.WriteHeader(403)
			return
		}
		nonce := fields["nonce"].(string)
		if badNonce {
			nonce = "replayed"
		}
		p := claims{Product: Product, LicenseID: "license-1", InstallationID: fields["installationId"].(string), DeviceKey: fields["deviceKey"].(string), Permanent: true, Active: active, IssuedAt: now.Unix(), ValidUntil: now.Add(maxLease).Unix(), Nonce: nonce}
		payload, _ := json.Marshal(p)
		sig := ed25519.Sign(private, payload)
		if badSignature {
			sig[0] ^= 1
		}
		json.NewEncoder(w).Encode(envelope{Payload: payload, Signature: sig})
	}))
	defer server.Close()
	c.url = server.URL
	c.http = server.Client()
	if c.Check(ctx) == nil {
		t.Fatal("unactivated installation accepted")
	}
	if err = c.Activate(ctx, "PC-test-license-key"); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(db.DB(), "installation", server.URL, base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Check(ctx); err != nil {
		t.Fatalf("cached activation did not survive restart: %v", err)
	}
	badNonce = true
	if c.Activate(ctx, "PC-test-license-key") == nil {
		t.Fatal("replayed response accepted")
	}
	badNonce = false
	badSignature = true
	if c.Activate(ctx, "PC-test-license-key") == nil {
		t.Fatal("forged response accepted")
	}
	badSignature = false
	if err = c.Check(ctx); err != nil {
		t.Fatalf("bad response replaced valid cached lease: %v", err)
	}

	now = now.Add(365 * 24 * time.Hour)
	if err = c.Check(ctx); err != nil {
		t.Fatalf("permanent activation expired: %v", err)
	}
	reopened, err = New(db.DB(), "installation", server.URL, base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return now }
	if err = reopened.Check(ctx); err != nil {
		t.Fatalf("offline restart failed: %v", err)
	}
	copied, err := New(db.DB(), "another-installation", server.URL, base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	if copied.Check(ctx) == nil {
		t.Fatal("copied activation accepted")
	}
	if _, err = db.DB().Exec(`UPDATE permanent_license_state SET envelope='{}'`); err != nil {
		t.Fatal(err)
	}
	tampered, err := New(db.DB(), "installation", server.URL, base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	if tampered.Check(ctx) == nil {
		t.Fatal("tampered activation accepted on startup")
	}
	server.Close()
	if err = c.Refresh(ctx); err != nil {
		t.Fatalf("status refresh contacted server: %v", err)
	}
	if err = c.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
