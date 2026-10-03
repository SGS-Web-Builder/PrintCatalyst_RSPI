package localserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/idcards"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localserver"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/notifications"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/passport"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/provisioning"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/reports"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/tunnel"
)

// portalFixture builds a server with portal routes wired and a fully provisioned
// licence/owner/business/pricing state so a customer can place an order.
// The server binds to 127.0.0.1 on a free port so the loopback Host check in
// localOwnerRequest succeeds.
func portalFixture(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	ts, db, _ := portalFixtureWithData(t)
	return ts, db
}

// portalFixtureWithData is the same as portalFixture but also returns the
// on-disk data root, which the ID Card Studio tests need so they can write
// real PNG files the compose step is able to read.
func portalFixtureWithData(t *testing.T, extra ...localserver.Option) (*httptest.Server, *store.Store, string) {
	t.Helper()
	path := t.TempDir() + "/portal.sqlite"
	dataRoot := t.TempDir() + "/data"
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	for _, gate := range provisioning.OrderedGates {
		if err := provisioning.New(database.DB()).CompleteGate(ctx, gate, "test"); err != nil {
			t.Fatalf("complete %s: %v", gate, err)
		}
	}
	accounts := owner.New(database.DB())
	if err := accounts.Create(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveProfile(ctx, owner.Profile{
		Name: "Campus Prints", Address: "Pune", Phone: "+919999999999",
		Country: "IN", Currency: "INR", Locale: "en-IN", TimeZone: "Asia/Kolkata",
	}); err != nil {
		t.Fatal(err)
	}
	pricingSvc := pricing.New(database.DB())
	if _, err := pricingSvc.Save(ctx, pricing.Input{
		Entries: []pricing.Entry{
			{PaperSize: "A4", ColourMode: pricing.ColourMonochrome, Sides: pricing.SidesOneSided, UnitPriceMinor: 250},
			{PaperSize: "A4", ColourMode: pricing.ColourColour, Sides: pricing.SidesOneSided, UnitPriceMinor: 1500},
		},
	}); err != nil {
		t.Fatal(err)
	}
	seedCheckoutPrinter(t, database)
	files := mustNewFiles(t, dataRoot)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	licensingSvc, err := licensing.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := licensingSvc.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	paymentsSvc, err := payments.New(database.DB(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	options := []localserver.Option{
		localserver.WithStoreHealth(database),
		localserver.WithProvisioning(provisioning.New(database.DB())),
		localserver.WithOwner(accounts, ""),
		localserver.WithPricing(pricingSvc),
		localserver.WithOrders(orders.New(database.DB(), pricingSvc)),
		localserver.WithReports(reports.New(database.DB())),
		localserver.WithNotifications(notifications.New(database.DB(), nil)),
		localserver.WithPrinters(printers.New(database.DB())),
		localserver.WithIDCards(idcards.New(database.DB(), files)),
		localserver.WithPassports(passport.New(database.DB(), files, passport.GeometricDetector{})),
		localserver.WithTunnel(tunnel.New(database.DB())),
		localserver.WithLicensing(licensingSvc),
		localserver.WithPayments(paymentsSvc),
		localserver.WithDB(database.DB()),
		localserver.WithFiles(files),
	}
	srv := localserver.New(listener.Addr().String(), "installation-test", append(options, extra...)...)
	ts := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: srv.Handler()},
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, database, dataRoot
}

// minimalPDF produces a 3-page PDF body for upload tests. The structure is
// canonical PDF 1.4: a /Catalog object (1) references a /Pages container (2)
// which lists three leaf /Page objects (3, 4, 5) in its /Kids array. A
// cross-reference table maps each object to its byte offset; the trailer
// points to /Root 1 0 R and the startxref marker holds the xref offset.
func minimalPDF() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	// Object 1: /Catalog referencing the /Pages container.
	obj1 := buf.Len()
	buf.WriteString("1 0 obj\n<</Type/Catalog/Pages 2 0 R>>\nendobj\n")
	// Object 2: /Pages container with three /Kids.
	obj2 := buf.Len()
	buf.WriteString("2 0 obj\n<</Type/Pages/Count 3/Kids[3 0 R 4 0 R 5 0 R]>>\nendobj\n")
	// Object 3: leaf page.
	obj3 := buf.Len()
	buf.WriteString("3 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")
	// Object 4: leaf page.
	obj4 := buf.Len()
	buf.WriteString("4 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")
	// Object 5: leaf page.
	obj5 := buf.Len()
	buf.WriteString("5 0 obj\n<</Type/Page/Parent 2 0 R>>\nendobj\n")

	// Cross-reference table.
	xrefOffset := buf.Len()
	buf.WriteString("xref\n")
	buf.WriteString("0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj1))
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj2))
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj3))
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj4))
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", obj5))

	// Trailer and startxref.
	buf.WriteString("trailer\n<</Size 6/Root 1 0 R>>\n")
	buf.WriteString(fmt.Sprintf("startxref\n%d\n", xrefOffset))
	buf.WriteString("%%EOF\n")
	return buf.Bytes()
}

