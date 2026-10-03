package localserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/currency"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

const ownerCookie = "pc_local_owner"

// businessView is the persisted business profile plus the currency precision the
// server derived from supported-currency metadata. Precision is reported here,
// and nowhere accepted as input, so the settings screen converts a typed amount
// with exactly the exponent the pricing service will store it under. It is a
// response shape only: the stored profile is unchanged, and the form never
// submits these computed fields back.
type businessView struct {
	owner.Profile
	CurrencyMinorUnits int  `json:"currencyMinorUnits"`
	CurrencySupported  bool `json:"currencySupported"`
}

func newBusinessView(profile owner.Profile) businessView {
	minorUnits := currency.MinorUnits(profile.Currency)
	return businessView{
		Profile:            profile,
		CurrencyMinorUnits: minorUnits,
		CurrencySupported:  minorUnits != currency.Unsupported,
	}
}

func WithOwner(service *owner.Service, setupToken string) Option {
	return func(s *Server) { s.owner = service; s.setupToken = setupToken }
}
func (s *Server) localOwnerRequest(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	host, port, err := net.SplitHostPort(r.Host)
	_, expectedPort, _ := net.SplitHostPort(s.address)
	if err != nil || port != expectedPort || (host != "localhost" && host != "127.0.0.1" && host != "::1") || !isLoopbackClient(r.RemoteAddr) {
		http.Error(w, "local owner access required", 403)
		return false
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "CF-Connecting-IP"} {
		if r.Header.Get(header) != "" {
			http.Error(w, "forwarded owner access is forbidden", 403)
			return false
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	origin := r.Header.Get("Origin")
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" || (origin != "" && origin != scheme+"://"+r.Host) {
		http.Error(w, "same-origin access required", 403)
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		// A missing Origin header on a non-GET is ambiguous: it could be a
		// same-origin fetch with the header stripped by a privacy tool, or a
		// cross-origin request without one. We accept it here and let the
		// loopback / no-forwarded-headers checks above provide the protection.
		// We only enforce a matching origin when the header IS present.
		if origin != "" && origin != scheme+"://"+r.Host {
			http.Error(w, "same-origin access required", 403)
			return false
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			http.Error(w, "JSON required", 415)
			return false
		}
	}
	if s.owner == nil {
		http.Error(w, "owner service unavailable", 503)
		return false
	}
	return true
}
func decodeOwner(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeOwnerJSON(w, r, target, 16<<10)
}

// decodeOwnerJSON enforces a per-endpoint request body limit. Pricing carries a
// larger limit because a merchant price book holds many rows.
func decodeOwnerJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one JSON object required", 400)
		return false
	}
	return true
}
func ownerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, owner.ErrInvalid), errors.Is(err, pricing.ErrInvalid), errors.Is(err, notifications.ErrInvalid), errors.Is(err, printers.ErrInvalid),
		errors.Is(err, idcards.ErrInvalid), errors.Is(err, idcards.ErrBadCorners), errors.Is(err, idcards.ErrCompose), errors.Is(err, idcards.ErrBadCalibration),
		errors.Is(err, passport.ErrInvalid), errors.Is(err, passport.ErrBadFaceRegion), errors.Is(err, passport.ErrCompose),
		errors.Is(err, passport.ErrUnknownPreset), errors.Is(err, passport.ErrInvalidBackground):
		http.Error(w, err.Error(), 400)
	case errors.Is(err, owner.ErrSetup), errors.Is(err, pricing.ErrSetup),
		errors.Is(err, pricing.ErrUnsupportedCurrency), errors.Is(err, pricing.ErrPrecisionCorrection),
		errors.Is(err, pricing.ErrConfirmFreePricing):
		// 409: the request itself is well formed, but local state has to change
		// first — business details, the business currency, a precision correction
		// the merchant has not confirmed yet, or a free-pricing intent the
		// merchant has not acknowledged.
		http.Error(w, err.Error(), 409)
	case errors.Is(err, owner.ErrCredentials):
		http.Error(w, err.Error(), 401)
	case errors.Is(err, orders.ErrNotFound):
		http.Error(w, err.Error(), 404)
	case errors.Is(err, orders.ErrInvalidTransition):
		// Status transitions outside the allowed state-machine graph — the
		// request was well-formed but the order's current state does not
		// permit it. 409 surfaces that an action exists, just not from here.
		http.Error(w, err.Error(), 409)
	case errors.Is(err, orders.ErrInvoiceAlreadyExists):
		// A duplicate invoice for the same order is a conflict: the request
		// was well-formed and authorised, but the desired state already exists.
		http.Error(w, err.Error(), 409)
	case errors.Is(err, owner.ErrNotFound), errors.Is(err, pricing.ErrNotFound), errors.Is(err, owner.ErrOperatorMissing), errors.Is(err, printers.ErrNotFound), errors.Is(err, idcards.ErrNotFound), errors.Is(err, passport.ErrNotFound), errors.Is(err, passport.ErrNoDocument):
		http.Error(w, err.Error(), 404)
	case errors.Is(err, owner.ErrOperatorExists):
		// A duplicate operator username is a conflict with the local state —
		// the request was well formed and authorised, but the requested
		// identity collides with one that is already in use.
		http.Error(w, err.Error(), 409)
	case errors.Is(err, owner.ErrThrottled):
		w.Header().Set("Retry-After", "64")
		http.Error(w, err.Error(), 429)
	default:
		http.Error(w, "local data operation failed", 500)
	}
}

