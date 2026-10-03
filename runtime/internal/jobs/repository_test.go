package jobs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func TestCreateCommitsJobAndOutboxTogether(t *testing.T) {
	database := openTestStore(t)
	repository := New(database.DB(), sequenceIDs("job-1", "event-1"))

	job, err := repository.Create(context.Background(), CreateInput{
		LocalOrderID: "ORDER-0001", TotalMinor: 1250, Currency: "INR",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if job.ID != "job-1" || job.Status != StatusPending {
		t.Fatalf("job = %#v", job)
	}
	var aggregateID, eventType string
	if err := database.DB().QueryRow(`SELECT aggregate_id, event_type FROM outbox_events WHERE id = 'event-1'`).Scan(&aggregateID, &eventType); err != nil {
		t.Fatal(err)
	}
	if aggregateID != job.ID || eventType != "job.created" {
		t.Fatalf("outbox aggregate=%q event=%q", aggregateID, eventType)
	}
}

func TestCreateRollsBackJobWhenOutboxInsertFails(t *testing.T) {
	database := openTestStore(t)
	if _, err := database.DB().Exec(`
INSERT INTO outbox_events (id, aggregate_id, event_type, payload, status, available_at, created_at)
VALUES ('existing', 'job-fixed', 'job.created', '{}', 'pending', '2026-09-06T00:00:00Z', '2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	repository := New(database.DB(), sequenceIDs("job-fixed", "event-fixed"))
	if _, err := repository.Create(context.Background(), CreateInput{
		LocalOrderID: "ORDER-ROLLBACK", TotalMinor: 500, Currency: "INR",
	}); err == nil {
		t.Fatal("Create() succeeded, want outbox uniqueness failure")
	}
	var count int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM print_jobs WHERE id = 'job-fixed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphan job count = %d", count)
	}
}

func TestPendingOutboxSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "print-catalyst.sqlite")
	first, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(first.DB(), sequenceIDs("job-restart", "event-restart"))
	if _, err := repository.Create(context.Background(), CreateInput{
		LocalOrderID: "ORDER-RESTART", TotalMinor: 700, Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	events, err := New(second.DB()).ClaimOutbox(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "event-restart" || events[0].AggregateID != "job-restart" {
		t.Fatalf("events = %#v", events)
	}
}

func TestCreateRejectsInvalidMoneyBeforeWriting(t *testing.T) {
	database := openTestStore(t)
	repository := New(database.DB())
	for _, input := range []CreateInput{
		{LocalOrderID: "NEGATIVE", TotalMinor: -1, Currency: "INR"},
		{LocalOrderID: "BAD-CURRENCY", TotalMinor: 1, Currency: "RUPEES"},
	} {
		if _, err := repository.Create(context.Background(), input); err == nil {
			t.Fatalf("Create(%#v) succeeded", input)
		}
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "print-catalyst.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func sequenceIDs(values ...string) func() string {
	index := 0
	return func() string {
		value := values[index]
		index++
		return value
	}
}
