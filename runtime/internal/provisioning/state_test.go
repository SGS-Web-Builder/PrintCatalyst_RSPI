package provisioning

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func TestCompleteGatePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "print-catalyst.sqlite")
	first, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	state := New(first.DB())
	if err := state.CompleteGate(context.Background(), GateLicence, "licence signature verified"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	status, err := New(second.DB()).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Gates[0].Complete || status.Next != GateOwner {
		t.Fatalf("status = %#v", status)
	}
}

func TestGatesCannotBeSkippedOrCompletedWithoutEvidence(t *testing.T) {
	state, closeStore := openState(t)
	defer closeStore()
	if err := state.CompleteGate(context.Background(), GateOwner, "owner created"); err == nil {
		t.Fatal("owner gate completed before licence")
	}
	if err := state.CompleteGate(context.Background(), GateLicence, "  "); err == nil {
		t.Fatal("licence gate completed without evidence")
	}
	status, err := state.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Completed != 0 || status.ProductionReady {
		t.Fatalf("status = %#v", status)
	}
}

func TestProductionReadyOnlyAfterAllMandatoryGates(t *testing.T) {
	state, closeStore := openState(t)
	defer closeStore()
	for index, gate := range OrderedGates {
		if ready, err := state.ProductionReady(context.Background()); err != nil || ready {
			t.Fatalf("ready before gate %d: ready=%v err=%v", index, ready, err)
		}
		if err := state.CompleteGate(context.Background(), gate, "verified "+string(gate)); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := state.ProductionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("production is not ready after every gate")
	}
	var auditCount int
	if err := state.database.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'provisioning.gate.completed'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != len(OrderedGates) {
		t.Fatalf("audit count = %d, want %d", auditCount, len(OrderedGates))
	}
}

func openState(t *testing.T) (*State, func()) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "print-catalyst.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	return New(database.DB()), func() { _ = database.Close() }
}
