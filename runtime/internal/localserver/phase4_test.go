package localserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// ---- Phase 4: printers HTTP ----

func TestOwnerPrintersListRequiresAuth(t *testing.T) {
	ts, _ := portalFixture(t)
	resp, err := http.Get(ts.URL + "/api/v1/owner/printers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("unauthenticated printers list = %d, want 401/403", resp.StatusCode)
	}
}

func TestOwnerPrintersRegisterAndList(t *testing.T) {
	ts, db := portalFixture(t)
	if _, err := db.DB().Exec("DELETE FROM printers"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := signedInAsOwner(t, ts.URL)

	body, _ := json.Marshal(map[string]any{
		"backend":   "ipp",
		"queueName": "LabPrinter",
		"uri":       "ipp://localhost/printers/lab",
		"attributes": map[string][]string{
			"media-supported":            {"iso_a4_210x297mm", "na_letter_8.5x11in"},
			"media-source-supported":     {"auto", "tray-1"},
			"print-color-mode-supported": {"monochrome", "color"},
			"sides-supported":            {"one-sided", "two-sided-long-edge"},
		},
	})
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/printers", bytes.NewReader(body))
	regReq.AddCookie(cookie)
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("X-CSRF-Token", csrf)
	regReq.Header.Set("Origin", ts.URL)
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	defer regResp.Body.Close()
	if regResp.StatusCode != 201 {
		bs, _ := io.ReadAll(regResp.Body)
		t.Fatalf("register status = %d, body = %s", regResp.StatusCode, string(bs))
	}
	var reg struct {
		ID           string `json:"id"`
		Capabilities struct {
			PaperSizes []struct {
				Key string `json:"key"`
			} `json:"paperSizes"`
		} `json:"capabilities"`
	}
	if err := json.NewDecoder(regResp.Body).Decode(&reg); err != nil {
		t.Fatal(err)
	}
	if reg.ID == "" {
		t.Fatal("id missing")
	}
	if len(reg.Capabilities.PaperSizes) == 0 {
		t.Fatal("expected normalized paper sizes")
	}

	// List printers.
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/printers", nil)
	listReq.AddCookie(cookie)
	listReq.Header.Set("X-CSRF-Token", csrf)
	listReq.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != 200 {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var listed struct {
		Printers []struct {
			ID        string `json:"id"`
			QueueName string `json:"queueName"`
		} `json:"printers"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Printers) != 1 {
		t.Fatalf("printers = %d, want 1", len(listed.Printers))
	}
	if listed.Printers[0].ID != reg.ID {
		t.Fatalf("printer id mismatch: %s vs %s", listed.Printers[0].ID, reg.ID)
	}
}

func TestOwnerPrintersEnableDisable(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	// Register.
	body, _ := json.Marshal(map[string]any{
		"backend":   "mock",
		"queueName": "EnableDisable",
		"attributes": map[string][]string{
			"media-supported": {"A4"},
		},
	})
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/printers", bytes.NewReader(body))
	regReq.AddCookie(cookie)
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("X-CSRF-Token", csrf)
	regReq.Header.Set("Origin", ts.URL)
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	defer regResp.Body.Close()
	var reg struct {
		ID string `json:"id"`
	}
	json.NewDecoder(regResp.Body).Decode(&reg)

	// Enable.
	enReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/printers/"+reg.ID+"/enable", nil)
	enReq.AddCookie(cookie)
	enReq.Header.Set("X-CSRF-Token", csrf)
	enReq.Header.Set("Origin", ts.URL)
	enReq.Header.Set("Content-Type", "application/json")
	enResp, err := http.DefaultClient.Do(enReq)
	if err != nil {
		t.Fatal(err)
	}
	defer enResp.Body.Close()
	if enResp.StatusCode != 200 {
		t.Fatalf("enable status = %d", enResp.StatusCode)
	}

	// Disable.
	disReq, _ := http.NewRequest("PUT", ts.URL+"/api/v1/owner/printers/"+reg.ID+"/disable", nil)
	disReq.AddCookie(cookie)
	disReq.Header.Set("X-CSRF-Token", csrf)
	disReq.Header.Set("Origin", ts.URL)
	disReq.Header.Set("Content-Type", "application/json")
	disResp, err := http.DefaultClient.Do(disReq)
	if err != nil {
		t.Fatal(err)
	}
	defer disResp.Body.Close()
	if disResp.StatusCode != 200 {
		t.Fatalf("disable status = %d", disResp.StatusCode)
	}
}

func TestOwnerPrintersRegisterRejectsBadBackend(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	body, _ := json.Marshal(map[string]any{
		"backend":   "alien",
		"queueName": "X",
	})
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/printers", bytes.NewReader(body))
	regReq.AddCookie(cookie)
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("X-CSRF-Token", csrf)
	regReq.Header.Set("Origin", ts.URL)
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	defer regResp.Body.Close()
	if regResp.StatusCode != 400 {
		bs, _ := io.ReadAll(regResp.Body)
		t.Fatalf("bad backend status = %d, want 400, body = %s", regResp.StatusCode, string(bs))
	}
}

func TestOwnerPrintersRecordAndListVerifications(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	// Register a printer first.
	regBody, _ := json.Marshal(map[string]any{
		"backend":   "mock",
		"queueName": "VerifyMe",
		"attributes": map[string][]string{
			"media-supported": {"A4"},
		},
	})
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/printers", bytes.NewReader(regBody))
	regReq.AddCookie(cookie)
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("X-CSRF-Token", csrf)
	regReq.Header.Set("Origin", ts.URL)
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	defer regResp.Body.Close()
	var reg struct {
		ID string `json:"id"`
	}
	json.NewDecoder(regResp.Body).Decode(&reg)

	// Record a verification.
	recBody, _ := json.Marshal(map[string]any{
		"capabilityType": "paper_size",
		"capabilityKey":  "A4",
		"status":         "verified",
		"evidence":       "test page printed",
	})
	recReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/printers/"+reg.ID+"/verifications", bytes.NewReader(recBody))
	recReq.AddCookie(cookie)
	recReq.Header.Set("Content-Type", "application/json")
	recReq.Header.Set("X-CSRF-Token", csrf)
	recReq.Header.Set("Origin", ts.URL)
	recResp, err := http.DefaultClient.Do(recReq)
	if err != nil {
		t.Fatal(err)
	}
	defer recResp.Body.Close()
	if recResp.StatusCode != 201 {
		bs, _ := io.ReadAll(recResp.Body)
		t.Fatalf("record verification status = %d, body = %s", recResp.StatusCode, string(bs))
	}

	// List verifications.
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/printers/"+reg.ID+"/verifications", nil)
	listReq.AddCookie(cookie)
	listReq.Header.Set("X-CSRF-Token", csrf)
	listReq.Header.Set("Origin", ts.URL)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != 200 {
		t.Fatalf("list verifications status = %d", listResp.StatusCode)
	}
	var listed struct {
		Verifications []struct {
			CapabilityKey string `json:"capabilityKey"`
			Status        string `json:"status"`
		} `json:"verifications"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Verifications) != 1 {
		t.Fatalf("verifications = %d, want 1", len(listed.Verifications))
	}
	if listed.Verifications[0].Status != "verified" {
		t.Fatalf("status = %s, want verified", listed.Verifications[0].Status)
	}
}

func TestOwnerPrintersRemove(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)

	regBody, _ := json.Marshal(map[string]any{
		"backend":   "mock",
		"queueName": "RemoveMe",
		"attributes": map[string][]string{
			"media-supported": {"A4"},
		},
	})
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/owner/printers", bytes.NewReader(regBody))
	regReq.AddCookie(cookie)
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("X-CSRF-Token", csrf)
	regReq.Header.Set("Origin", ts.URL)
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	defer regResp.Body.Close()
	var reg struct {
		ID string `json:"id"`
	}
	json.NewDecoder(regResp.Body).Decode(&reg)

	delReq, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/owner/printers/"+reg.ID, nil)
	delReq.AddCookie(cookie)
	delReq.Header.Set("X-CSRF-Token", csrf)
	delReq.Header.Set("Origin", ts.URL)
	delReq.Header.Set("Content-Type", "application/json")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != 204 {
		t.Fatalf("remove status = %d, want 204", delResp.StatusCode)
	}

	// Subsequent GET returns 404.
	getReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/printers/"+reg.ID, nil)
	getReq.AddCookie(cookie)
	getReq.Header.Set("X-CSRF-Token", csrf)
	getReq.Header.Set("Origin", ts.URL)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != 404 {
		t.Fatalf("post-remove GET status = %d, want 404", getResp.StatusCode)
	}
}

func TestPrinterPaperSetupAPI(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	request := func(method, path, body string, auth bool) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		if auth {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	reg := request("POST", "/api/v1/owner/printers", `{"backend":"windows","queueName":"Paper setup API","attributes":{"media-supported":["A4"]}}`, true)
	var printer struct {
		ID string `json:"id"`
	}
	json.NewDecoder(reg.Body).Decode(&printer)
	reg.Body.Close()
	if reg.StatusCode != 201 {
		t.Fatal(reg.Status)
	}
	path := "/api/v1/owner/printers/" + printer.ID
	for _, item := range []struct {
		method, path, body string
		auth               bool
		want               int
	}{
		{"PUT", path + "/paper", `{"paper":"A4","enabled":true}`, false, 401},
		{"POST", path + "/test-print", `{"paper":"A4"}`, false, 401},
		{"PUT", path + "/paper", `{"paper":"A4","enabled":true}`, true, 200},
		{"PUT", path + "/paper", `{"paper":"A4","enabled":false}`, true, 200},
		{"PUT", path + "/paper", `{"paper":"Imaginary","enabled":true}`, true, 400},
	} {
		resp := request(item.method, item.path, item.body, item.auth)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != item.want {
			t.Fatalf("%s %s: %d %s", item.method, item.path, resp.StatusCode, body)
		}
	}
}
