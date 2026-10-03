package dispatch

import (
	"context"
	"strings"
	"time"
)

func (d *Dispatcher) refreshSeparatorJobs(ctx context.Context) {
	monitor, ok := d.backend.(JobMonitor)
	if !ok {
		return
	}
	rows, err := d.db.QueryContext(ctx, `SELECT order_id,queue_name,job_id,progress FROM separator_invoices WHERE state='submitted' AND (progress NOT IN ('completed','review') OR (progress='completed' AND job_id LIKE 'winspool-retained-job-%'))`)
	if err != nil {
		d.setLastError(err)
		return
	}
	type job struct{ order, queue, id, progress string }
	var jobs []job
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.order, &j.queue, &j.id, &j.progress); err != nil {
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
		if j.progress == "completed" {
			if release, ok := d.backend.(interface {
				ReleaseCompleted(context.Context, string, string, string) error
			}); ok {
				if err = release.ReleaseCompleted(ctx, j.queue, j.id, j.order); err == nil {
					_, err = d.db.ExecContext(ctx, `UPDATE separator_invoices SET job_id=? WHERE order_id=? AND queue_name=? AND job_id=?`, strings.Replace(j.id, "winspool-retained-job-", "winspool-job-", 1), j.order, j.queue, j.id)
				}
			}
		} else {
			state, detail, e := monitor.JobProgress(ctx, j.queue, j.id, j.order)
			if e != nil {
				state = "blocked"
				detail = e.Error()
			}
			switch state {
			case "pending", "processing", "printing", "completed", "blocked", "review":
				_, err = d.db.ExecContext(ctx, `UPDATE separator_invoices SET progress=?,progress_detail=?,updated_at=? WHERE order_id=? AND queue_name=? AND progress NOT IN ('completed','review') AND (progress<>? OR progress_detail<>?)`, state, detail, time.Now().Unix(), j.order, j.queue, state, detail)
			}
		}
		if err != nil {
			d.setLastError(err)
		}
	}
}
