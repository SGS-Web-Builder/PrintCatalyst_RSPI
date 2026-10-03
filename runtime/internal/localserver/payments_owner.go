package localserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
	"io"
	"net/http"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
)

// WithPayments wires the payments service. Required for the
// /api/v1/owner/payments endpoints; safe to omit in builds that
// disable payments (the routes silently 503).
func WithPayments(svc *payments.Service) Option {
	return func(s *Server) { s.payments = svc }
}

// registerOwnerPayments wires the payment endpoints onto the owner
// mux.
func (s *Server) registerOwnerPayments(mux *http.ServeMux) {
	s.registerPaymentConnection(mux)
	mux.HandleFunc("/api/v1/owner/payments/providers", s.handleOwnerPaymentsProviders)
	mux.HandleFunc("PUT /api/v1/owner/payments/providers/{id}", s.handleOwnerPaymentsProviderItem)
	mux.HandleFunc("DELETE /api/v1/owner/payments/providers/{id}", s.handleOwnerPaymentsProviderItem)
	mux.HandleFunc("POST /api/v1/owner/payments/providers/test", s.handleOwnerPaymentsProvidersTest)
	// Setup diagnostics are owner-only; only the signed webhook receiver is public.
	mux.HandleFunc("GET /api/v1/owner/payments/providers/{id}/webhook-url", s.protectOwnerView(s.handleOwnerPaymentsProviderWebhookURL))
	mux.HandleFunc("POST /api/v1/owner/payments/providers/{id}/webhook-test", s.protectOwnerEdit(s.handleOwnerPaymentsProviderWebhookTest))
	mux.HandleFunc("/api/v1/owner/payments/intents", s.handleOwnerPaymentsIntents)
	mux.HandleFunc("POST /api/v1/owner/payments/intents/{id}/action", s.handleOwnerPaymentsIntentAction)
	mux.HandleFunc("/api/v1/owner/payments/ledger", s.handleOwnerPaymentsLedger)
	mux.HandleFunc("POST /api/v1/owner/payments/webhook", s.handleOwnerPaymentsWebhook)
}

func (s *Server) handleOwnerPaymentsProviders(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(s.handleOwnerPaymentsProvidersList)(w, r)
	case http.MethodPost:
		s.protectOwnerEdit(s.handleOwnerPaymentsProvidersCreate)(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerPaymentsProviderItem(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "provider id is required", 400)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			var request paymentsProviderCreateRequest
			if !decodeOwnerJSON(w, r, &request, 16<<10) {
				return
			}
			if request.Kind == "razorpay_merchant" && (request.Enabled || request.Secret != "") && !s.requirePaymentOrigin(w, r) {
				return
			}
			provider, err := s.payments.UpdateProvider(r.Context(), id, payments.ProviderInput{
				Kind:        payments.Kind(request.Kind),
				DisplayName: request.DisplayName,
				Enabled:     request.Enabled,
				IsDefault:   request.IsDefault,
				Config:      request.Config,
				Secret:      request.Secret,
			})
			if err != nil {
				paymentsOwnerError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(provider)
		})(w, r)
	case http.MethodDelete:
		s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
			if err := s.payments.DeleteProvider(r.Context(), id); err != nil {
				paymentsOwnerError(w, err)
				return
			}
			w.WriteHeader(204)
		})(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerPaymentsProvidersList(w http.ResponseWriter, r *http.Request) {
	// Seed the platform Razorpay provider so the dashboard always
	// shows it. The seeding is idempotent.
	if _, err := s.payments.EnsurePlatformProvider(r.Context()); err != nil {
		paymentsOwnerError(w, err)
		return
	}
	providers, err := s.payments.ListProviders(r.Context())
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"providers": providers})
}

type paymentsProviderCreateRequest struct {
	Kind        string         `json:"kind"`
	DisplayName string         `json:"displayName"`
	Enabled     bool           `json:"enabled"`
	IsDefault   bool           `json:"isDefault"`
	Config      map[string]any `json:"config"`
	Secret      string         `json:"secret"`
}

// handleOwnerPaymentsProvidersTest verifies a merchant-supplied
// Razorpay credential blob (key id, key secret, webhook secret)
// against the live Razorpay API without persisting it. The
// dashboard's payment-setup wizard calls this from the "Test
// connection" button so the merchant can confirm the pasted keys
// are valid before saving. The endpoint accepts the same JSON
// payload the CreateProvider endpoint expects in its `secret`
// field; the body is never written to the database.
func (s *Server) handleOwnerPaymentsProvidersTest(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var request paymentsProviderCreateRequest
	if !decodeOwnerJSON(w, r, &request, 16<<10) {
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		if !s.requirePaymentOrigin(w, r) {
			return
		}
		if err := s.payments.TestProviderConnection(r.Context(), []byte(request.Secret)); err != nil {
			paymentsOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})(w, r)
}

