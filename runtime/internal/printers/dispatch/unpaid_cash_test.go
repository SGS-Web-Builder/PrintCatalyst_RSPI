package dispatch

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pickup"
	"strings"
	"testing"
)

func TestUnpaidCashPolicyPickupAndDispatch(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "otp", true: "automatic"}[automatic], func(t *testing.T) {
			ctx := context.Background()
			d, backend, db := separatorFixture(t, 2, true, 0)
			d.config.RequirePickup = true
			must := func(q string, args ...any) {
				t.Helper()
				if _, e := db.Exec(q, args...); e != nil {
					t.Fatal(e)
				}
			}
			must("UPDATE orders SET status='pending_payment',payment_method='cash_on_counter' WHERE id='order-00'")
			must("UPDATE business_settings SET auto_print_mode='off'")
			key := []byte(strings.Repeat("a", 32))
			lookup := []byte(strings.Repeat("b", 32))
			p, e := pickup.New(db, key, lookup)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = p.IssueVerified(ctx, "order-00", "unpaid-cash:order-00"); e == nil {
				t.Fatal("unpaid permitted with policy off")
			}
			must("UPDATE business_settings SET auto_print_mode='all_documents'")
			must("UPDATE orders SET payment_method='razorpay' WHERE id='order-00'")
			if _, e = p.IssueVerified(ctx, "order-00", "unpaid-cash:order-00"); e == nil {
				t.Fatal("unpaid online permitted")
			}
			must("UPDATE orders SET payment_method='cash_on_counter' WHERE id='order-00'")
			if e = p.Recover(ctx); e != nil {
				t.Fatal(e)
			}
			v, e := p.ViewForOrder(ctx, "order-00")
			if e != nil || len(v.Code) != 4 || !v.PaymentPending {
				t.Fatal(v, e)
			}
			code := v.Code
			d.tick(ctx)
			if len(backend.refs) != 0 {
				t.Fatal("printed without release")
			}
			if e = p.MarkPrepared(ctx, "order-00", strings.Repeat("c", 64)); e != nil {
				t.Fatal(e)
			}
			must("UPDATE business_settings SET auto_print_mode='off'")
			if _, e = p.Claim(ctx, code); e == nil {
				t.Fatal("revoked policy released")
			}
			must("UPDATE business_settings SET auto_print_mode='all_documents'")
			if automatic {
				must("UPDATE kiosk_control SET otp_enabled=0")
				n, e := p.ReleaseAutomatic(ctx)
				if e != nil || n != 1 {
					t.Fatal(n, e)
				}
			} else {
				if _, e = p.Claim(ctx, code); e != nil {
					t.Fatal(e)
				}
			}
			// A committed release survives subsequent policy changes and restart.
			must("UPDATE business_settings SET auto_print_mode='off'")
			d.tick(ctx)
			if e = d.LastError(); e != nil {
				t.Fatal(e)
			}
			if len(backend.refs) != 2 {
				t.Fatalf("document and invoice: %d", len(backend.refs))
			}
			var status string
			if e = db.QueryRow("SELECT status FROM orders WHERE id='order-00'").Scan(&status); e != nil || status != "pending_payment" {
				t.Fatal("payment changed", status, e)
			}
			again := NewWithDefaultQueue(db, d.docs, backend, d.config)
			again.tick(ctx)
			if len(backend.refs) != 2 {
				t.Fatal("duplicate after restart")
			}
		})
	}
}

func TestUnpaidCashPreparedPipeline(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, renderer, _ := prepareFixture(t, 1, true, 0)
	if _, e := d.db.Exec("UPDATE orders SET status='pending_payment',payment_method='cash_on_counter' WHERE id='order-00'"); e != nil {
		t.Fatal(e)
	}
	if _, e := d.db.Exec("UPDATE business_settings SET auto_print_mode='all_documents'"); e != nil {
		t.Fatal(e)
	}
	if e := d.prepareOrder(ctx, "order-00"); e != nil {
		t.Fatal(e)
	}
	if renderer.calls == 0 {
		t.Fatal("renderer not invoked")
	}
	if _, e := p.Claim(ctx, codes[0]); e != nil {
		t.Fatal(e)
	}
	d.tick(ctx)
	if e := d.LastError(); e != nil {
		t.Fatal(e)
	}
	if len(b.refs) != 2 {
		t.Fatal("missing prepared document/invoice", len(b.refs))
	}
	var status string
	d.db.QueryRow("SELECT status FROM orders WHERE id='order-00'").Scan(&status)
	if status != "pending_payment" {
		t.Fatal("unpaid changed", status)
	}
}

func TestFourPreparedDocumentsSubmittedTogetherWithoutCompletionWait(t *testing.T) {
	ctx := context.Background()
	d, b, p, codes, _, _ := prepareFixture(t, 1, false, 0)
	// Four independent print lines represent a composed PDF and three singles.
	for _, id := range []string{"single-1", "single-2", "single-3"} {
		_, err := d.db.Exec(`INSERT INTO order_lines(id,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,unit_price_minor,line_total_minor) SELECT ?,order_id,document_id,paper_size,paper_key,colour_mode,sides,copies,page_range_start,page_range_end,unit_price_minor,line_total_minor FROM order_lines WHERE id='order-00-line'`, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.db.Exec(`UPDATE orders SET status='pending_payment',payment_method='cash_on_counter'; UPDATE business_settings SET auto_print_mode='all_documents'`); err != nil {
		t.Fatal(err)
	}
	if err := d.prepareOrder(ctx, "order-00"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(ctx, codes[0]); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx)
	if d.LastError() != nil || len(b.refs) != 4 {
		t.Fatal(len(b.refs), d.LastError())
	}
	// No progress refresh/physical completion occurred between these submissions.
	if !d.seen["order-00"] {
		t.Fatal("fully submitted unpaid bundle remains in hot path")
	}
	d.tick(ctx)
	again := New(d.db, d.docs, b, d.resolver, d.config)
	again.tick(ctx)
	if len(b.refs) != 4 {
		t.Fatal("replayed submitted jobs")
	}
}
