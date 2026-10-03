//go:build windows

package licensegate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func latencyClient(t testing.TB) *Client {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "license.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(db.DB(), "latency-test", "https://licenses.example", base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := claims{Product: Product, LicenseID: "test", InstallationID: c.installation, DeviceKey: base64.StdEncoding.EncodeToString(c.key.Public().(ed25519.PublicKey)), Permanent: true, Active: true, IssuedAt: now.Unix(), ValidUntil: now.Add(maxLease).Unix()}
	payload, _ := json.Marshal(p)
	raw, _ := json.Marshal(envelope{Payload: payload, Signature: ed25519.Sign(priv, payload)})
	if _, err = db.DB().Exec(`INSERT INTO permanent_license_state VALUES(1,?,?)`, raw, now.Unix()); err != nil {
		t.Fatal(err)
	}
	c.loadActivation(context.Background())
	return c
}

func TestSlowActivationDoesNotBlockActivatedPrints(t *testing.T) {
	c := latencyClient(t)
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(503) }))
	defer server.Close()
	defer close(release)
	c.url = server.URL
	c.http = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresh := make(chan error, 1)
	go func() { refresh <- c.Activate(ctx, "PC-test-license-key") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never reached server")
	}
	check := make(chan error, 1)
	start := time.Now()
	go func() { check <- c.Check(ctx) }()
	select {
	case err := <-check:
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("local licence check with stalled refresh: %s", time.Since(start))
	case <-time.After(time.Second):
		t.Fatal("local print check waited on network refresh")
	}
	// A permanently activated PC remains usable after the old lease window.
	c.now = func() time.Time { return time.Now().Add(maxLease + time.Hour) }
	if c.Check(ctx) != nil {
		t.Fatal("permanent activation expired")
	}
}

func BenchmarkLocalLicenseCheck(b *testing.B) {
	c := latencyClient(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Check(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