func TestPortalUploadsCreateDocumentAndReturnIDs(t *testing.T) {
	ts, _ := portalFixture(t)
	body, contentType := uploadMultipart(t, []uploadFile{
		{Name: "report.pdf", MIME: "application/pdf", Body: minimalPDF()},
	})
	resp, err := http.Post(ts.URL+"/api/v1/portal/uploads", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		read := readAllBody(t, resp)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, read)
	}
	var parsed struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID string `json:"documentId"`
			PageCount  int    `json:"pageCount"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OrderID == "" {
		t.Fatal("missing orderId")
	}
	if len(parsed.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(parsed.Files))
	}
	if parsed.Files[0].DocumentID == "" {
		t.Fatal("missing documentId")
	}
	if parsed.Files[0].PageCount != 3 {
		t.Fatalf("page count = %d, want 3", parsed.Files[0].PageCount)
	}
}

// TestPortalWriteEndpointsRejectWhenSetupIncomplete guards the rule that
// the customer-facing portal must refuse every write until every
// provisioning gate is satisfied. The half-configured fixture mirrors a
// fresh installation that has only completed the licence gate — the
// common state during a merchant's first hour on the machine — and
// asserts the upload, quote and order endpoints all return
// 503 Service Unavailable with a structured {"status":"not_ready", ...}
// payload instead of silently accepting customer data.
func TestPortalWriteEndpointsRejectWhenSetupIncomplete(t *testing.T) {
	path := t.TempDir() + "/portal-unready.sqlite"
	dataRoot := t.TempDir() + "/data"
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := provisioning.New(database.DB()).CompleteGate(ctx, provisioning.GateLicence, "test"); err != nil {
		t.Fatal(err)
	}
	files := mustNewFiles(t, dataRoot)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	options := []localserver.Option{
		localserver.WithStoreHealth(database),
		localserver.WithProvisioning(provisioning.New(database.DB())),
		localserver.WithDB(database.DB()),
		localserver.WithFiles(files),
	}
	srv := localserver.New(listener.Addr().String(), "installation-test", options...)
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.Handler()}}
	ts.Start()
	defer ts.Close()

	// /api/v1/portal/uploads — multipart.
	multipartBody, contentType := uploadMultipart(t, []uploadFile{
		{Name: "report.pdf", MIME: "application/pdf", Body: minimalPDF()},
	})
	resp, err := http.Post(ts.URL+"/api/v1/portal/uploads", contentType, multipartBody)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		body := readAllBody(t, resp)
		t.Fatalf("uploads status = %d, want 503 (body = %s)", resp.StatusCode, body)
	}
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "30" {
		t.Fatalf("uploads Retry-After = %q, want 30", retryAfter)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "not_ready" || payload["reason"] == "" {
		t.Fatalf("uploads payload = %v, want not_ready with a reason", payload)
	}
	resp.Body.Close()

	// /api/v1/portal/quote — JSON body. An empty lines slice would normally
	// fail validation (the server requires at least one line), but the
	// production-ready check must run BEFORE the body validation so a
	// half-configured installation never sees customer payloads. The
	// 503 response is the only signal the dashboard needs.
	quoteBody := bytes.NewBufferString(`{"lines":[]}`)
	quoteResp, err := http.Post(ts.URL+"/api/v1/portal/quote", "application/json", quoteBody)
	if err != nil {
		t.Fatal(err)
	}
	if quoteResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("quote status = %d, want 503", quoteResp.StatusCode)
	}
	quoteResp.Body.Close()

	// /api/v1/portal/orders — JSON body.
	orderBody := bytes.NewBufferString(`{"orderId":"00000000000000000000000000000000","lines":[],"customerName":"x","customerPhone":"+910000000000","customerEmail":"","customerNotes":""}`)
	orderResp, err := http.Post(ts.URL+"/api/v1/portal/orders", "application/json", orderBody)
	if err != nil {
		t.Fatal(err)
	}
	if orderResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("orders status = %d, want 503", orderResp.StatusCode)
	}
	orderResp.Body.Close()
}

func TestPortalUploadRejectsUnsupportedType(t *testing.T) {
	ts, _ := portalFixture(t)
	body, contentType := uploadMultipart(t, []uploadFile{
		{Name: "doc.txt", MIME: "text/plain", Body: []byte("hello")},
	})
	resp, err := http.Post(ts.URL+"/api/v1/portal/uploads", contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPortalQuoteRejectsUnknownPricing(t *testing.T) {
	ts, _ := portalFixture(t)
	uploadBody, uploadCT := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", uploadCT, uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	var upload struct {
		Files []struct {
			DocumentID string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"lines": []map[string]any{{
			"documentId":     upload.Files[0].DocumentID,
			"paperSize":      "A99",
			"colourMode":     "monochrome",
			"sides":          "one-sided",
			"copies":         1,
			"pageRangeStart": 1,
			"pageRangeEnd":   3,
		}},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(ts.URL+"/api/v1/portal/quote", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		read := readAllBody(t, resp)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, read)
	}
}

func TestPortalQuoteAndOrderEndToEnd(t *testing.T) {
	ts, database := portalFixture(t)
	uploadBody, uploadCT := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", uploadCT, uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	var upload struct {
		OrderID string `json:"orderId"`
		Files   []struct {
			DocumentID string `json:"documentId"`
		} `json:"files"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	lines := []map[string]any{{
		"documentId":     upload.Files[0].DocumentID,
		"paperSize":      "A4",
		"colourMode":     "monochrome",
		"sides":          "one-sided",
		"copies":         2,
		"pageRangeStart": 1,
		"pageRangeEnd":   3,
	}}
	quoteBody, _ := json.Marshal(map[string]any{"lines": lines})
	qResp, err := http.Post(ts.URL+"/api/v1/portal/quote", "application/json", bytes.NewReader(quoteBody))
	if err != nil {
		t.Fatal(err)
	}
	defer qResp.Body.Close()
	if qResp.StatusCode != 200 {
		t.Fatalf("quote status = %d, body = %s", qResp.StatusCode, readAllBody(t, qResp))
	}
	var quote struct {
		TotalMinor int64 `json:"totalMinor"`
		Lines      []struct {
			Sheets         int64 `json:"sheets"`
			UnitPriceMinor int64 `json:"unitPriceMinor"`
			LineTotalMinor int64 `json:"lineTotalMinor"`
		} `json:"lines"`
	}
	if err := json.NewDecoder(qResp.Body).Decode(&quote); err != nil {
		t.Fatal(err)
	}
	if quote.Lines[0].Sheets != 6 {
		t.Fatalf("sheets = %d, want 6 (3 pages × 2 copies)", quote.Lines[0].Sheets)
	}
	if quote.Lines[0].LineTotalMinor != 6*250 {
		t.Fatalf("line total = %d, want %d", quote.Lines[0].LineTotalMinor, 6*250)
	}
	if quote.TotalMinor != 6*250 {
		t.Fatalf("total = %d, want %d", quote.TotalMinor, 6*250)
	}

	// Now place the order.
	orderBody, _ := json.Marshal(map[string]any{
		"orderId":       upload.OrderID,
		"lines":         lines,
		"customerName":  "Ravi Sharma",
		"customerPhone": "+919876543210",
		"customerEmail": "ravi@example.com",
	})
	oResp, err := http.Post(ts.URL+"/api/v1/portal/orders", "application/json", bytes.NewReader(orderBody))
	if err != nil {
		t.Fatal(err)
	}
	defer oResp.Body.Close()
	if oResp.StatusCode != 201 {
		t.Fatalf("place order status = %d, body = %s", oResp.StatusCode, readAllBody(t, oResp))
	}
	var placed struct {
		OrderID    string `json:"orderId"`
		ShareToken string `json:"shareToken"`
		TotalMinor int64  `json:"totalMinor"`
		Status     string `json:"status"`
	}
	if err := json.NewDecoder(oResp.Body).Decode(&placed); err != nil {
		t.Fatal(err)
	}
	if placed.OrderID != upload.OrderID {
		t.Fatalf("orderId = %s, want %s", placed.OrderID, upload.OrderID)
	}
	if placed.TotalMinor != 6*250 {
		t.Fatalf("total = %d, want %d", placed.TotalMinor, 6*250)
	}
	if placed.Status != orders.StatusPendingPayment {
		t.Fatalf("status = %s", placed.Status)
	}

	// And persist check.
	row := database.DB().QueryRow("SELECT COUNT(*) FROM orders WHERE id=? AND status=?", placed.OrderID, orders.StatusPendingPayment)
	var n int
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("order persistence: %d %v", n, err)
	}
	row = database.DB().QueryRow("SELECT COUNT(*) FROM order_lines WHERE order_id=?", placed.OrderID)
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("order lines persistence: %d %v", n, err)
	}
}

