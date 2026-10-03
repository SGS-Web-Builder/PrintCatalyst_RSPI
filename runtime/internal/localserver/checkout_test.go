package localserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func checkoutJSON(t *testing.T, url string, body any, want int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d want %d: %s", url, resp.StatusCode, want, data)
	}
	result := map[string]any{}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCheckoutAtomicUploadsRetryPreviewAndDiscard(t *testing.T) {
	ts, db, root := portalFixtureWithData(t)
	draft := checkoutJSON(t, ts.URL+"/api/v1/portal/drafts", map[string]any{}, 200)
	id, token := draft["orderId"].(string), draft["uploadToken"].(string)
	upload := func(files []uploadFile, want int) map[string]any {
		body, typ := uploadMultipart(t, files)
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/uploads?orderId="+id, body)
		req.Header.Set("Content-Type", typ)
		req.Header.Set("X-Upload-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("upload %d: %s", resp.StatusCode, raw)
		}
		out := map[string]any{}
		json.Unmarshal(raw, &out)
		return out
	}
	good := uploadFile{Name: "good.pdf", MIME: "application/pdf", Body: minimalPDF()}
	upload([]uploadFile{good, {Name: "bad.pdf", MIME: "application/pdf", Body: []byte("broken")}}, 400)
	var count int
	db.DB().QueryRow("SELECT COUNT(*) FROM documents").Scan(&count)
	if count != 0 {
		t.Fatal("partial metadata survived")
	}
	filepath.WalkDir(filepath.Join(root, "documents"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("partial file survived: %s", path)
		}
		return nil
	})
	upload([]uploadFile{good}, 201)
	again := upload([]uploadFile{good}, 201)
	if len(again["files"].([]any)) != 1 {
		t.Fatal("retry duplicated files")
	}
	doc := again["files"].([]any)[0].(map[string]any)["documentId"].(string)
	for _, test := range []struct {
		token  string
		status int
	}{{"wrong", 404}, {token, 200}} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/portal/documents/"+doc+"?orderId="+id, nil)
		req.Header.Set("X-Upload-Token", test.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != test.status {
			t.Fatalf("preview status %d", resp.StatusCode)
		}
		if test.status == 200 && (!bytes.Equal(body, minimalPDF()) || resp.Header.Get("Cache-Control") != "no-store") {
			t.Fatal("incorrect or cacheable preview")
		}
	}
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/portal/drafts/"+id, nil)
	req.Header.Set("X-Upload-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	db.DB().QueryRow("SELECT COUNT(*) FROM documents").Scan(&count)
	if count != 0 {
		t.Fatal("discard left documents")
	}
}

