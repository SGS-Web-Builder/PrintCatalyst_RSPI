package dispatch

import (
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"strings"
	"testing"
)

type retainedBackend struct {
	progressBackend
	released   int
	releaseErr error
}

func (b *retainedBackend) ReleaseCompleted(context.Context, string, string, string) error {
	b.released++
	return b.releaseErr
}

func TestRetainedJobReleasedOnlyAfterDurableCompletionAndCleanupRetries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, db)
	seedOrder(t, ctx, db, docs, files, "retained")
	b := &retainedBackend{progressBackend: progressBackend{state: "printing"}}
	d := NewWithDefaultQueue(db, docs, b, DispatcherConfig{DefaultQueue: "Test"})
	d.tick(ctx)
	if _, err = db.Exec("UPDATE print_submissions SET job_id='winspool-retained-job-123'"); err != nil {
		t.Fatal(err)
	}
	d.refreshJobs(ctx)
	if b.released != 0 {
		t.Fatal("released before completion")
	}
	b.state = "completed"
	d.refreshJobs(ctx)
	if b.released != 0 {
		t.Fatal("released in detection pass")
	}
	b.releaseErr = errors.New("temporary queue failure")
	d.refreshJobs(ctx)
	var id, progress string
	db.QueryRow("SELECT job_id,progress FROM print_submissions").Scan(&id, &progress)
	if progress != "completed" || !strings.Contains(id, "retained") {
		t.Fatal(id, progress)
	}
	b.releaseErr = nil
	d.refreshJobs(ctx)
	db.QueryRow("SELECT job_id FROM print_submissions").Scan(&id)
	if id != "winspool-job-123" || b.released != 2 {
		t.Fatal(id, b.released)
	}
	d.refreshJobs(ctx)
	d.tick(ctx)
	if b.released != 2 || len(b.Jobs()) != 1 {
		t.Fatal("repeated cleanup or duplicate print")
	}
}
