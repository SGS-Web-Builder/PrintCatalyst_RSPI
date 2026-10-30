package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/fleet"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Optional metadata-only sender. Device/customer files, OTPs and payment details
// are deliberately absent from the fleet heartbeat contract.
func (s *Server) sendFleetHeartbeat(ctx context.Context) {
	endpoint := os.Getenv("PC_FLEET_URL")
	keyFile := os.Getenv("PC_FLEET_TOKEN_FILE")
	if endpoint == "" || keyFile == "" {
		return
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return
	}
	key := strings.TrimSpace(string(raw))
	if len(key) < 32 {
		return
	}
	snap := fleet.Snapshot{Version: "remote-operations-v1"}
	if s.db.QueryRowContext(ctx, "SELECT paused FROM kiosk_control WHERE singleton=1").Scan(&snap.Paused) != nil {
		return
	}
	if s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM printers WHERE enabled=1 AND removed_at IS NULL").Scan(&snap.Printers) != nil {
		return
	}
	if s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE submitted_at>0 AND status NOT IN ('completed','cancelled')").Scan(&snap.PendingOrders) != nil {
		return
	}
	if s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM printer_telemetry WHERE checked_at<? OR json_extract(payload,'$.state') IN ('stopped','unavailable')", time.Now().Add(-90*time.Second).Unix()).Scan(&snap.Alerts) != nil {
		return
	}
	body, _ := json.Marshal(snap)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(endpoint, "/")+"/api/heartbeat", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err == nil {
		res.Body.Close()
	}
}
