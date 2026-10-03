package localserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalCloudflareGuard(t *testing.T) {
	for _, tc := range []struct {
		name, peer, configured, host, proto, origin string
		want                                        bool
	}{
		{"cloudflare", "127.0.0.1:5000", "https://print.everhost.in", "print.everhost.in", "https", "https://print.everhost.in", true},
		{"ipv6 connector", "[::1]:5000", "https://print.everhost.in", "print.everhost.in", "https", "https://print.everhost.in", true},
		{"remote spoof", "192.168.1.20:5000", "https://print.everhost.in", "print.everhost.in", "https", "https://print.everhost.in", false},
		{"unconfigured", "127.0.0.1:5000", "", "print.everhost.in", "https", "https://print.everhost.in", false},
		{"wrong host", "127.0.0.1:5000", "https://print.everhost.in", "evil.example", "https", "https://evil.example", false},
		{"cross origin", "127.0.0.1:5000", "https://print.everhost.in", "print.everhost.in", "https", "https://evil.example", false},
		{"host chain", "127.0.0.1:5000", "https://print.everhost.in", "print.everhost.in, evil.example", "https", "https://print.everhost.in", false},
		{"wrong scheme", "127.0.0.1:5000", "https://print.everhost.in", "print.everhost.in", "http", "https://print.everhost.in", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New("", "test", WithPublicOrigin(tc.configured))
			r := httptest.NewRequest("POST", "http://localhost:8080/api/v1/portal/quote", strings.NewReader(`{}`))
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-Host", tc.host)
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			r.Header.Set("X-Forwarded-For", "203.0.113.10")
			r.Header.Set("CF-Connecting-IP", "203.0.113.10")
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", "application/json")
			if got := s.portalGuard(httptest.NewRecorder(), r, true); got != tc.want {
				t.Fatalf("accepted = %v", got)
			}
			// Cloudflare can preserve Host without sending X-Forwarded-Host.
			r.Host = tc.host
			r.Header.Del("X-Forwarded-Host")
			if got := s.portalGuard(httptest.NewRecorder(), r, true); got != tc.want {
				t.Fatalf("preserved Host accepted = %v", got)
			}
		})
	}
}

func TestPortalDirectLANStillWorksWithPublicDomain(t *testing.T) {
	s := New("", "test", WithPublicOrigin("https://print.everhost.in"))
	r := httptest.NewRequest("POST", "http://192.168.1.10:8080/api/v1/portal/quote", strings.NewReader(`{}`))
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("Origin", "http://192.168.1.10:8080")
	r.Header.Set("Content-Type", "application/json")
	if !s.portalGuard(httptest.NewRecorder(), r, true) {
		t.Fatal("direct LAN access rejected")
	}
}
