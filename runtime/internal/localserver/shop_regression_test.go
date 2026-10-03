package localserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

func TestDirectIPQRWithoutTunnelAndStoredBranding(t *testing.T) {
	ts, _ := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	body := []byte(`{"provider":"direct","publicOrigin":"http://192.168.1.10:8080"}`)
	resp := putJSON(t, ts.URL, "/api/v1/owner/tunnel", cookie, csrf, body)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("save direct: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	resp = putJSON(t, ts.URL, "/api/v1/owner/qr-theme", cookie, csrf, []byte(`{"dark":"#102030","light":"#ffffff","frame":"Shop QR"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("theme status %d", resp.StatusCode)
	}
	resp.Body.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/owner/tunnel/qr.svg", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	svg, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(svg), "#102030") || !strings.Contains(string(svg), "Shop QR") {
		t.Fatalf("direct QR: %d %s", resp.StatusCode, svg)
	}
	req, _ = http.NewRequest("GET", ts.URL+"/api/v1/owner/tunnel", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var state map[string]any
	json.NewDecoder(resp.Body).Decode(&state)
	if state["qrTargetURL"] != "http://192.168.1.10:8080/portal/" {
		t.Fatalf("origin was rewritten: %v", state)
	}
}

func TestLegacyWindowsDocumentPathsReadAndDelete(t *testing.T) {
	root := t.TempDir()
	files, err := localfiles.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = files.MkdirAll("documents/order"); err != nil {
		t.Fatal(err)
	}
	if err = files.WriteAtomic("documents/order/a.pdf", strings.NewReader("pdf bytes")); err != nil {
		t.Fatal(err)
	}
	docs := documents.New(files, nil)
	body, err := docs.FetchAt(context.Background(), `documents\order\a.pdf`)
	if err != nil || string(body) != "pdf bytes" {
		t.Fatalf("read legacy path: %s %v", body, err)
	}
	if _, err = docs.FetchAt(context.Background(), `documents\..\secret`); err == nil {
		t.Fatal("accepted traversal")
	}
	if err = docs.Delete(context.Background(), `documents\order\a.pdf`); err != nil {
		t.Fatal(err)
	}
}
