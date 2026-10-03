package localserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

func (s *Server) connectionInstallationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installationID
}

func (s *Server) paymentOrigin(ctx context.Context) (string, error) {
	const message = "Complete step 1: save and verify your public HTTPS domain in QR setup before connecting Razorpay."
	if s.tunnel == nil {
		return "", fmt.Errorf("%s", message)
	}
	snap, err := s.tunnel.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(snap.PublicOrigin)
	if err != nil || u.Scheme != "https" || !strings.Contains(u.Hostname(), ".") || net.ParseIP(u.Hostname()) != nil || strings.HasSuffix(u.Hostname(), ".localhost") || strings.HasSuffix(u.Hostname(), ".local") || snap.Status != tunnel.StatusOnline || snap.LastVerifiedAt == 0 {
		return "", fmt.Errorf("%s", message)
	}
	if s.PublicOrigin() != snap.PublicOrigin {
		return "", fmt.Errorf("Verify the domain again to apply it to the running application.")
	}
	return snap.PublicOrigin, nil
}

func (s *Server) requirePaymentOrigin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.paymentOrigin(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return false
	}
	return true
}

func (s *Server) registerPaymentConnection(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/owner/payments/connection", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		origin, err := s.paymentOrigin(r.Context())
		message := "Domain verified. Continue with your Razorpay API keys."
		if err != nil {
			message = err.Error()
		}
		writeJSON(w, map[string]any{"ready": err == nil, "publicOrigin": origin, "message": message})
	}))
	mux.HandleFunc("POST /api/v1/owner/payments/webhook-secret", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if !s.requirePaymentOrigin(w, r) {
			return
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			http.Error(w, "Could not generate webhook secret", 500)
			return
		}
		writeJSON(w, map[string]string{"secret": hex.EncodeToString(secret[:])})
	}))
}
