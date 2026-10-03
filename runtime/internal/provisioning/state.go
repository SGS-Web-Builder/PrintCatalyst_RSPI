package provisioning

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Gate string

const (
	GateLicence   Gate = "licence"
	GateOwner     Gate = "owner"
	GateBusiness  Gate = "business"
	GatePricing   Gate = "pricing"
	GatePrinter   Gate = "printer"
	GatePayment   Gate = "payment"
	GateTunnel    Gate = "tunnel"
	GateTestOrder Gate = "test_order"
	GateBackup    Gate = "backup"
)

var OrderedGates = []Gate{
	GateLicence, GateOwner, GateBusiness, GatePricing, GatePrinter,
	GatePayment, GateTunnel, GateTestOrder, GateBackup,
}

type GateStatus struct {
	Gate        Gate
	Complete    bool
	CompletedAt *time.Time
}

type StatusResult struct {
	Gates           []GateStatus
	Completed       int
	Next            Gate
	ProductionReady bool
}

type State struct {
	database *sql.DB
	now      func() time.Time
	newID    func() string
}

func New(database *sql.DB) *State {
	return &State{database: database, now: time.Now, newID: randomID}
}

func (s *State) CompleteGate(ctx context.Context, gate Gate, evidence string) error {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return fmt.Errorf("provisioning evidence is required")
	}
	if !knownGate(gate) {
		return fmt.Errorf("unknown provisioning gate %q", gate)
	}

	transaction, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provisioning transaction: %w", err)
	}
	defer transaction.Rollback()
	completed, err := completedGates(ctx, transaction)
	if err != nil {
		return err
	}
	if completed[gate] {
		return nil
	}
	next := nextGate(completed)
	if gate != next {
		return fmt.Errorf("provisioning gate %q cannot be completed before %q", gate, next)
	}

	timestamp := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO provisioning_gates (gate, completed_at, evidence) VALUES (?, ?, ?)
ON CONFLICT(gate) DO UPDATE SET completed_at = excluded.completed_at, evidence = excluded.evidence`, gate, timestamp, evidence); err != nil {
		return fmt.Errorf("complete provisioning gate: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO audit_events (id, event_type, evidence, occurred_at) VALUES (?, 'provisioning.gate.completed', ?, ?)`, s.newID(), string(gate)+": "+evidence, timestamp); err != nil {
		return fmt.Errorf("record provisioning audit: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit provisioning gate: %w", err)
	}
	return nil
}

// CompleteGateInTx records a gate inside a caller-owned transaction so the gate
// and the configuration it certifies commit or roll back together. Ordering is
// enforced here: a gate can only complete when every earlier gate is complete.
func CompleteGateInTx(ctx context.Context, transaction *sql.Tx, gate Gate, evidence string, now time.Time) error {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return fmt.Errorf("provisioning evidence is required")
	}
	if !knownGate(gate) {
		return fmt.Errorf("unknown provisioning gate %q", gate)
	}
	completed, err := completedGates(ctx, transaction)
	if err != nil {
		return err
	}
	if completed[gate] {
		return nil
	}
	if next := nextGate(completed); gate != next {
		return fmt.Errorf("provisioning gate %q cannot be completed before %q", gate, next)
	}
	timestamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO provisioning_gates (gate, completed_at, evidence) VALUES (?, ?, ?)
ON CONFLICT(gate) DO UPDATE SET completed_at = excluded.completed_at, evidence = excluded.evidence`, gate, timestamp, evidence); err != nil {
		return fmt.Errorf("complete provisioning gate: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO audit_events (id, event_type, evidence, occurred_at) VALUES (?, 'provisioning.gate.completed', ?, ?)`, randomID(), string(gate)+": "+evidence, timestamp); err != nil {
		return fmt.Errorf("record provisioning audit: %w", err)
	}
	return nil
}

func (s *State) Status(ctx context.Context) (StatusResult, error) {
	rows, err := s.database.QueryContext(ctx, `SELECT gate, completed_at FROM provisioning_gates WHERE completed_at IS NOT NULL`)
	if err != nil {
		return StatusResult{}, fmt.Errorf("read provisioning status: %w", err)
	}
	defer rows.Close()
	completed := make(map[Gate]time.Time)
	for rows.Next() {
		var gate Gate
		var timestamp string
		if err := rows.Scan(&gate, &timestamp); err != nil {
			return StatusResult{}, fmt.Errorf("scan provisioning status: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return StatusResult{}, fmt.Errorf("parse provisioning timestamp: %w", err)
		}
		completed[gate] = parsed
	}
	result := StatusResult{Gates: make([]GateStatus, 0, len(OrderedGates))}
	completedFlags := make(map[Gate]bool, len(completed))
	for _, gate := range OrderedGates {
		status := GateStatus{Gate: gate}
		if timestamp, ok := completed[gate]; ok {
			status.Complete = true
			status.CompletedAt = &timestamp
			result.Completed++
			completedFlags[gate] = true
		}
		result.Gates = append(result.Gates, status)
	}
	result.Next = nextGate(completedFlags)
	result.ProductionReady = result.Completed == len(OrderedGates)
	return result, nil
}

func (s *State) ProductionReady(ctx context.Context) (bool, error) {
	status, err := s.Status(ctx)
	return status.ProductionReady, err
}

func completedGates(ctx context.Context, transaction *sql.Tx) (map[Gate]bool, error) {
	rows, err := transaction.QueryContext(ctx, `SELECT gate FROM provisioning_gates WHERE completed_at IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read completed gates: %w", err)
	}
	defer rows.Close()
	completed := map[Gate]bool{}
	for rows.Next() {
		var gate Gate
		if err := rows.Scan(&gate); err != nil {
			return nil, fmt.Errorf("scan completed gate: %w", err)
		}
		completed[gate] = true
	}
	return completed, nil
}

func nextGate(completed map[Gate]bool) Gate {
	for _, gate := range OrderedGates {
		if !completed[gate] {
			return gate
		}
	}
	return ""
}

func knownGate(candidate Gate) bool {
	for _, gate := range OrderedGates {
		if candidate == gate {
			return true
		}
	}
	return false
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("secure random ID generation failed: %v", err))
	}
	return hex.EncodeToString(buffer)
}
