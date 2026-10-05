// Package kiosk provides the private physical release boundary. It exposes no
// merchant endpoints and is never mounted on the public portal listener.
package kiosk

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Address = "127.0.0.1:8081"

type Claimer interface {
	Claim(context.Context, string) (string, error)
}
type Server struct {
	db           *sql.DB
	pickup       Claimer
	check        func(context.Context) error
	credential   [32]byte
	now          func() time.Time
	mu           sync.Mutex
	server       *http.Server
	listener     net.Listener
	serveErr     error
	sessionMu    sync.Mutex
	ticketHash   [32]byte
	ticketUntil  time.Time
	sessionHash  [32]byte
	sessionUntil time.Time
}

func New(db *sql.DB, p Claimer, key []byte, check func(context.Context) error) (*Server, error) {
	if db == nil || p == nil || len(key) != 32 || check == nil {
		return nil, errors.New("kiosk storage, pickup, credential and licence check are required")
	}
	return &Server{db: db, pickup: p, check: check, credential: sha256.Sum256([]byte("Bearer " + base64.RawURLEncoding.EncodeToString(key))), now: time.Now}, nil
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.handle) }
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(peer)
	if err != nil || ip == nil || !ip.IsLoopback() || r.Host != Address {
		respond(w, 403, "forbidden")
		return
	}
	for name := range r.Header {
		n := strings.ToLower(name)
		if n == "forwarded" || strings.HasPrefix(n, "x-forwarded-") || strings.HasPrefix(n, "cf-") || n == "x-real-ip" {
			respond(w, 403, "forbidden")
			return
		}
	}
	if s.serveAsset(w, r) {
		return
	}
	if r.Header.Get("Origin") != "http://"+Address {
		respond(w, 403, "forbidden")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		respond(w, 403, "forbidden")
		return
	}
	auth := r.Header.Values("Authorization")
	bearer := false
	if len(auth) == 1 {
		digest := sha256.Sum256([]byte(auth[0]))
		bearer = subtle.ConstantTimeCompare(digest[:], s.credential[:]) == 1
	}
	if s.handleSession(w, r, bearer) {
		return
	}
	if !bearer && (len(auth) != 0 || !s.validSession(r)) {
		respond(w, 403, "forbidden")
		return
	}
	if !bearer {
		http.SetCookie(w, &http.Cookie{Name: "pc_kiosk", Value: r.CookiesNamed("pc_kiosk")[0].Value, Path: "/api/v1/kiosk/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 60 * 60})
	}
	if r.URL.Path == "/api/v1/kiosk/progress" {
		s.progress(w, r)
		return
	}
	if r.URL.Path != "/api/v1/kiosk/claim" || r.URL.RawQuery != "" {
		respond(w, 404, "not_found")
		return
	}
	if r.Method != "POST" {
		respond(w, 405, "method_not_allowed")
		return
	}
	if wait, err := s.reserve(r.Context()); err != nil {
		respond(w, 503, "unavailable")
		return
	} else if wait > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(wait, 10))
		respond(w, 429, "cooldown")
		return
	}
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		respond(w, 415, "json_required")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		respond(w, 400, "invalid_code")
		return
	}
	if err = s.check(r.Context()); err != nil {
		respond(w, 503, "assistance")
		return
	}
	order, err := s.pickup.Claim(r.Context(), input.Code)
	switch {
	case err == nil:
		s.accepted(w, r, order)
	case errors.Is(err, pickup.ErrPreparing):
		respond(w, 409, "preparing")
	case errors.Is(err, pickup.ErrUnavailable):
		respond(w, 400, "invalid_code")
	default:
		respond(w, 503, "assistance")
	}
}
func respond(w http.ResponseWriter, status int, state string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"state": state})
}

func (s *Server) Name() string { return "physical-kiosk" }
func (s *Server) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", Address)
	if err != nil {
		return err
	}
	s.listener = listener
	s.serveErr = nil
	s.server = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	server := s.server
	go func() {
		err := server.Serve(listener)
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.serveErr = err
		}
	}()
	return nil
}
func (s *Server) Ready(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return errors.New("kiosk listener is stopped")
	}
	return s.serveErr
}
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	err := server.Shutdown(ctx)
	if err != nil {
		_ = server.Close()
	}
	s.mu.Lock()
	s.listener = nil
	s.server = nil
	s.mu.Unlock()
	return err
}
