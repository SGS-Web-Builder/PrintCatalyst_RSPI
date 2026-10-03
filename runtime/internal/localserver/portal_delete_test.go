package localserver_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"
)

func TestPortalDeleteDocumentOwnershipAndRetry(t *testing.T) {
	ts, db := portalFixture(t)
	draft := checkoutJSON(t, ts.URL+"/api/v1/portal/drafts", map[string]any{}, 200)
	id, token := draft["orderId"].(string), draft["uploadToken"].(string)
	var pngBody bytes.Buffer
	png.Encode(&pngBody, image.NewRGBA(image.Rect(0, 0, 30, 40)))
	body, kind := uploadMultipart(t, []uploadFile{{Name: "one.png", MIME: "image/png", Body: pngBody.Bytes()}, {Name: "two.png", MIME: "image/png", Body: pngBody.Bytes()}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/uploads?orderId="+id, body)
	req.Header.Set("Content-Type", kind)
	req.Header.Set("X-Upload-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct {
		Files []struct {
			ID string `json:"documentId"`
		} `json:"files"`
	}
	json.NewDecoder(resp.Body).Decode(&uploaded)
	resp.Body.Close()
	if len(uploaded.Files) != 2 {
		t.Fatal("upload failed")
	}

	call := func(doc, auth string, want int) {
		t.Helper()
		req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/portal/documents/"+doc+"?orderId="+id, nil)
		req.Header.Set("X-Upload-Token", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("got %d want %d: %s", resp.StatusCode, want, body)
		}
	}
	call(uploaded.Files[0].ID, "wrong", 404)
	call(uploaded.Files[0].ID, token, 204)
	call(uploaded.Files[0].ID, token, 204)
	var count int
	db.DB().QueryRow("SELECT COUNT(*) FROM documents WHERE order_id=?", id).Scan(&count)
	if count != 1 {
		t.Fatalf("remaining documents %d", count)
	}
	if _, err := db.DB().Exec("UPDATE orders SET submitted_at=1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	call(uploaded.Files[1].ID, token, 404)
	if _, err := db.DB().Exec("UPDATE orders SET submitted_at=0 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	call(uploaded.Files[1].ID, token, 204)
	db.DB().QueryRow("SELECT COUNT(*) FROM documents WHERE order_id=?", id).Scan(&count)
	if count != 0 {
		t.Fatalf("last document was not removed: %d", count)
	}
}
