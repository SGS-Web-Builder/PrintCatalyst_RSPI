package localserver_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments/razorpay"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

func TestRazorpayHeaderCapturesAndDuplicateIsAcknowledged(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, dir+"/payments.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := payments.New(db.DB(), dir, payments.WithProviderAdapter(payments.KindRazorpayMerchant, razorpay.New()))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := svc.CreateProvider(ctx, payments.ProviderInput{Kind: payments.KindRazorpayMerchant, DisplayName: "Test", Enabled: true, Secret: `{"key_id":"rzp_test_demo","key_secret":"api-secret","webhook_secret":"webhook-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.DB().Exec(`INSERT INTO payment_intents(id,order_id,installation_id,provider_id,amount_minor,currency,currency_minor_units,status,gateway_order_id,idempotency_key,created_at,updated_at) VALUES('intent','order','installation',?,500,'INR',2,'redirected','plink_1','key',?,?)`, provider.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(localserver.New("", "installation", localserver.WithPayments(svc)).Handler())
	defer server.Close()
	body := `{"entity":"event","event":"payment_link.paid","account_id":"acc_test","created_at":123,"payload":{"payment_link":{"entity":{"id":"plink_1","amount":500,"amount_paid":500,"currency":"INR","status":"paid","notes":{"intent_id":"intent"}}},"payment":{"entity":{"id":"pay_1","amount":500,"currency":"INR","status":"captured"}}}}`
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write([]byte(body))
	signature := hex.EncodeToString(mac.Sum(nil))
	for i, sig := range []string{"wrong-signature", signature, signature} {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/owner/payments/webhook?provider="+provider.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Razorpay-Signature", sig)
		req.Header.Set("X-Forwarded-Host", "print.example.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		text, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if i == 0 && resp.StatusCode != 422 {
			t.Fatalf("invalid signature = %d: %s", resp.StatusCode, text)
		}
		if i > 0 && resp.StatusCode != 200 && resp.StatusCode != 202 {
			t.Fatalf("delivery %d = %d: %s", i, resp.StatusCode, text)
		}
	}
	var status string
	if err := db.DB().QueryRow("SELECT status FROM payment_intents WHERE id='intent'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "captured" {
		t.Fatalf("intent = %s", status)
	}
}
