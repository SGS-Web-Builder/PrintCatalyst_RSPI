package razorpay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
)

func (a *Adapter) FetchCapture(ctx context.Context, intent payments.Intent, secret []byte) (payments.Webhook, bool, error) {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return payments.Webhook{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/payment_links/"+url.PathEscape(intent.GatewayOrderID), nil)
	if err != nil {
		return payments.Webhook{}, false, err
	}
	req.SetBasicAuth(creds.KeyID, creds.KeySecret)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return payments.Webhook{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return payments.Webhook{}, false, fmt.Errorf("payment status API returned HTTP %d", resp.StatusCode)
	}
	var link struct {
		ID, Status, Currency string
		Amount               int64
		AmountPaid           int64 `json:"amount_paid"`
		Notes                map[string]string
		Payments             []struct {
			PaymentID string `json:"payment_id"`
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&link); err != nil {
		return payments.Webhook{}, false, err
	}
	if link.Status != "paid" {
		return payments.Webhook{}, false, nil
	}
	if link.ID != intent.GatewayOrderID || link.Notes["intent_id"] != intent.ID || link.Amount != intent.AmountMinor || link.AmountPaid != intent.AmountMinor || link.Currency != intent.Currency {
		return payments.Webhook{}, false, fmt.Errorf("paid link identity, amount or currency mismatch")
	}
	paymentID := ""
	if len(link.Payments) > 0 {
		paymentID = link.Payments[0].PaymentID
	}
	if paymentID == "" {
		return payments.Webhook{}, false, fmt.Errorf("paid link has no payment reference")
	}
	return payments.Webhook{IntentID: intent.ID, GatewayOrderID: link.ID, GatewayPaymentID: paymentID, Status: payments.StatusCaptured, AmountMinor: link.Amount, Currency: link.Currency}, true, nil
}
