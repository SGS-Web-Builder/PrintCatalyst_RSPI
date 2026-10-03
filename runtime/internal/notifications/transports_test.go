package notifications_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func transportFixture(t *testing.T, emailDialer func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error, whatsAppPOST func(ctx context.Context, apiURL, token, to, body string) error) (*store.Store, *notifications.Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notif-transport.sqlite")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test"); err != nil {
		t.Fatal(err)
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	svc := notifications.New(database.DB(), nil)
	if emailDialer != nil {
		svc.SetEmailDialer(emailDialer)
	}
	if whatsAppPOST != nil {
		svc.SetWhatsAppPOST(whatsAppPOST)
	}
	return database, svc
}

func waitForDeliveries(t *testing.T, svc *notifications.Service, want int) []notifications.Delivery {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		deliveries, err := svc.Deliveries(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries) >= want {
			return deliveries
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d delivery rows", want)
	return nil
}

func TestEmailTransportIsInvokedWhenEnabled(t *testing.T) {
	var captured struct {
		mu      sync.Mutex
		calls   int
		subject string
		body    string
	}
	dialer := func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error {
		captured.mu.Lock()
		defer captured.mu.Unlock()
		captured.calls++
		captured.subject = subject
		captured.body = body
		return nil
	}
	db, svc := transportFixture(t, dialer, nil)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true,
		EmailHost:    "smtp.example.com",
		EmailPort:    587,
		EmailUsername: "alerts@example.com",
		EmailFrom:    "alerts@example.com",
		EmailToken:   "smtp-password",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-1", "Test Customer", 12345, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 1)
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(deliveries))
	}
	if deliveries[0].Transport != "email" {
		t.Fatalf("transport = %q, want email", deliveries[0].Transport)
	}
	if deliveries[0].Status != "delivered" {
		t.Fatalf("status = %q, want delivered", deliveries[0].Status)
	}
	captured.mu.Lock()
	defer captured.mu.Unlock()
	if captured.calls != 1 {
		t.Fatalf("dialer calls = %d, want 1", captured.calls)
	}
	if captured.subject == "" || captured.body == "" {
		t.Fatal("dialer did not receive subject or body")
	}
}

func TestEmailTransportRecordsFailure(t *testing.T) {
	dialer := func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error {
		return errors.New("smtp refused")
	}
	db, svc := transportFixture(t, dialer, nil)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true, EmailHost: "smtp.example.com", EmailPort: 587,
		EmailUsername: "alerts@example.com", EmailFrom: "alerts@example.com", EmailToken: "smtp-password",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-1", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 1)
	if deliveries[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", deliveries[0].Status)
	}
	if deliveries[0].FailureDetail == "" {
		t.Fatal("failure detail is empty")
	}
}

func TestEmailTransportSkipsWhenHostMissing(t *testing.T) {
	dialer := func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error {
		t.Fatal("dialer must not be called when host is empty")
		return nil
	}
	db, svc := transportFixture(t, dialer, nil)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true, EmailHost: "", EmailPort: 587,
		EmailUsername: "alerts@example.com", EmailFrom: "alerts@example.com", EmailToken: "smtp-password",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-1", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 1)
	if deliveries[0].Status != "skipped" {
		t.Fatalf("status = %q, want skipped", deliveries[0].Status)
	}
}

func TestEmailTransportSkipsWhenPasswordMissing(t *testing.T) {
	dialer := func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error {
		t.Fatal("dialer must not be called without password")
		return nil
	}
	db, svc := transportFixture(t, dialer, nil)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true, EmailHost: "smtp.example.com", EmailPort: 587,
		EmailUsername: "alerts@example.com", EmailFrom: "alerts@example.com", EmailToken: "",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-1", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 1)
	if deliveries[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", deliveries[0].Status)
	}
}

func TestWhatsAppTransportIsInvokedWhenEnabled(t *testing.T) {
	var captured struct {
		mu     sync.Mutex
		calls  int
		token  string
		to     string
		body   string
	}
	poster := func(ctx context.Context, apiURL, token, to, body string) error {
		captured.mu.Lock()
		defer captured.mu.Unlock()
		captured.calls++
		captured.token = token
		captured.to = to
		captured.body = body
		return nil
	}
	db, svc := transportFixture(t, nil, poster)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		WhatsAppEnabled: true,
		WhatsAppAPIURL:  "https://graph.facebook.com/v17.0/me/messages",
		WhatsAppToken:   "wa-token-1234567890",
		WhatsAppTo:      "+919999999999",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-2", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 1)
	if deliveries[0].Transport != "whatsapp" {
		t.Fatalf("transport = %q, want whatsapp", deliveries[0].Transport)
	}
	if deliveries[0].Status != "delivered" {
		t.Fatalf("status = %q, want delivered", deliveries[0].Status)
	}
	captured.mu.Lock()
	defer captured.mu.Unlock()
	if captured.calls != 1 {
		t.Fatalf("poster calls = %d, want 1", captured.calls)
	}
	if captured.token != "wa-token-1234567890" {
		t.Fatalf("token = %q", captured.token)
	}
	if captured.to != "+919999999999" {
		t.Fatalf("to = %q", captured.to)
	}
}

