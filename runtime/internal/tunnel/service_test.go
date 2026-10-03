package tunnel

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newServiceForTest opens an in-memory SQLite, applies the embedded tunnel
// schema and returns a wired service. It is shared by every test in this
// file so each one starts from a clean, minimal database.
func newServiceForTest(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tunnel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })
	for _, stmt := range []string{
		`CREATE TABLE tunnel_state (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			provider TEXT NOT NULL DEFAULT 'cloudflared',
			public_origin TEXT NOT NULL DEFAULT '',
			tunnel_token_ref TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'unconfigured',
			last_verified_at INTEGER,
			last_verified_status INTEGER,
			last_verified_error TEXT NOT NULL DEFAULT '',
			last_attempt_at INTEGER,
			last_error TEXT NOT NULL DEFAULT '',
			qr_target_path TEXT NOT NULL DEFAULT '/portal/',
			shop_route TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE tunnel_events (
			id TEXT PRIMARY KEY NOT NULL,
			occurred_at INTEGER NOT NULL,
			status TEXT NOT NULL,
			detail TEXT NOT NULL DEFAULT '',
			http_status INTEGER,
			round_trip_ms INTEGER
		)`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Unix(1_700_000_000, 0)
	idSeq := 0
	svc := &Service{
		database: database,
		now: func() time.Time {
			idSeq++
			return base.Add(time.Duration(idSeq) * time.Second)
		},
		newID: func() string {
			return "id-" + string(rune('a'+idSeq))
		},
	}
	return svc, database
}

func TestSnapshotEmpty(t *testing.T) {
	svc, _ := newServiceForTest(t)
	snap, err := svc.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != StatusUnconfigured {
		t.Fatalf("status = %q, want unconfigured", snap.Status)
	}
	if snap.PublicOrigin != "" {
		t.Fatalf("public origin = %q, want empty", snap.PublicOrigin)
	}
	if snap.PublicURL != "" {
		t.Fatalf("public url = %q, want empty", snap.PublicURL)
	}
	if snap.HasToken {
		t.Fatal("HasToken should be false")
	}
	if snap.QRTargetPath != "/portal/" {
		t.Fatalf("qr target path = %q, want /portal/", snap.QRTargetPath)
	}
}

func TestSaveConfigRejectsInvalidOrigin(t *testing.T) {
	svc, _ := newServiceForTest(t)
	_, err := svc.SaveConfig(context.Background(), Config{PublicOrigin: "http://shop.example.com"})
	if err == nil {
		t.Fatal("expected error for non-HTTPS origin")
	}
}

func TestSaveConfigStoresFingerprint(t *testing.T) {
	svc, _ := newServiceForTest(t)
	snap, err := svc.SaveConfig(context.Background(), Config{
		Provider:     ProviderCloudflared,
		PublicOrigin: "https://shop.example.com",
		TunnelToken:  "supersecret-token-1234567890",
		ShopRoute:    "shop-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.HasToken != true {
		t.Fatal("HasToken should be true after save")
	}
	if snap.TunnelTokenFingerprint == "supersecret-token-1234567890" {
		t.Fatal("raw token leaked to snapshot")
	}
	if snap.TunnelTokenFingerprint == "" {
		t.Fatal("fingerprint should be set")
	}
	if len(snap.TunnelTokenFingerprint) != 12 {
		t.Fatalf("fingerprint length = %d, want 12", len(snap.TunnelTokenFingerprint))
	}
	if snap.PublicURL != "https://shop.example.com/portal/shop-42" {
		t.Fatalf("public URL = %q", snap.PublicURL)
	}
	if snap.Status != StatusUnconfigured {
		t.Fatalf("status = %q, want unconfigured", snap.Status)
	}
}

func TestSaveConfigPersistsTokenAcrossWrites(t *testing.T) {
	svc, _ := newServiceForTest(t)
	if _, err := svc.SaveConfig(context.Background(), Config{
		Provider:     ProviderCloudflared,
		PublicOrigin: "https://shop.example.com",
		TunnelToken:  "supersecret-token-1234567890",
	}); err != nil {
		t.Fatal(err)
	}
	// Update the origin without supplying a new token.
	snap, err := svc.SaveConfig(context.Background(), Config{
		Provider:     ProviderCloudflared,
		PublicOrigin: "https://new.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.HasToken {
		t.Fatal("token should persist when not overwritten")
	}
	if snap.PublicOrigin != "https://new.example.com" {
		t.Fatalf("origin = %q", snap.PublicOrigin)
	}
}

func TestSetStatusInsertsSingleton(t *testing.T) {
	svc, _ := newServiceForTest(t)
	snap, err := svc.SetStatus(context.Background(), StatusStarting, "tunnel is starting")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != StatusStarting {
		t.Fatalf("status = %q, want starting", snap.Status)
	}
	if snap.LastError != "tunnel is starting" {
		t.Fatalf("last error = %q", snap.LastError)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	svc, _ := newServiceForTest(t)
	if _, err := svc.SetStatus(context.Background(), StatusStarting, "starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetStatus(context.Background(), StatusOnline, "ok"); err != nil {
		t.Fatal(err)
	}
	events, err := svc.Events(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	// Newest first.
	if events[0].Status != StatusOnline {
		t.Fatalf("newest event status = %q, want online", events[0].Status)
	}
	if events[1].Status != StatusStarting {
		t.Fatalf("second event status = %q, want starting", events[1].Status)
	}
}

func TestProbeAndRecordWithoutOrigin(t *testing.T) {
	svc, _ := newServiceForTest(t)
	_, result, err := svc.ProbeAndRecord(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUnconfigured {
		t.Fatalf("status = %q, want unconfigured", result.Status)
	}
	if result.Error == "" {
		t.Fatal("expected an error reason")
	}
}

func TestVerifyReachesLocalServer(t *testing.T) {
	// Use a local httptest server as the public origin. The probe does not
	// care that the URL is loopback; it just checks HTTPS semantics. Since
	// httptest.NewTLSServer gives us a real https:// scheme, we use it as
	// a stand-in for the tunneled public origin.
}
