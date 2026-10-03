package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const StatusPending = "pending"

type CreateInput struct {
	LocalOrderID string
	TotalMinor   int64
	Currency     string
}

type Job struct {
	ID           string
	LocalOrderID string
	Status       string
	TotalMinor   int64
	Currency     string
	CreatedAt    time.Time
}

type OutboxEvent struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     string
	Attempts    int
	CreatedAt   time.Time
}

type Repository struct {
	database *sql.DB
	newID    func() string
	now      func() time.Time
}

func New(database *sql.DB, idGenerators ...func() string) *Repository {
	newID := randomID
	if len(idGenerators) != 0 && idGenerators[0] != nil {
		newID = idGenerators[0]
	}
	return &Repository{database: database, newID: newID, now: time.Now}
}

func (r *Repository) Create(ctx context.Context, input CreateInput) (Job, error) {
	input.LocalOrderID = strings.TrimSpace(input.LocalOrderID)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.LocalOrderID == "" {
		return Job{}, fmt.Errorf("local order ID is required")
	}
	if input.TotalMinor < 0 {
		return Job{}, fmt.Errorf("total minor units cannot be negative")
	}
	if !validCurrency(input.Currency) {
		return Job{}, fmt.Errorf("currency must be a three-letter ISO 4217 code")
	}

	now := r.now().UTC()
	job := Job{
		ID: r.newID(), LocalOrderID: input.LocalOrderID, Status: StatusPending,
		TotalMinor: input.TotalMinor, Currency: input.Currency, CreatedAt: now,
	}
	eventID := r.newID()
	payload, err := json.Marshal(map[string]any{
		"jobId": job.ID, "localOrderId": job.LocalOrderID,
		"totalMinor": job.TotalMinor, "currency": job.Currency,
	})
	if err != nil {
		return Job{}, fmt.Errorf("encode job event: %w", err)
	}

	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, fmt.Errorf("begin job transaction: %w", err)
	}
	defer transaction.Rollback()
	timestamp := now.Format(time.RFC3339Nano)
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO print_jobs (id, local_order_id, status, total_minor, currency, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, job.ID, job.LocalOrderID, job.Status, job.TotalMinor, job.Currency, timestamp, timestamp); err != nil {
		return Job{}, fmt.Errorf("insert print job: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO outbox_events (id, aggregate_id, event_type, payload, status, available_at, created_at)
VALUES (?, ?, 'job.created', ?, 'pending', ?, ?)`, eventID, job.ID, string(payload), timestamp, timestamp); err != nil {
		return Job{}, fmt.Errorf("insert job outbox event: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return Job{}, fmt.Errorf("commit job transaction: %w", err)
	}
	return job, nil
}

func (r *Repository) ClaimOutbox(ctx context.Context, limit int) ([]OutboxEvent, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("outbox claim limit must be from 1 through 100")
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer transaction.Rollback()
	rows, err := transaction.QueryContext(ctx, `
SELECT id, aggregate_id, event_type, payload, attempts, created_at
FROM outbox_events
WHERE status = 'pending' AND available_at <= ?
ORDER BY created_at, id
LIMIT ?`, time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("select outbox events: %w", err)
	}
	var events []OutboxEvent
	for rows.Next() {
		var event OutboxEvent
		var createdAt string
		if err := rows.Scan(&event.ID, &event.AggregateID, &event.EventType, &event.Payload, &event.Attempts, &createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("parse outbox timestamp: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close outbox rows: %w", err)
	}
	for _, event := range events {
		if _, err := transaction.ExecContext(ctx, `UPDATE outbox_events SET status = 'processing', attempts = attempts + 1 WHERE id = ? AND status = 'pending'`, event.ID); err != nil {
			return nil, fmt.Errorf("claim outbox event %s: %w", event.ID, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return events, nil
}

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func randomID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("secure random ID generation failed: %v", err))
	}
	return hex.EncodeToString(bytes)
}
