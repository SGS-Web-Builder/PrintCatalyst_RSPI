package dispatch

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"testing"
)

func TestServiceRoutingAndUpdates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		_, err := db.Exec(`INSERT INTO printers(id,backend,queue_name,enabled,is_default,created_at,last_seen_at) VALUES(?,'windows',?,1,1,0,0)`, id, "Queue "+id)
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := business.New(db)
	record, err := svc.CreateService(ctx, business.CreateServiceInput{Code: "photo", DisplayName: "Photo", Enabled: true, PrinterIDs: []string{"two"}})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	record, err = svc.UpdateService(ctx, record.ID, business.UpdateServiceInput{DisplayName: "Photo prints", Enabled: &enabled, PrinterIDs: []string{"two"}})
	if err != nil {
		t.Fatal(err)
	}
	d := New(db, nil, nil, NewDBQueueResolver(db), DispatcherConfig{DefaultQueue: "fallback"})
	queue, err := d.orderQueue(ctx, "", record.ID)
	if err != nil || queue != "Queue two" {
		t.Fatalf("service routed to %q: %v", queue, err)
	}
	if _, err = d.orderQueue(ctx, "one", record.ID); err == nil {
		t.Fatal("unassigned printer accepted")
	}
	db.Exec("UPDATE printers SET enabled=0 WHERE id='two'")
	if _, err = d.orderQueue(ctx, "", record.ID); err == nil {
		t.Fatal("silently used another printer")
	}
}
