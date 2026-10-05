package dispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pageselection"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
)

// PreparationRenderer returns a complete PDF with selection/orientation/N-up
// baked in. Output options describe those final sheets, not the source pages.
type PreparationRenderer interface {
	RenderPrepared(context.Context, []byte, DocumentRef) ([]byte, prepared.Settings, error)
}
type preparationLine struct {
	Ref         DocumentRef `json:"ref"`
	Queue       string      `json:"queue"`
	SourceHash  string      `json:"source_hash"`
	InvoiceText string      `json:"invoice_text,omitempty"`
}

func (d *Dispatcher) RunPreparation(ctx context.Context) error {
	if d.config.Prepared == nil || d.config.Renderer == nil {
		return errors.New("prepared store and renderer are required")
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		if err := d.prepareWaiting(ctx); err != nil && ctx.Err() == nil {
			d.setLastError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (d *Dispatcher) prepareWaiting(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, `SELECT p.order_id FROM kiosk_pickups p JOIN orders o ON o.id=p.order_id LEFT JOIN kiosk_preparations k ON k.order_id=p.order_id WHERE p.state='active' AND p.preparation<>'ready' AND o.status='paid' AND o.submitted_at>0 AND (k.order_id IS NULL OR (k.state='pending' AND k.updated_at<=?)) ORDER BY p.created_at LIMIT 10`, time.Now().Unix()-30)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = d.prepareOrder(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("order %s preparation requires attention", id))
			// Do not persist renderer stderr, paths or customer document contents.
			_, _ = d.db.ExecContext(ctx, `UPDATE kiosk_preparations SET state='failed',error='preparation requires attention',updated_at=? WHERE order_id=? AND state<>'ready'`, time.Now().Unix(), id)
			_, _ = d.db.ExecContext(ctx, `UPDATE kiosk_pickups SET preparation='failed' WHERE order_id=? AND state='active' AND preparation<>'ready'`, id)
		}
	}
	return errors.Join(failures...)
}
func (d *Dispatcher) prepareOrder(ctx context.Context, id string) error {
	if d.config.Prepared == nil || d.config.Renderer == nil || d.docs == nil {
		return errors.New("preparation unavailable")
	}
	// Creating this row enables database freeze triggers before any source reads.
	_, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO kiosk_preparations(order_id,plan_json,updated_at) SELECT o.id,'',? FROM orders o JOIN kiosk_pickups p ON p.order_id=o.id WHERE o.id=? AND o.status='paid' AND o.submitted_at>0 AND p.state='active' AND p.preparation<>'ready'`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	var raw, state string
	if err = d.db.QueryRowContext(ctx, `SELECT plan_json,state FROM kiosk_preparations WHERE order_id=?`, id).Scan(&raw, &state); err != nil {
		return err
	}
	if state == "ready" {
		return nil
	}
	if raw == "" {
		plan, e := d.capturePreparation(ctx, id)
		if e != nil {
			return e
		}
		b, e := json.Marshal(plan)
		if e != nil {
			return e
		}
		if _, e = d.db.ExecContext(ctx, `UPDATE kiosk_preparations SET plan_json=? WHERE order_id=? AND plan_json=''`, string(b), id); e != nil {
			return e
		}
		if e = d.db.QueryRowContext(ctx, `SELECT plan_json FROM kiosk_preparations WHERE order_id=?`, id).Scan(&raw); e != nil {
			return e
		}
	}
	var plan []preparationLine
	if err = json.Unmarshal([]byte(raw), &plan); err != nil {
		return err
	}
	if len(plan) == 0 {
		return errors.New("empty preparation plan")
	}
	digest, err := d.config.Prepared.PublishJobs(id, len(plan), func(index int) (prepared.Input, error) {
		line := plan[index]
		var body []byte
		if line.Ref.Invoice {
			body = []byte(line.InvoiceText)
		} else {
			body, err = d.docs.FetchAt(ctx, line.Ref.StoragePath)
			if err != nil {
				return prepared.Input{}, err
			}
			h := sha256.Sum256(body)
			if hex.EncodeToString(h[:]) != line.SourceHash {
				return prepared.Input{}, errors.New("source checksum changed")
			}
			if line.Ref.MIMEType == "application/pdf" {
				body, err = documents.ReflowImages(body, line.Ref.PaperSize, line.Ref.Orientation)
				if err != nil {
					return prepared.Input{}, err
				}
			}
		}
		pdf, settings, e := d.config.Renderer.RenderPrepared(ctx, body, line.Ref)
		if e != nil {
			return prepared.Input{}, e
		}
		if settings.Pages < 1 || settings.Pages > 1000 {
			return prepared.Input{}, errors.New("renderer must report validated PDF page count")
		}
		colour := line.Ref.ColourMode
		if colour == "colour" {
			colour = "color"
		}
		if settings.Paper != line.Ref.PaperSize || settings.Tray != line.Ref.Tray || settings.Copies != line.Ref.Copies || settings.Colour != colour {
			return prepared.Input{}, errors.New("renderer changed frozen print options")
		}
		if line.Ref.Sides == "one-sided" && settings.Sides != "one-sided" || line.Ref.Sides != "one-sided" && settings.Sides == "one-sided" {
			return prepared.Input{}, errors.New("renderer changed duplex selection")
		}
		if (line.Ref.Orientation == "portrait" || line.Ref.Orientation == "landscape") && settings.Sides != orientationDuplex(line.Ref.Sides, line.Ref.Orientation) {
			return prepared.Input{}, errors.New("renderer changed duplex orientation")
		}
		return prepared.Input{LineID: line.Ref.LineID, Queue: line.Queue, Invoice: line.Ref.Invoice, Settings: settings, PDF: pdf}, nil
	})
	if err != nil {
		return err
	}
	if _, err = d.config.Prepared.Verify(id, digest); err != nil {
		return err
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE kiosk_pickups SET preparation='ready',prepared_digest=? WHERE order_id=? AND state='active' AND preparation<>'ready' AND EXISTS(SELECT 1 FROM orders WHERE id=? AND status='paid')`, digest, id, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("pickup no longer eligible for preparation")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kiosk_preparations SET prepared_digest=?,state='ready',error='',updated_at=? WHERE order_id=?`, digest, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *Dispatcher) capturePreparation(ctx context.Context, id string) ([]preparationLine, error) {
	var printer, service string
	if err := d.db.QueryRowContext(ctx, `SELECT primary_printer_id,service_id FROM orders WHERE id=? AND status='paid'`, id).Scan(&printer, &service); err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT l.id,l.document_id,doc.storage_path,doc.page_count,doc.mime_type,doc.sha256,l.colour_mode,l.sides,l.paper_size,l.copies,l.page_range_start,l.page_range_end,l.orientation,l.pages_per_sheet,l.selected_pages_json FROM order_lines l JOIN documents doc ON doc.id=l.document_id WHERE l.order_id=? ORDER BY l.id`, id)
	if err != nil {
		return nil, err
	}
	var plan []preparationLine
	for rows.Next() {
		var line preparationLine
		var selected string
		line.Ref.OrderID = id
		r := &line.Ref
		if err = rows.Scan(&r.LineID, &r.DocumentID, &r.StoragePath, &r.PageCount, &r.MIMEType, &line.SourceHash, &r.ColourMode, &r.Sides, &r.PaperSize, &r.Copies, &r.PageStart, &r.PageEnd, &r.Orientation, &r.PagesPerSheet, &selected); err != nil {
			break
		}
		r.Pages, err = pageselection.Decode(selected)
		if err != nil {
			break
		}
		if err = validatePrintOptions(*r, r.PageCount); err != nil {
			break
		}
		plan = append(plan, line)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(plan) == 0 {
		return nil, errors.New("no print lines")
	}
	queues := map[string]bool{}
	for i := range plan {
		plan[i].Queue, err = d.resolveQueue(ctx, printer, service, plan[i].Ref)
		if err != nil {
			return nil, err
		}
		queues[plan[i].Queue] = true
	}
	routes := map[string]string{}
	for _, line := range plan {
		routes[line.Ref.LineID] = line.Queue
	}
	// Capture candidate invoices now; the claimed queue group selects whether to
	// submit them later. No invoice reaches the printer merely because it exists.
	for _, line := range append([]preparationLine(nil), plan...) {
		q := line.Queue
		if !queues[q] {
			continue
		}
		queues[q] = false
		var paper, tray string
		err = d.db.QueryRowContext(ctx, `SELECT s.paper,s.tray FROM printer_invoice_settings s JOIN printers p ON p.id=s.printer_id WHERE p.queue_name=? AND s.enabled=1 AND p.removed_at IS NULL`, q).Scan(&paper, &tray)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		text, e := d.separatorTextForRoutes(ctx, id, q, routes)
		if e != nil {
			return nil, e
		}
		logo, e := d.separatorLogo(ctx)
		if e != nil {
			return nil, e
		}
		h := sha256.Sum256([]byte(q))
		ref := DocumentRef{OrderID: id, LineID: "invoice-" + hex.EncodeToString(h[:]), Invoice: true, InvoiceLogo: logo, MIMEType: "text/plain", PaperSize: paper, Tray: tray, ColourMode: "monochrome", Sides: "one-sided", Copies: 1, PageStart: 1, PageEnd: 1, PageCount: 1, PagesPerSheet: 1, Orientation: "portrait"}
		plan = append(plan, preparationLine{Ref: ref, Queue: q, InvoiceText: text})
	}
	return plan, nil
}
