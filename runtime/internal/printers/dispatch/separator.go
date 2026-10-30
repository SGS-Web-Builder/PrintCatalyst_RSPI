package dispatch

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
	"strings"
	"time"
)

type dispatchBatch struct{ id, printer, service, status string }
type separatorSetting struct {
	paper, tray string
	threshold   int
}

// Only fresh waiting orders count. Submitted, failed and interrupted jobs never
// inflate a new group. Persist both the yes and no decisions across restarts.
func (d *Dispatcher) planSeparators(ctx context.Context, batches []dispatchBatch) error {
	if len(batches) == 0 {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, `SELECT p.queue_name,s.paper,s.tray,s.threshold FROM printer_invoice_settings s JOIN printers p ON p.id=s.printer_id WHERE s.enabled=1 AND p.removed_at IS NULL`)
	if err != nil {
		return err
	}
	settings := map[string]separatorSetting{}
	for rows.Next() {
		var q string
		var s separatorSetting
		if err = rows.Scan(&q, &s.paper, &s.tray, &s.threshold); err != nil {
			break
		}
		settings[q] = s
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(settings) == 0 {
		return nil
	}
	type route struct{ line, order, queue string }
	var routes []route
	groups := map[string]map[string]bool{}
	for _, b := range batches {
		d.mu.Lock()
		skip := d.seen[b.id]
		d.mu.Unlock()
		if skip {
			continue
		}
		var started bool
		if err = d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM order_lines l WHERE l.order_id=? AND (EXISTS(SELECT 1 FROM print_submissions j WHERE j.line_id=l.id) OR EXISTS(SELECT 1 FROM print_routes r WHERE r.line_id=l.id)))`, b.id).Scan(&started); err != nil {
			return err
		}
		if started {
			continue
		}
		rs, e := d.db.QueryContext(ctx, `SELECT id,paper_size,colour_mode,sides,copies,orientation,pages_per_sheet FROM order_lines WHERE order_id=? ORDER BY id`, b.id)
		if e != nil {
			return e
		}
		var refs []DocumentRef
		for rs.Next() {
			var ref DocumentRef
			ref.OrderID = b.id
			if e = rs.Scan(&ref.LineID, &ref.PaperSize, &ref.ColourMode, &ref.Sides, &ref.Copies, &ref.Orientation, &ref.PagesPerSheet); e != nil {
				break
			}
			refs = append(refs, ref)
		}
		if e == nil {
			e = rs.Err()
		}
		rs.Close()
		if e != nil {
			return e
		}
		var proposed []route
		for _, ref := range refs {
			q, resolveErr := d.resolveQueue(ctx, b.printer, b.service, ref)
			if resolveErr != nil {
				proposed = nil
				break
			}
			proposed = append(proposed, route{ref.LineID, b.id, q})
		}
		for _, r := range proposed {
			routes = append(routes, r)
			if _, ok := settings[r.queue]; ok {
				if groups[r.queue] == nil {
					groups[r.queue] = map[string]bool{}
				}
				groups[r.queue][b.id] = true
			}
		}
	}
	if len(routes) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range routes {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO print_routes(line_id,queue_name) VALUES(?,?)`, r.line, r.queue); err != nil {
			return err
		}
	}
	for q, orders := range groups {
		s := settings[q]
		state := "skipped"
		if len(orders) > s.threshold {
			state = "waiting"
		}
		for id := range orders {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO separator_invoices(order_id,queue_name,paper,tray,queue_count,state,updated_at) VALUES(?,?,?,?,?,?,?)`, id, q, s.paper, s.tray, len(orders), state, time.Now().Unix()); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (d *Dispatcher) resolveQueue(ctx context.Context, printer, service string, ref DocumentRef) (string, error) {
	if resolver, ok := d.resolver.(interface {
		CompatibleQueue(context.Context, string, string, DocumentRef) (string, error)
	}); ok {
		return resolver.CompatibleQueue(ctx, printer, service, ref)
	}
	return d.orderQueue(ctx, printer, service)
}

func (d *Dispatcher) submitSeparators(ctx context.Context, id string) error {
	if err := d.checkPickup(ctx, id); err != nil {
		return err
	}
	if err := licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
		return err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT queue_name,paper,tray,state,error FROM separator_invoices WHERE order_id=? AND state<>'skipped' ORDER BY queue_name`, id)
	if err != nil {
		return err
	}
	type job struct{ queue, paper, tray, state, detail string }
	var jobs []job
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.queue, &j.paper, &j.tray, &j.state, &j.detail); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.state == "submitted" {
			continue
		}
		if j.state != "waiting" {
			return fmt.Errorf("order %s invoice: %s; check output before retry: %s", id, j.state, j.detail)
		}
		text, err := d.separatorText(ctx, id, j.queue)
		if err != nil {
			return err
		}
		logo, err := d.separatorLogo(ctx)
		if err != nil {
			return err
		}
		if err := licensegate.CheckRequired(ctx, d.config.LicenseCheck); err != nil {
			return err
		}
		result, err := d.db.ExecContext(ctx, `UPDATE separator_invoices SET state='submitting',updated_at=? WHERE order_id=? AND queue_name=? AND state='waiting'`, time.Now().Unix(), id, j.queue)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("invoice already claimed")
		}
		sheets := 1
		ref := DocumentRef{OrderID: id, Invoice: true, InvoiceLogo: logo, InvoiceSheets: &sheets, MIMEType: "text/plain", PaperSize: j.paper, Tray: j.tray, ColourMode: "monochrome", Sides: "one-sided", Copies: 1, Orientation: "portrait", PagesPerSheet: 1, PageStart: 1, PageEnd: 1, PageCount: 1}
		jobID, submitErr := d.backend.Submit(ctx, j.queue, []byte(text), ref)
		if submitErr != nil {
			_, saveErr := d.db.ExecContext(ctx, `UPDATE separator_invoices SET state='failed',error=?,updated_at=? WHERE order_id=? AND queue_name=?`, submitErr.Error(), time.Now().Unix(), id, j.queue)
			return errors.Join(fmt.Errorf("invoice for %s: %w", id, submitErr), saveErr)
		}
		if _, err = d.db.ExecContext(ctx, `UPDATE separator_invoices SET state='submitted',job_id=?,sheets=?,error='',updated_at=? WHERE order_id=? AND queue_name=?`, jobID, sheets, time.Now().Unix(), id, j.queue); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) separatorText(ctx context.Context, id, queue string) (string, error) {
	return d.separatorTextForRoutes(ctx, id, queue, nil)
}

