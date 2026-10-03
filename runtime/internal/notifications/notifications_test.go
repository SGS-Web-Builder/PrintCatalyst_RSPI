package notifications_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func fixture(t *testing.T) (*store.Store, *notifications.Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notif.sqlite")
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
	return database, notifications.New(database.DB(), nil)
}

func TestLoadReturnsDefaultSettings(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	settings, err := svc.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.DesktopAlerts {
		t.Fatal("desktop alerts should default to true")
	}
	if !settings.AudioAlerts {
		t.Fatal("audio alerts should default to true")
	}
}

func TestSaveUpdatesDesktopAndAudioFlags(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	ctx := context.Background()
	err := svc.Save(ctx, notifications.Settings{
		DesktopAlerts: false,
		AudioAlerts:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := svc.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.DesktopAlerts {
		t.Fatal("desktop alerts should be false after save")
	}
	if settings.AudioAlerts {
		t.Fatal("audio alerts should be false after save")
	}
}

func TestSaveValidatesEmailPort(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	// Email disabled: port 0 is OK.
	err := svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: false,
		EmailPort:    0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Email enabled: invalid port must error with ErrInvalid.
	err = svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true,
		EmailPort:    0,
	})
	if !errors.Is(err, notifications.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
	err = svc.Save(context.Background(), notifications.Settings{
		EmailEnabled: true,
		EmailPort:    65536,
	})
	if !errors.Is(err, notifications.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestNotifyBroadcastsToSubscribers(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()

	sessionID, events := svc.Subscribe()
	defer svc.Unsubscribe(sessionID)

	// Fire a notification.
	svc.Notify(context.Background(), "ord123", "Ravi", 500, "INR", 2, 1700000000)

	select {
	case event := <-events:
		if event.OrderID != "ord123" {
			t.Fatalf("order id = %s, want ord123", event.OrderID)
		}
		if event.CustomerName != "Ravi" {
			t.Fatalf("customer = %s, want Ravi", event.CustomerName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for notification event")
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()
	sessionID, events := svc.Subscribe()
	svc.Unsubscribe(sessionID)
	// Channel should be closed; sending should not panic (broadcast is non-blocking).
	svc.Notify(context.Background(), "ord456", "Priya", 300, "INR", 2, 1700000000)
	// If the channel was properly closed, the select should timeout or return immediately.
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected closed channel")
		}
	default:
		// Channel drained already.
	}
}

// TestSubscribeReturnsUniqueHexSessionIDs guards against a regression
// where Subscribe returned a single Unicode rune for the session id.
// string(rune(int64)) silently truncates to int32 and produces invalid
// UTF-8 once the counter exceeds 0x10FFFF, and produces the same id
// twice if two sessions ever land on the same rune value.
//
// The current implementation derives an 8-byte hex string from
// crypto/rand; this test asserts every Subscribe call yields a fresh,
// 16-character, lowercase-hex identifier that Unsubscribe can close.
func TestSubscribeReturnsUniqueHexSessionIDs(t *testing.T) {
	db, svc := fixture(t)
	defer db.Close()

	const created = 64
	ids := make([]string, 0, created)
	channels := make([]<-chan notifications.Event, 0, created)
	seen := make(map[string]struct{}, created)
	for i := 0; i < created; i++ {
		id, ch := svc.Subscribe()
		if len(id) != 16 {
			t.Fatalf("session id = %q (len %d), want 16-character hex", id, len(id))
		}
		for _, r := range id {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Fatalf("session id %q contains non-hex character %q", id, r)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("session id %q returned twice in a row", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		channels = append(channels, ch)
	}
	for i, id := range ids {
		svc.Unsubscribe(id)
		// Unsubscribe must close exactly the channel Subscribe returned.
		// Pulling from the buffered channel after close yields the zero
		// value with ok=false.
		select {
		case _, ok := <-channels[i]:
			if ok {
				t.Fatalf("channel for %q still delivered a value after Unsubscribe", id)
			}
		default:
			t.Fatalf("channel for %q was not closed by Unsubscribe", id)
		}
	}
}