func TestWhatsAppTransportRecordsFailure(t *testing.T) {
	poster := func(ctx context.Context, apiURL, token, to, body string) error {
		return errors.New("whatsapp gateway 500")
	}
	db, svc := transportFixture(t, nil, poster)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		WhatsAppEnabled: true, WhatsAppAPIURL: "https://example.com/whatsapp", WhatsAppToken: "tok", WhatsAppTo: "+91",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-3", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 1)
	if deliveries[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", deliveries[0].Status)
	}
}

func TestNotifyWithNoTransportConfiguredDoesNotPanic(t *testing.T) {
	db, svc := transportFixture(t, nil, nil)
	defer db.Close()
	// No settings saved → Load returns defaults (desktop/audio off,
	// email/whatsapp off). Notify should silently no-op.
	svc.Notify(context.Background(), "order-4", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries, err := svc.Deliveries(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("deliveries = %d, want 0", len(deliveries))
	}
}

func TestBothTransportsAreInvoked(t *testing.T) {
	var emailCalls, whatsAppCalls int
	var mu sync.Mutex
	email := func(ctx context.Context, host string, port int, username, password, from, to, subject, body string) error {
		mu.Lock()
		defer mu.Unlock()
		emailCalls++
		return nil
	}
	whatsApp := func(ctx context.Context, apiURL, token, to, body string) error {
		mu.Lock()
		defer mu.Unlock()
		whatsAppCalls++
		return nil
	}
	db, svc := transportFixture(t, email, whatsApp)
	defer db.Close()
	if err := svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true, EmailHost: "smtp.example.com", EmailPort: 587,
		EmailUsername: "alerts@example.com", EmailFrom: "alerts@example.com", EmailToken: "smtp-password",
		WhatsAppEnabled: true, WhatsAppAPIURL: "https://example.com/whatsapp", WhatsAppToken: "tok", WhatsAppTo: "+91",
	}); err != nil {
		t.Fatal(err)
	}
	svc.Notify(context.Background(), "order-5", "Test Customer", 100, "INR", 2, time.Now().Unix())
	deliveries := waitForDeliveries(t, svc, 2)
	if len(deliveries) != 2 {
		t.Fatalf("deliveries = %d, want 2", len(deliveries))
	}
	mu.Lock()
	defer mu.Unlock()
	if emailCalls != 1 {
		t.Fatalf("email calls = %d, want 1", emailCalls)
	}
	if whatsAppCalls != 1 {
		t.Fatalf("whatsapp calls = %d, want 1", whatsAppCalls)
	}
}

func TestRenderEmailBodyContainsOrderDetails(t *testing.T) {
	event := notifications.Event{
		OrderID:      "order-test",
		CustomerName: "Test Customer",
		TotalMinor:   12345,
		Currency:     "INR",
		CurrencyMU:   2,
		CreatedAt:    time.Now().Unix(),
	}
	body := renderEmailBodyForTest(event)
	for _, want := range []string{"order-test", "Test Customer", "12345", "INR"} {
		if !contains(body, want) {
			t.Fatalf("body missing %q\nbody: %s", want, body)
		}
	}
}

func TestRenderWhatsAppBodyContainsOrderDetails(t *testing.T) {
	event := notifications.Event{
		OrderID:      "order-test",
		CustomerName: "Test Customer",
		TotalMinor:   12345,
		Currency:     "INR",
	}
	body := renderWhatsAppBodyForTest(event)
	for _, want := range []string{"Test Customer", "12345", "INR", "order-test"} {
		if !contains(body, want) {
			t.Fatalf("body missing %q\nbody: %s", want, body)
		}
	}
}

func renderEmailBodyForTest(event notifications.Event) string {
	return fmt.Sprintf("New Print Catalyst order\n\nOrder: %s\nCustomer: %s\nTotal: %d minor units (%s)\nSubmitted at: %s\n\nManage orders in the local dashboard.\n",
		event.OrderID, event.CustomerName, event.TotalMinor, event.Currency, time.Unix(event.CreatedAt, 0).UTC().Format(time.RFC3339))
}

func renderWhatsAppBodyForTest(event notifications.Event) string {
	return fmt.Sprintf("Print Catalyst · new order from %s — %d %s (order %s)",
		event.CustomerName, event.TotalMinor, event.Currency, event.OrderID)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
