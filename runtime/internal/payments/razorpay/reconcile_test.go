package razorpay

import (
	"context"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCaptureVerifiesRemotePayment(t *testing.T) {
	amount := 500
	status := "paid"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "key" || pass != "secret" || r.URL.Path != "/payment_links/plink_1" {
			t.Errorf("wrong request %s", r.URL)
			http.Error(w, "unauthorized", 401)
			return
		}
		fmt.Fprintf(w, `{"id":"plink_1","status":%q,"amount":%d,"amount_paid":%d,"currency":"INR","notes":{"intent_id":"intent_1"},"payments":[{"payment_id":"pay_1"}]}`, status, amount, amount)
	}))
	defer server.Close()
	adapter := New().WithEndpoint(server.URL).WithHTTPClient(server.Client())
	secret := []byte(`{"key_id":"key","key_secret":"secret","webhook_secret":"webhook"}`)
	intent := payments.Intent{ID: "intent_1", GatewayOrderID: "plink_1", AmountMinor: 500, Currency: "INR"}
	event, paid, err := adapter.FetchCapture(context.Background(), intent, secret)
	if err != nil || !paid || event.Status != payments.StatusCaptured || event.GatewayPaymentID != "pay_1" {
		t.Fatalf("capture: %+v %v %v", event, paid, err)
	}
	amount = 1
	if _, paid, err = adapter.FetchCapture(context.Background(), intent, secret); err == nil || paid {
		t.Fatal("accepted wrong amount")
	}
	amount = 500
	status = "created"
	if _, paid, err = adapter.FetchCapture(context.Background(), intent, secret); err != nil || paid {
		t.Fatal("unpaid link marked paid")
	}
}