func (s *Server) handleOwnerPaymentsProvidersCreate(w http.ResponseWriter, r *http.Request) {
	var request paymentsProviderCreateRequest
	if !decodeOwnerJSON(w, r, &request, 16<<10) {
		return
	}
	if request.Kind == "razorpay_merchant" && !s.requirePaymentOrigin(w, r) {
		return
	}
	provider, err := s.payments.CreateProvider(r.Context(), payments.ProviderInput{
		Kind:        payments.Kind(request.Kind),
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		IsDefault:   request.IsDefault,
		Config:      request.Config,
		Secret:      request.Secret,
	})
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(provider)
}

func (s *Server) handleOwnerPaymentsIntents(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.protectOwnerView(s.handleOwnerPaymentsIntentsList)(w, r)
	case http.MethodPost:
		s.protectOwnerEdit(s.handleOwnerPaymentsIntentCreate)(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleOwnerPaymentsIntentsList(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := parseLimit(raw)
		if err != nil {
			http.Error(w, "invalid limit", 400)
			return
		}
		limit = parsed
	}
	intents, err := s.payments.ListIntents(r.Context(), limit)
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"intents": intents})
}

type paymentsIntentCreateRequest struct {
	OrderID            string `json:"orderId"`
	ProviderID         string `json:"providerId"`
	AmountMinor        int64  `json:"amountMinor"`
	Currency           string `json:"currency"`
	CurrencyMinorUnits int    `json:"currencyMinorUnits"`
	CustomerName       string `json:"customerName"`
	CustomerPhone      string `json:"customerPhone"`
	CustomerEmail      string `json:"customerEmail"`
	IdempotencyKey     string `json:"idempotencyKey"`
}

func (s *Server) handleOwnerPaymentsIntentCreate(w http.ResponseWriter, r *http.Request) {
	var request paymentsIntentCreateRequest
	if !decodeOwnerJSON(w, r, &request, 4<<10) {
		return
	}
	intent, result, err := s.payments.CreateIntent(r.Context(), payments.IntentInput{
		OrderID:            request.OrderID,
		ProviderID:         request.ProviderID,
		AmountMinor:        request.AmountMinor,
		Currency:           request.Currency,
		CurrencyMinorUnits: request.CurrencyMinorUnits,
		CustomerName:       request.CustomerName,
		CustomerPhone:      request.CustomerPhone,
		CustomerEmail:      request.CustomerEmail,
		IdempotencyKey:     request.IdempotencyKey,
	})
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(map[string]any{"intent": intent, "result": result})
}

type paymentsManualRequest struct {
	Method      string `json:"method"`
	Reference   string `json:"reference"`
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
	Note        string `json:"note"`
	Reject      bool   `json:"reject"`
}

func (s *Server) handleOwnerPaymentsIntentAction(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	intentID := r.PathValue("id")
	if intentID == "" {
		http.Error(w, "intent id is required", 400)
		return
	}
	s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var request paymentsManualRequest
		if !decodeOwnerJSON(w, r, &request, 4<<10) {
			return
		}
		actor, _ := s.subjectName(w, r)
		if actor == "" {
			actor = "owner"
		}
		if request.Reject {
			if err := s.payments.ManualReject(r.Context(), intentID, actor, request.Note); err != nil {
				paymentsOwnerError(w, err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rejected": true})
			return
		}
		approval, err := s.payments.ManualApprove(r.Context(), intentID, payments.Method(request.Method), request.Reference, request.AmountMinor, request.Currency, actor, request.Note)
		if err != nil {
			paymentsOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"approval": approval})
	})(w, r)
}

func (s *Server) handleOwnerPaymentsLedger(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := parseLimit(raw)
			if err != nil {
				http.Error(w, "invalid limit", 400)
				return
			}
			limit = parsed
		}
		entries, err := s.payments.Ledger(r.Context(), limit)
		if err != nil {
			paymentsOwnerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
	})(w, r)
}