// ownerSession validates the request's loopback origin, JSON content-type
// and CSRF token, then resolves the session cookie to a Subject that carries
// the role. Handlers that need role-based gating call RequirePermission on
// the returned Subject; handlers that do not (e.g. setup status) can ignore
// the role. The cookie value is returned alongside the subject so handlers
// can mint a fresh CSRF token if they need to echo one back to the browser.
func (s *Server) ownerSession(w http.ResponseWriter, r *http.Request) (string, owner.Subject, bool) {
	cookie, err := r.Cookie(ownerCookie)
	if err != nil {
		ownerError(w, owner.ErrCredentials)
		return "", owner.Subject{}, false
	}
	subject, err := s.owner.Resolve(r.Context(), cookie.Value)
	if err != nil {
		ownerError(w, err)
		return "", owner.Subject{}, false
	}
	if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(owner.Digest("csrf:"+cookie.Value))) != 1 {
		http.Error(w, "invalid CSRF token", 403)
		return "", owner.Subject{}, false
	}
	return cookie.Value, subject, true
}
func (s *Server) registerOwner(mux *http.ServeMux) {
	protect := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.localOwnerRequest(w, r) {
				fn(w, r)
			}
		}
	}
	mux.HandleFunc("GET /api/v1/setup/owner", protect(func(w http.ResponseWriter, r *http.Request) {
		exists, err := s.owner.Exists(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ownerExists": exists, "licenseRequired": s.licenseGate != nil})
	}))
	mux.HandleFunc("POST /api/v1/setup/owner", protect(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username   string `json:"username"`
			Password   string `json:"password"`
			SetupToken string `json:"setupToken"`
			LicenseKey string `json:"licenseKey"`
		}
		if !decodeOwner(w, r, &input) {
			return
		}
		if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(input.SetupToken), []byte(s.setupToken)) != 1 {
			http.Error(w, "invalid installation setup token", 403)
			return
		}
		if s.licenseGate != nil {
			if err := owner.ValidateCredentials(input.Username, input.Password); err != nil {
				ownerError(w, err)
				return
			}
			exists, err := s.owner.Exists(r.Context())
			if err != nil {
				ownerError(w, err)
				return
			}
			if exists {
				http.Error(w, "owner already exists; sign in to activate the licence", 409)
				return
			}
			if err = s.licenseGate.Activate(r.Context(), input.LicenseKey); err != nil {
				http.Error(w, err.Error(), 403)
				return
			}
		}
		if err := s.owner.Create(r.Context(), input.Username, input.Password); err != nil {
			ownerError(w, err)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]bool{"created": true})
	}))
	mux.HandleFunc("POST /api/v1/owner/login", protect(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeOwner(w, r, &input) {
			return
		}
		token, role, err := s.owner.Login(r.Context(), input.Username, input.Password)
		if err != nil {
			ownerError(w, err)
			return
		}
		// Plain HTTP is permitted only on this guarded loopback origin. LAN/tunnel
		// administration is not implemented; HTTPS uses Secure cookies automatically.
		http.SetCookie(w, &http.Cookie{Name: ownerCookie, Value: token, Path: "/api/v1/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
		_ = json.NewEncoder(w).Encode(map[string]string{"csrfToken": owner.Digest("csrf:" + token), "role": string(role)})
	}))
	mux.HandleFunc("GET /api/v1/owner/session", protect(func(w http.ResponseWriter, r *http.Request) {
		token, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"username": subject.Name, "role": string(subject.Role), "csrfToken": owner.Digest("csrf:" + token)})
	}))
	mux.HandleFunc("POST /api/v1/owner/logout", protect(func(w http.ResponseWriter, r *http.Request) {
		token, _, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		if err := s.owner.Logout(r.Context(), token); err != nil {
			ownerError(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: ownerCookie, Value: "", Path: "/api/v1/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		_ = json.NewEncoder(w).Encode(map[string]bool{"signedOut": true})
	}))
	mux.HandleFunc("GET /api/v1/owner/business", protect(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.ownerSession(w, r); !ok {
			return
		}
		profile, err := s.owner.Profile(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(newBusinessView(profile))
	}))
	mux.HandleFunc("PUT /api/v1/owner/business", protect(func(w http.ResponseWriter, r *http.Request) {
		_, subject, ok := s.ownerSession(w, r)
		if !ok {
			return
		}
		// The business profile is the merchant identity: only the owner may
		// change it. Operators who try to call this endpoint receive 403
		// without seeing whether the request body was well formed.
		if !owner.RequirePermission(w, subject, owner.CanEditBusiness) {
			return
		}
		var profile owner.Profile
		if !decodeOwner(w, r, &profile) {
			return
		}
		if err := s.owner.SaveProfile(r.Context(), profile); err != nil {
			ownerError(w, err)
			return
		}
		stored, err := s.owner.Profile(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(newBusinessView(stored))
	}))
}
