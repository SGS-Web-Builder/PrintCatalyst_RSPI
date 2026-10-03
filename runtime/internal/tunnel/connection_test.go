package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyInstallationIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		online     bool
	}{
		{"correct", `{"service":"print-catalyst-on-premise","installationId":"shop-a"}`, true},
		{"wrong shop", `{"service":"print-catalyst-on-premise","installationId":"shop-b"}`, false},
		{"generic success", `<html>Welcome</html>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result := Verify(context.Background(), server.URL, WithInstallation("shop-a"))
			if (result.Status == StatusOnline) != tc.online {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestSavingDomainInvalidatesVerification(t *testing.T) {
	svc, db := newServiceForTest(t)
	ctx := context.Background()
	cfg := Config{Provider: ProviderCloudflared, PublicOrigin: "https://old.example.com"}
	if _, err := svc.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE tunnel_state SET status='online', last_verified_at=123"); err != nil {
		t.Fatal(err)
	}
	cfg.PublicOrigin = "https://new.example.com"
	snap, err := svc.SaveConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != StatusUnconfigured || snap.LastVerifiedAt != 0 {
		t.Fatalf("old domain verification retained: %+v", snap)
	}
}
