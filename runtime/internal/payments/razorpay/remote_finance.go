package razorpay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

func (a *Adapter) financeRequest(ctx context.Context, method, path, key string, body any, secret []byte, out any) error {
	creds, err := ParseCredentials(secret)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if body != nil {
		if err = json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint+path, &buf)
	if err != nil {
		return err
	}
	req.SetBasicAuth(creds.KeyID, creds.KeySecret)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Refund-Idempotency", key)
	}
	client := *a.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("gateway returned HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
func (a *Adapter) Refund(ctx context.Context, payment, key string, amount int64, secret []byte) (payments.RefundResult, error) {
	var out payments.RefundResult
	err := a.financeRequest(ctx, "POST", "/payments/"+url.PathEscape(payment)+"/refund", key, map[string]any{"amount": amount, "speed": "normal"}, secret, &out)
	return out, err
}
func (a *Adapter) RefundStatus(ctx context.Context, id string, secret []byte) (payments.RefundResult, error) {
	var out payments.RefundResult
	err := a.financeRequest(ctx, "GET", "/refunds/"+url.PathEscape(id), "", nil, secret, &out)
	return out, err
}
func (a *Adapter) Settlements(ctx context.Context, skip int, secret []byte) ([]payments.Settlement, error) {
	var out struct {
		Items []payments.Settlement `json:"items"`
	}
	err := a.financeRequest(ctx, "GET", "/settlements?count=25&skip="+strconv.Itoa(skip), "", nil, secret, &out)
	return out.Items, err
}
