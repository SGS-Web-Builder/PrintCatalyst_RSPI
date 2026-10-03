package localserver

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
)

// registerOwnerNotifications wires the owner-facing notifications endpoints:
// settings read/write and SSE stream for real-time order alerts.
func (s *Server) registerOwnerNotifications(mux *http.ServeMux) {
	// GET /api/v1/owner/notifications/settings  — read settings (owner + operator)
	mux.HandleFunc("GET /api/v1/owner/notifications/settings", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.notifications.Load(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		// Never surface API tokens in responses.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"desktopAlertsEnabled": settings.DesktopAlerts,
			"audioAlertsEnabled":  settings.AudioAlerts,
			"emailEnabled":        settings.EmailEnabled,
			"emailHost":           settings.EmailHost,
			"emailPort":           settings.EmailPort,
			"emailUsername":       settings.EmailUsername,
			"emailFrom":           settings.EmailFrom,
			"whatsappEnabled":    settings.WhatsAppEnabled,
			"whatsappApiUrl":     settings.WhatsAppAPIURL,
			"whatsappTo":         settings.WhatsAppTo,
			"updatedAt":          settings.UpdatedAt,
		})
	}))

	// PUT /api/v1/owner/notifications/settings  — update settings (owner only)
	mux.HandleFunc("PUT /api/v1/owner/notifications/settings", s.protectOwnerEdit(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			DesktopAlerts    bool   `json:"desktopAlertsEnabled"`
			AudioAlerts     bool   `json:"audioAlertsEnabled"`
			EmailEnabled    bool   `json:"emailEnabled"`
			EmailHost       string `json:"emailHost"`
			EmailPort       int    `json:"emailPort"`
			EmailUsername   string `json:"emailUsername"`
			EmailFrom       string `json:"emailFrom"`
			EmailToken      string `json:"emailToken,omitempty"`
			WhatsAppEnabled bool   `json:"whatsappEnabled"`
			WhatsAppAPIURL  string `json:"whatsappApiUrl"`
			WhatsAppTo      string `json:"whatsappTo"`
			WhatsAppToken   string `json:"whatsappToken,omitempty"`
		}
		if !decodeOwnerJSON(w, r, &input, 16<<10) {
			return
		}
		// Load current settings to retain existing tokens if not updated.
		cur, err := s.notifications.Load(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		emailToken := input.EmailToken
		if emailToken == "" {
			emailToken = cur.EmailToken
		}
		whatsappToken := input.WhatsAppToken
		if whatsappToken == "" {
			whatsappToken = cur.WhatsAppToken
		}
		if err := s.notifications.Save(r.Context(), notifications.Settings{
			DesktopAlerts:   input.DesktopAlerts,
			AudioAlerts:    input.AudioAlerts,
			EmailEnabled:   input.EmailEnabled,
			EmailHost:       input.EmailHost,
			EmailPort:       input.EmailPort,
			EmailUsername:   input.EmailUsername,
			EmailFrom:       input.EmailFrom,
			EmailToken:      emailToken,
			WhatsAppEnabled: input.WhatsAppEnabled,
			WhatsAppAPIURL: input.WhatsAppAPIURL,
			WhatsAppTo:     input.WhatsAppTo,
			WhatsAppToken:  whatsappToken,
		}); err != nil {
			ownerError(w, err)
			return
		}
		updated, err := s.notifications.Load(r.Context())
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"desktopAlertsEnabled": updated.DesktopAlerts,
			"audioAlertsEnabled":  updated.AudioAlerts,
			"emailEnabled":        updated.EmailEnabled,
			"emailHost":           updated.EmailHost,
			"emailPort":           updated.EmailPort,
			"emailUsername":       updated.EmailUsername,
			"emailFrom":           updated.EmailFrom,
			"whatsappEnabled":    updated.WhatsAppEnabled,
			"whatsappApiUrl":     updated.WhatsAppAPIURL,
			"whatsappTo":         updated.WhatsAppTo,
			"updatedAt":          updated.UpdatedAt,
		})
	}))

	// GET /api/v1/owner/notifications/stream  — SSE stream of new-order events
	mux.HandleFunc("GET /api/v1/owner/notifications/stream", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		sessionID, events := s.notifications.Subscribe()
		defer s.notifications.Unsubscribe(sessionID)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", 500)
			return
		}

		heartbeat := time.NewTicker(25 * time.Second)
		defer heartbeat.Stop()

		notify := w.(http.CloseNotifier).CloseNotify()

		// First event: acknowledge the session.
		writeSSEEvent(w, "connected", map[string]string{"session": sessionID})
		flusher.Flush()

		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				data, err := notifications.MarshalEvent(event)
				if err != nil {
					continue
				}
				io.WriteString(w, "event: new-order\n")
				io.WriteString(w, "data: "+string(data)+"\n\n")
				flusher.Flush()
			case <-heartbeat.C:
				// Server-sent events comment keeps proxies and browsers alive.
				io.WriteString(w, ": heartbeat\n\n")
				flusher.Flush()
			case <-notify:
				return
			}
		}
	}))

	// GET /api/v1/owner/notifications/deliveries — recent transport deliveries (owner + operator)
	mux.HandleFunc("GET /api/v1/owner/notifications/deliveries", s.protectOwnerView(func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := parseLimit(raw)
			if err != nil {
				http.Error(w, "invalid limit", 400)
				return
			}
			limit = parsed
		}
		deliveries, err := s.notifications.Deliveries(r.Context(), limit)
		if err != nil {
			ownerError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deliveries": deliveries})
	}))
}

// writeSSEEvent writes one SSE "event:" line and a "data:" block.
func writeSSEEvent(w io.Writer, eventType string, data map[string]string) {
	bs, _ := json.Marshal(data)
	io.WriteString(w, "event: "+eventType+"\n")
	io.WriteString(w, "data: "+string(bs)+"\n\n")
}