func TestPortalOrderRefreshEndpoint(t *testing.T) {
	ts, _ := portalFixture(t)
	uploadBody, uploadCT := uploadMultipart(t, []uploadFile{{Name: "a.pdf", MIME: "application/pdf", Body: minimalPDF()}})
	upResp, err := http.Post(ts.URL+"/api/v1/portal/uploads", uploadCT, uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	var upload struct {
		OrderID string `json:"orderId"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	upResp.Body.Close()
	resp, err := http.Get(ts.URL + "/api/v1/portal/orders/" + upload.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		OrderID string `json:"orderId"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.OrderID != upload.OrderID {
		t.Fatalf("orderId = %s, want %s", got.OrderID, upload.OrderID)
	}
}

func TestPortalRejectsForwardedHeaders(t *testing.T) {
	ts, _ := portalFixture(t)
	body, _ := json.Marshal(map[string]any{"lines": []any{}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/quote", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// ---- helpers ----

type uploadFile struct {
	Name string
	MIME string
	Body []byte
}

func uploadMultipart(t *testing.T, files []uploadFile) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range files {
		hdr := make(map[string][]string)
		hdr["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="files"; filename=%q`, f.Name)}
		hdr["Content-Type"] = []string{f.MIME}
		part, err := w.CreatePart(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(f.Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readAllBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	return readAll(t, resp.Body)
}

func readCSRF(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var parsed map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	tok, ok := parsed["csrfToken"]
	if !ok || tok == "" {
		t.Fatal("missing csrfToken in login response")
	}
	return tok
}

func seedCheckoutPrinter(t *testing.T, db *store.Store) {
	t.Helper()
	svc := printers.New(db.DB())
	ctx := context.Background()
	p, err := svc.Register(ctx, printers.RegisterInput{Backend: printers.BackendMock, QueueName: "Checkout fixture", IsDefault: true, Capabilities: &printers.Snapshot{PaperSizes: []printers.PaperSize{{Key: "A4", RawLabel: "A4"}, {Key: "A3", RawLabel: "A3"}}, ColourModes: []string{"monochrome", "colour"}, SidesModes: []string{"one-sided", "two-sided-long-edge", "two-sided-short-edge"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Enable(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, paper := range []string{"A4", "A3"} {
		if err = svc.RecordVerification(ctx, printers.Verification{PrinterID: p.ID, CapabilityType: "paper_size", CapabilityKey: paper, Status: printers.VerificationVerified}); err != nil {
			t.Fatal(err)
		}
	}
}