func TestCheckoutSharedPricesDiscountAndLimits(t *testing.T) {
	ts, db := portalFixture(t)
	ctx := context.Background()
	biz := business.New(db.DB())
	var printer string
	db.DB().QueryRow("SELECT id FROM printers LIMIT 1").Scan(&printer)
	svc, err := biz.CreateService(ctx, business.CreateServiceInput{Code: "copy", DisplayName: "Copy", Enabled: true, PrinterIDs: []string{printer}, Pricing: []pricing.Entry{{PaperSize: "A4", ColourMode: "monochrome", Sides: "one-sided", UnitPriceMinor: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = biz.CreateDiscount(ctx, business.CreateDiscountInput{Code: "SAVE10", DisplayName: "Save", Type: "percent", Value: 10, Enabled: true, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	newUpload := func() map[string]any {
		body, typ := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
		resp, err := http.Post(ts.URL+"/api/v1/portal/uploads", typ, body)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var data map[string]any
		json.NewDecoder(resp.Body).Decode(&data)
		return data
	}
	up := newUpload()
	doc := up["files"].([]any)[0].(map[string]any)["documentId"]
	line := map[string]any{"documentId": doc, "paperSize": "A4", "colourMode": "monochrome", "sides": "one-sided", "copies": 1, "pageRangeStart": 1, "pageRangeEnd": 3}
	payload := map[string]any{"lines": []any{line}, "serviceId": svc.ID, "discountCode": "save10"}
	quote := checkoutJSON(t, ts.URL+"/api/v1/portal/quote", payload, 200)
	if quote["totalMinor"] != float64(675) || quote["discountMinor"] != float64(75) {
		t.Fatalf("wrong shared-rate discount quote: %v", quote)
	}
	payload["orderId"] = up["orderId"]
	payload["customerName"] = "Customer"
	payload["customerPhone"] = "1234567890"
	placed := checkoutJSON(t, ts.URL+"/api/v1/portal/orders", payload, 201)
	if placed["totalMinor"] != float64(675) {
		t.Fatal(placed)
	}
	checkoutJSON(t, ts.URL+"/api/v1/portal/orders", payload, 409)
	var uses int
	db.DB().QueryRow("SELECT times_used FROM discounts WHERE code='SAVE10'").Scan(&uses)
	if uses != 1 {
		t.Fatal("duplicate consumed discount")
	}
	delete(payload, "orderId")
	delete(payload, "customerName")
	delete(payload, "customerPhone")
	checkoutJSON(t, ts.URL+"/api/v1/portal/quote", payload, 400)
	payload["discountCode"] = ""
	line["colourMode"] = "colour"
	colourQuote := checkoutJSON(t, ts.URL+"/api/v1/portal/quote", payload, 200)
	if colourQuote["totalMinor"] != float64(4500) {
		t.Fatal("colour must use the shared grid too", colourQuote)
	}
}

func TestCheckoutCannotCombineCapabilitiesAcrossPrinters(t *testing.T) {
	ts, db := portalFixture(t)
	if _, err := db.DB().Exec(`UPDATE printer_capabilities SET normalized_json='{"colourModes":["monochrome"],"sidesModes":["one-sided"]}'`); err != nil {
		t.Fatal(err)
	}
	// A second printer can print colour, but only on A3. Its colour capability
	// must not be combined with the first printer's verified A4 paper.
	ctx := context.Background()
	fleet := printers.New(db.DB())
	other, err := fleet.Register(ctx, printers.RegisterInput{Backend: printers.BackendMock, QueueName: "A3 colour only", Capabilities: &printers.Snapshot{PaperSizes: []printers.PaperSize{{Key: "A3", RawLabel: "A3"}}, ColourModes: []string{"colour"}, SidesModes: []string{"one-sided"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fleet.Enable(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := fleet.RecordVerification(ctx, printers.Verification{PrinterID: other.ID, CapabilityType: "paper_size", CapabilityKey: "A3", Status: printers.VerificationVerified}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/api/v1/portal/options")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data struct{ Combinations []pricing.Entry }
	json.NewDecoder(resp.Body).Decode(&data)
	if len(data.Combinations) != 1 || data.Combinations[0].ColourMode != "monochrome" {
		t.Fatalf("unsupported colour exposed: %+v", data)
	}
}

func TestCheckoutSheetPreviewOptionsAndAuthorization(t *testing.T) {
	ts, _ := portalFixture(t)
	draft := checkoutJSON(t, ts.URL+"/api/v1/portal/drafts", map[string]any{}, 200)
	id, token := draft["orderId"].(string), draft["uploadToken"].(string)
	img := image.NewNRGBA(image.Rect(0, 0, 12, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 12; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var data bytes.Buffer
	png.Encode(&data, img)
	body, typ := uploadMultipart(t, []uploadFile{{Name: "colour.png", MIME: "image/png", Body: data.Bytes()}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/uploads?orderId="+id, body)
	req.Header.Set("Content-Type", typ)
	req.Header.Set("X-Upload-Token", token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct {
		Files []struct {
			DocumentID string `json:"documentId"`
		}
	}
	json.NewDecoder(response.Body).Decode(&uploaded)
	response.Body.Close()
	if response.StatusCode != 201 || len(uploaded.Files) != 1 {
		t.Fatal("image upload failed")
	}
	endpoint := ts.URL + "/api/v1/portal/documents/" + uploaded.Files[0].DocumentID + "?orderId=" + id + "&preview=sheet&from=1&to=1&pagesPerSheet=1&side=1&paperSize=A4&sides=one-sided"
	for _, tt := range []struct {
		token, options string
		status         int
	}{{token, "&orientation=landscape&colourMode=colour", 200}, {token, "&orientation=portrait&colourMode=monochrome", 200}, {"wrong", "&orientation=portrait&colourMode=colour", 404}, {token, "&orientation=sideways&colourMode=colour", 400}} {
		req, _ := http.NewRequest("GET", endpoint+tt.options, nil)
		req.Header.Set("X-Upload-Token", tt.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tt.status {
			t.Fatalf("status=%d want %d: %s", res.StatusCode, tt.status, raw)
		}
		if tt.status == 200 {
			result, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			b := result.Bounds()
			r, g, blue, _ := result.At(b.Dx()/2, b.Dy()/2).RGBA()
			if tt.options == "&orientation=portrait&colourMode=monochrome" {
				if b.Dx() >= b.Dy() || r != g || r != blue {
					t.Fatal("portrait monochrome was not applied")
				}
			} else if b.Dx() <= b.Dy() || r == g {
				t.Fatal("landscape colour was not applied")
			}
		}
	}
}

func TestCheckoutSelectedPagesPricingPersistenceAndInvoice(t *testing.T) {
	ts, db := portalFixture(t)
	body, typ := uploadMultipart(t, []uploadFile{{Name: "pages.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	resp, err := http.Post(ts.URL+"/api/v1/portal/uploads", typ, body)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded map[string]any
	json.NewDecoder(resp.Body).Decode(&uploaded)
	resp.Body.Close()
	doc := uploaded["files"].([]any)[0].(map[string]any)["documentId"]
	line := map[string]any{"documentId": doc, "paperSize": "A4", "colourMode": "monochrome", "sides": "one-sided", "copies": 2, "pageRangeStart": 1, "pageRangeEnd": 3, "pagesPerSheet": 2, "pages": []int{1, 3}}
	request := map[string]any{"lines": []any{line}}
	quote := checkoutJSON(t, ts.URL+"/api/v1/portal/quote", request, 200)
	if quote["totalMinor"] != float64(500) {
		t.Fatalf("selected pages were not priced with N-up: %v", quote)
	}
	for _, invalid := range [][]int{{}, {0}, {4}, {1, 1}, {3, 1}} {
		line["pages"] = invalid
		checkoutJSON(t, ts.URL+"/api/v1/portal/quote", request, 400)
	}
	line["pages"] = []int{1, 3}
	request["orderId"] = uploaded["orderId"]
	request["customerName"] = "Test"
	request["customerPhone"] = "0000000000"
	result := checkoutJSON(t, ts.URL+"/api/v1/portal/orders", request, 201)
	id := result["orderId"].(string)
	svc := orders.New(db.DB(), pricing.New(db.DB()))
	order, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(order.Lines) != 1 || order.Lines[0].PagesJSON != "[1,3]" {
		t.Fatalf("selection was lost: %+v", order.Lines)
	}
	if _, err := db.DB().Exec("UPDATE orders SET status='paid' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	invoice, err := svc.IssueInvoice(context.Background(), id, "test-owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(invoice.Lines) != 1 || invoice.Lines[0].PagesJSON != "[1,3]" || invoice.TotalMinor != 500 {
		t.Fatalf("wrong invoice snapshot: %+v", invoice)
	}
}