func (d *Dispatcher) separatorTextForRoutes(ctx context.Context, id, queue string, routes map[string]string) (string, error) {
	var name, phone, email, notes, currency, status, merchant string
	var total, discount int64
	var units int
	var created int64
	err := d.db.QueryRowContext(ctx, `SELECT customer_name,customer_phone,customer_email,customer_notes,currency,currency_minor_units,total_minor,discount_minor,status,created_at FROM orders WHERE id=?`, id).Scan(&name, &phone, &email, &notes, &currency, &units, &total, &discount, &status, &created)
	if err != nil {
		return "", err
	}
	err = d.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(profile_json,'$.name'),'') FROM business_profile WHERE singleton=1`).Scan(&merchant)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if merchant == "" {
		merchant = "Print Catalyst"
	}
	// The separator can be submitted before Windows confirms document output.
	// Do not describe submission alone as completed printing.
	var done bool
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM order_lines WHERE order_id=?) AND NOT EXISTS(SELECT 1 FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=? AND COALESCE(j.progress,'')<>'completed')`, id, id).Scan(&done); err != nil {
		return "", err
	}
	heading := "ORDER INVOICE / SEPARATOR"
	if done || status == "completed" {
		heading = "PRINT DONE"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n%s\nOrder: %s\nPlaced: %s\nCustomer: %s\nPhone: %s\n", merchant, heading, id, time.Unix(created, 0).Local().Format("02 Jan 2006 15:04"), name, phone)
	if email != "" {
		fmt.Fprintf(&out, "Email: %s\n", email)
	}
	if status == "pending_payment" {
		status = "Pending"
	} else if status == "paid" || status == "dispatched" || status == "completed" {
		status = "Recorded"
	}
	fmt.Fprintf(&out, "Printer: %s\nPayment: %s\n\nORDER DOCUMENTS & SETTINGS\n", queue, status)
	rows, err := d.db.QueryContext(ctx, `SELECT l.id,COALESCE(d.original_filename,'Document'),l.paper_size,l.colour_mode,l.sides,l.copies,l.page_range_start,l.page_range_end,l.orientation,l.pages_per_sheet,l.selected_pages_json,l.line_total_minor,COALESCE(j.queue_name,r.queue_name,'Unassigned') FROM order_lines l LEFT JOIN documents d ON d.id=l.document_id LEFT JOIN print_submissions j ON j.line_id=l.id LEFT JOIN print_routes r ON r.line_id=l.id WHERE l.order_id=? ORDER BY l.id`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var lineID, file, paper, colour, sides, orientation, selected, documentQueue string
		var copies, start, end, nup int
		var value int64
		if err = rows.Scan(&lineID, &file, &paper, &colour, &sides, &copies, &start, &end, &orientation, &nup, &selected, &value, &documentQueue); err != nil {
			return "", err
		}
		pages := fmt.Sprintf("%d-%d", start, end)
		if selected != "" && selected != "null" {
			pages = selected
		}
		if frozen, ok := routes[lineID]; ok {
			documentQueue = frozen
		}
		if documentQueue == queue {
			documentQueue = "This printer"
		}
		if colour == "monochrome" {
			colour = "Black & white"
		} else if colour == "colour" {
			colour = "Colour"
		}
		if sides != "one-sided" && (orientation == "portrait" || orientation == "landscape") {
			sides = orientationDuplex(sides, orientation)
		}
		switch sides {
		case "one-sided":
			sides = "Single-sided"
		case "two-sided-long-edge":
			sides = "Duplex, long edge"
		case "two-sided-short-edge":
			sides = "Duplex, short edge"
		}
		if sides != "Single-sided" && (orientation == "auto" || orientation == "") {
			sides = "Back-to-back, automatic flip edge"
		}
		fmt.Fprintf(&out, "Document: %s\nPrinter: %s\nPaper: %s\nColour: %s\nSides: %s\nCopies: %d\nPages: %s\nOrientation: %s\nPages per side: %d\nScaling: Fit to page\nLine value: %s\n\n", file, documentQueue, paper, colour, sides, copies, pages, orientation, nup, invoiceMoney(currency, units, value))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if discount > 0 {
		fmt.Fprintf(&out, "Order discount: %s\n", invoiceMoney(currency, units, discount))
	}
	fmt.Fprintf(&out, "TOTAL ORDER VALUE: %s\n", invoiceMoney(currency, units, total))
	if notes != "" {
		fmt.Fprintf(&out, "Customer notes: %s\n", notes)
	}
	out.WriteString("Total covers the entire order, including other printers.\nEnd of this order on this printer.\n")
	return strings.ReplaceAll(out.String(), "\x00", ""), nil
}
func invoiceMoney(currency string, units int, value int64) string {
	scale := int64(1)
	for i := 0; i < units; i++ {
		scale *= 10
	}
	if units == 0 {
		return fmt.Sprintf("%s %d", currency, value)
	}
	return fmt.Sprintf("%s %d.%0*d", currency, value/scale, units, value%scale)
}

func (d *Dispatcher) separatorLogo(ctx context.Context) ([]byte, error) {
	var raw string
	if err := d.db.QueryRowContext(ctx, "SELECT settings FROM portal_branding WHERE singleton=1").Scan(&raw); err != nil {
		return nil, err
	}
	var branding struct {
		Logo string `json:"logo"`
	}
	if err := json.Unmarshal([]byte(raw), &branding); err != nil {
		return nil, err
	}
	if branding.Logo == "" {
		return nil, nil
	}
	parts := strings.SplitN(branding.Logo, ",", 2)
	if len(parts) != 2 || (parts[0] != "data:image/png;base64" && parts[0] != "data:image/jpeg;base64") {
		return nil, errors.New("invoice logo must be PNG or JPEG")
	}
	body, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	if len(body) > 512*1024 {
		return nil, errors.New("invoice logo exceeds 512 KB")
	}
	return body, nil
}
