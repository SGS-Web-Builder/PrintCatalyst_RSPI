package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
	"time"
)

func preparedRef(id string, j prepared.Job) DocumentRef {
	colour := j.Settings.Colour
	if colour == "color" {
		colour = "colour"
	}
	return DocumentRef{Prepared: true, OrderID: id, LineID: j.LineID, Invoice: j.Invoice, MIMEType: "application/pdf", PaperSize: j.Settings.Paper, Tray: j.Settings.Tray, ColourMode: colour, Sides: j.Settings.Sides, Copies: j.Settings.Copies, PagesPerSheet: 1, Orientation: "auto"}
}
func (d *Dispatcher) dispatchPrepared(ctx context.Context, id, printer, service string) error {
	var digest string
	if err := d.db.QueryRowContext(ctx, `SELECT r.prepared_digest FROM kiosk_releases r JOIN kiosk_preparations p ON p.order_id=r.order_id WHERE r.order_id=? AND p.state='ready' AND p.prepared_digest=r.prepared_digest`, id).Scan(&digest); err != nil {
		return err
	}
	manifest, err := d.config.Prepared.Verify(id, digest)
	if err != nil {
		return err
	}
	type work struct {
		index int
		state string
	}
	var jobs []work
	candidates := map[string]int{}
	lines := map[string]bool{}
	for i, j := range manifest.Jobs {
		if j.Invoice {
			if _, ok := candidates[j.Queue]; ok {
				return errors.New("duplicate prepared invoice")
			}
			candidates[j.Queue] = i
			continue
		}
		var belongs bool
		if err = d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM order_lines WHERE id=? AND order_id=?)`, j.LineID, id).Scan(&belongs); err != nil {
			return err
		}
		if !belongs {
			return errors.New("prepared line mismatch")
		}
		lines[j.LineID] = true
		q, e := d.resolveQueue(ctx, printer, service, preparedRef(id, j))
		if e != nil {
			return e
		}
		if q != j.Queue {
			return errors.New("prepared printer route changed; operator review required")
		}
		var state string
		err = d.db.QueryRowContext(ctx, `SELECT state FROM print_submissions WHERE line_id=?`, j.LineID).Scan(&state)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if state != "" && state != "submitted" {
			return errors.New("previous submission requires review")
		}
		var held bool
		if err = d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM separator_invoices i JOIN orders o ON o.id=i.order_id WHERE i.queue_name=? AND i.order_id<>? AND i.state IN ('failed','submitting') AND o.status<>'cancelled')`, j.Queue, id).Scan(&held); err != nil {
			return err
		}
		if held {
			return errors.New("printer invoice requires review")
		}
		jobs = append(jobs, work{i, state})
	}
	var count int
	if err = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM order_lines WHERE order_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if len(lines) != count || count == 0 {
		return errors.New("incomplete prepared order")
	}
	rows, err := d.db.QueryContext(ctx, `SELECT queue_name,paper,tray,state FROM separator_invoices WHERE order_id=? AND state<>'skipped' ORDER BY queue_name`, id)
	if err != nil {
		return err
	}
	// Preflight every required invoice before submitting even the first document.
	for rows.Next() {
		var q, paper, tray, state string
		if err = rows.Scan(&q, &paper, &tray, &state); err != nil {
			break
		}
		i, ok := candidates[q]
		if !ok {
			err = errors.New("required invoice was not prepared")
			break
		}
		j := manifest.Jobs[i]
		if j.Settings.Paper != paper || j.Settings.Tray != tray || j.Settings.Pages < 1 {
			err = errors.New("invoice settings changed after preparation")
			break
		}
		if state != "waiting" && state != "submitted" {
			err = errors.New("invoice submission requires review")
			break
		}
		jobs = append(jobs, work{i, state})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.state == "submitted" {
			continue
		}
		j := manifest.Jobs[job.index]
		if err = licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
			return err
		}
		data, readErr := d.config.Prepared.ReadJob(digest, j)
		if readErr != nil {
			return readErr
		}
		var result sql.Result
		if j.Invoice {
			result, err = d.db.ExecContext(ctx, `UPDATE separator_invoices SET state='submitting',updated_at=? WHERE order_id=? AND queue_name=? AND state='waiting'`, time.Now().Unix(), id, j.Queue)
		} else {
			result, err = d.db.ExecContext(ctx, `INSERT OR IGNORE INTO print_submissions(line_id,state,queue_name,updated_at) VALUES(?,'submitting',?,?)`, j.LineID, j.Queue, time.Now().Unix())
		}
		if err != nil {
			return err
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.New("submission already claimed; review required")
		}
		jobID, submitErr := d.backend.Submit(ctx, j.Queue, data, preparedRef(id, j))
		state, detail := "submitted", ""
		if submitErr != nil || jobID == "" {
			state = "failed"
			detail = "submission outcome requires operator review"
		}
		if j.Invoice {
			_, err = d.db.ExecContext(ctx, `UPDATE separator_invoices SET state=?,job_id=?,sheets=?,error=?,updated_at=? WHERE order_id=? AND queue_name=?`, state, jobID, j.Settings.Pages, detail, time.Now().Unix(), id, j.Queue)
		} else {
			_, err = d.db.ExecContext(ctx, `UPDATE print_submissions SET state=?,job_id=?,error=?,updated_at=? WHERE line_id=?`, state, jobID, detail, time.Now().Unix(), j.LineID)
		}
		if err != nil {
			return err
		}
		if state == "failed" {
			return fmt.Errorf("%s", detail)
		}
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE orders SET status='dispatched',print_requested=0,updated_at=? WHERE id=? AND status='paid'`, time.Now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET dispatched_at=? WHERE order_id=? AND dispatched_at=0`, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}
