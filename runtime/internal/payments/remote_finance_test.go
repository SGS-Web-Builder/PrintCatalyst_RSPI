package payments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type financeGateway struct {
	pickupGateway
	fail    bool
	keys    []string
	fetches int
}

func (g *financeGateway) Refund(_ context.Context, payment, key string, amount int64, _ []byte) (RefundResult, error) {
	g.keys = append(g.keys, key)
	if g.fail {
		g.fail = false
		return RefundResult{}, errors.New("lost response")
	}
	return RefundResult{ID: "refund-test", PaymentID: payment, Amount: amount, Currency: "INR", Status: "pending"}, nil
}
func (g *financeGateway) RefundStatus(context.Context, string, []byte) (RefundResult, error) {
	g.fetches++
	return RefundResult{ID: "refund-test", PaymentID: "pay-test", Amount: 500, Currency: "INR", Status: "processed"}, nil
}
func (g *financeGateway) Settlements(context.Context, int, []byte) ([]Settlement, error) {
	return nil, nil
}
func TestRemoteRefundLostResponseBlocksPickupAndReusesRequest(t *testing.T) {
	svc, db, provider, w, p := pickupPaymentFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(w)
	if _, e := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.DB().Exec("UPDATE orders SET status='paid'"); e != nil {
		t.Fatal(e)
	}
	if e := p.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	view, e := p.ViewForOrder(ctx, "order")
	if e != nil || view.Code == "" {
		t.Fatal(view, e)
	}
	gateway := &financeGateway{fail: true}
	svc.adapters[KindRazorpayMerchant] = gateway
	if _, e = svc.RefundUnreleased(ctx, "order", "Customer cancelled"); e == nil {
		t.Fatal("lost response must require review")
	}
	if _, e = p.Claim(ctx, view.Code); e == nil {
		t.Fatal("refund must block physical release")
	}
	after, e := p.ViewForOrder(ctx, "order")
	if e != nil || after.Code != "" || after.State != "refund_review" {
		t.Fatal(after, e)
	}
	if _, e = svc.RefundUnreleased(ctx, "order", "Retry"); e != nil {
		t.Fatal(e)
	}
	if len(gateway.keys) != 2 || gateway.keys[0] != gateway.keys[1] {
		t.Fatal("duplicate refund identity", gateway.keys)
	}
	result, e := svc.RefundUnreleased(ctx, "order", "Check status")
	if e != nil || result.Status != "processed" || gateway.fetches != 1 || len(gateway.keys) != 2 {
		t.Fatal(result, e)
	}
	finalView, e := p.ViewForOrder(ctx, "order")
	if e != nil || finalView.State != "refunded" || finalView.Code != "" {
		t.Fatal(finalView, e)
	}
	var status string
	if e = db.DB().QueryRow("SELECT status FROM orders WHERE id='order'").Scan(&status); e != nil || status != "paid" {
		t.Fatal("payment history rewritten", status, e)
	}
}
func TestRemoteRefundRejectsAlreadyReleased(t *testing.T) {
	svc, db, provider, w, p := pickupPaymentFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(w)
	if _, e := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); e != nil {
		t.Fatal(e)
	}
	db.DB().Exec("UPDATE orders SET status='paid'")
	if e := p.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := db.DB().Exec("INSERT INTO kiosk_releases(order_id,prepared_digest,claimed_at) VALUES('order','digest',1)"); e != nil {
		t.Fatal(e)
	}
	gateway := &financeGateway{}
	svc.adapters[KindRazorpayMerchant] = gateway
	if _, e := svc.RefundUnreleased(ctx, "order", "Partial print"); e == nil {
		t.Fatal("released refund accepted")
	}
	if len(gateway.keys) != 0 {
		t.Fatal("gateway called")
	}
}

func TestMaintenanceDoesNotBlockPaidPhysicalPickup(t *testing.T) {
	svc, db, provider, w, p := pickupPaymentFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(w)
	if _, e := svc.RecordWebhook(ctx, provider, raw, "verified-test-signature"); e != nil {
		t.Fatal(e)
	}
	db.DB().Exec("UPDATE orders SET status='paid'")
	if e := p.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	view, e := p.ViewForOrder(ctx, "order")
	if e != nil {
		t.Fatal(e)
	}
	if e = p.MarkPrepared(ctx, "order", strings.Repeat("c", 64)); e != nil {
		t.Fatal(e)
	}
	db.DB().Exec("UPDATE kiosk_control SET paused=1")
	if id, e := p.Claim(ctx, view.Code); e != nil || id != "order" {
		t.Fatal("maintenance blocked paid pickup", id, e)
	}
}
