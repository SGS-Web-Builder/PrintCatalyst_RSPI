package dispatch

import (
	"context"
	"strings"
	"time"
)

// JobMonitor reports OS state without resubmitting or changing payment status.
type JobMonitor interface {
	JobProgress(context.Context, string, string, string) (string, string, error)
}

func (d *Dispatcher) monitorJobs(ctx context.Context) {
	if _, ok := d.backend.(JobMonitor); !ok {
		return
	}
	timer := time.NewTicker(500 * time.Millisecond)
	defer timer.Stop()
	for {
		d.refreshJobs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) refreshJobs(ctx context.Context) {
	d.refreshSeparatorJobs(ctx)
	monitor, ok := d.backend.(JobMonitor)
	if !ok {
		return
	}
	rows, err := d.db.QueryContext(ctx, `SELECT j.line_id,j.queue_name,j.job_id,l.order_id,j.progress FROM print_submissions j JOIN order_lines l ON l.id=j.line_id JOIN orders o ON o.id=l.order_id WHERE j.state='submitted' AND ((j.progress NOT IN ('completed','review') AND o.status NOT IN ('cancelled','completed')) OR (j.progress='completed' AND j.job_id LIKE 'winspool-retained-job-%'))`)
	if err != nil {
		d.setLastError(err)
		return
	}
	type job struct{ line, queue, id, order, progress string }
	var jobs []job
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.line, &j.queue, &j.id, &j.order, &j.progress); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		d.setLastError(err)
		return
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		if j.progress == "completed" {
			d.releaseCompleted(ctx, j.line, j.queue, j.id, j.order)
			continue
		}
		state, detail, e := monitor.JobProgress(ctx, j.queue, j.id, j.order)
		if e != nil {
			state = "blocked"
			detail = "Cannot read Windows printer status: " + e.Error()
		}
		switch state {
		case "pending", "processing", "printing", "completed", "blocked", "review":
		default:
			continue
		}
		_, e = d.db.ExecContext(ctx, `UPDATE print_submissions SET progress=?,progress_detail=?,updated_at=? WHERE line_id=? AND state='submitted' AND progress NOT IN ('completed','review') AND (progress<>? OR progress_detail<>?)`, state, detail, time.Now().Unix(), j.line, state, detail)
		if e != nil {
			d.setLastError(e)
		}
	}
}

func (d *Dispatcher) releaseCompleted(ctx context.Context, line, queue, id, order string) {
	releaser, ok := d.backend.(interface {
		ReleaseCompleted(context.Context, string, string, string) error
	})
	if !ok {
		return
	}
	if err := releaser.ReleaseCompleted(ctx, queue, id, order); err != nil {
		d.setLastError(err)
		return
	}
	_, err := d.db.ExecContext(ctx, "UPDATE print_submissions SET job_id=? WHERE line_id=? AND job_id=? AND progress='completed'", strings.Replace(id, "winspool-retained-job-", "winspool-job-", 1), line, id)
	if err != nil {
		d.setLastError(err)
	}
}