// handleOwnerPaymentsWebhook accepts a webhook POST from a
// merchant-owned gateway. The signature is the gateway's value; the
// adapter verifies it against the provider's stored secret.
func (s *Server) handleOwnerPaymentsWebhook(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	providerID := r.URL.Query().Get("provider")
	if providerID == "" {
		http.Error(w, "provider query parameter required", 400)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		http.Error(w, "webhook body too large", 413)
		return
	}
	signature := r.Header.Get("X-Razorpay-Signature")
	if signature == "" {
		signature = r.Header.Get("X-Provider-Signature")
	}
	event, err := s.payments.RecordWebhook(r.Context(), providerID, body, signature)
	if errors.Is(err, payments.ErrDuplicate) {
		writeJSON(w, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	w.WriteHeader(202)
	_ = json.NewEncoder(w).Encode(event)
}

// handleOwnerPaymentsProviderWebhookURL returns the full webhook URL the
// merchant must register in their Razorpay dashboard, plus the list of
// Razorpay event types that the runtime's adapter can consume. The URL
// is derived from the runtime's configured public origin so the merchant
// can copy-paste it directly. The handler checks that a public origin
// has been configured; if not it returns 409 so the dashboard can
// render a clear "configure the tunnel first" message.
func (s *Server) handleOwnerPaymentsProviderWebhookURL(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	providerID := r.PathValue("id")
	if providerID == "" {
		http.Error(w, "provider id is required", 400)
		return
	}
	providers, err := s.payments.ListProviders(r.Context())
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	var providerConfig *payments.ProviderConfig
	for i := range providers {
		if providers[i].ID == providerID {
			providerConfig = &providers[i]
			break
		}
	}
	if providerConfig == nil {
		http.Error(w, "provider not found", 404)
		return
	}
	origin, err := s.paymentOrigin(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	webhookURL := origin + "/api/v1/owner/payments/webhook?provider=" + providerID

	// The events this adapter can consume. Razorpay only fires webhooks
	// for events the dashboard is subscribed to; we surface this list
	// so the merchant knows exactly what to enable in the Razorpay
	// dashboard without reading documentation.
	events := []string{"payment_link.paid", "payment_link.cancelled", "payment_link.expired"}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"providerId":   providerID,
		"webhookURL":   webhookURL,
		"publicOrigin": origin,
		"events":       events,
	})
}

// This read-only diagnostic checks API credentials and domain identity. It does
// not fabricate a payment or claim to validate the merchant's Razorpay dashboard.
func (s *Server) handleOwnerPaymentsProviderWebhookTest(w http.ResponseWriter, r *http.Request) {
	if s.payments == nil {
		http.Error(w, "payments service unavailable", 503)
		return
	}
	secret, err := s.payments.LoadSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		paymentsOwnerError(w, err)
		return
	}
	defer clear(secret)
	if len(secret) == 0 {
		http.Error(w, "No stored merchant secret; save Razorpay credentials first", 409)
		return
	}
	origin, err := s.paymentOrigin(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	if err = s.payments.TestProviderConnection(r.Context(), secret); err != nil {
		paymentsOwnerError(w, err)
		return
	}
	probe := tunnel.Verify(r.Context(), origin, tunnel.WithInstallation(s.connectionInstallationID()))
	if probe.Status != tunnel.StatusOnline {
		writeJSON(w, map[string]any{"ok": false, "error": probe.Error})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "API credentials work and the domain reaches this installation. This does not verify Razorpay webhook registration. Complete a Razorpay Test Mode payment and confirm the order is marked paid before using Live Mode."})
}

// parseRazorpayCredentials decodes the secret blob that
// payments.Service stores for a Razorpay merchant provider.
// Mirrors the shape razorpay.ParseCredentials accepts.
func parseRazorpayCredentials(secret []byte) (struct {
	KeyID         string `json:"key_id"`
	KeySecret     string `json:"key_secret"`
	WebhookSecret string `json:"webhook_secret"`
}, error) {
	var c struct {
		KeyID         string `json:"key_id"`
		KeySecret     string `json:"key_secret"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if len(secret) == 0 {
		return c, fmt.Errorf("empty secret")
	}
	if err := json.Unmarshal(secret, &c); err != nil {
		return c, err
	}
	return c, nil
}

// paymentsOwnerError maps sentinel errors to HTTP status codes.
func paymentsOwnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payments.ErrInvalid):
		http.Error(w, err.Error(), 400)
	case errors.Is(err, payments.ErrNotFound):
		http.Error(w, err.Error(), 404)
	case errors.Is(err, payments.ErrDuplicate):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, payments.ErrUnconfigured), errors.Is(err, payments.ErrManualDisabled), errors.Is(err, payments.ErrTransition):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, payments.ErrSignature), errors.Is(err, payments.ErrMismatch), errors.Is(err, payments.ErrAmount), errors.Is(err, payments.ErrCurrency), errors.Is(err, payments.ErrWebhook):
		http.Error(w, err.Error(), 422)
	default:
		http.Error(w, err.Error(), 500)
	}
}
