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

func TestPortalComposeOwnershipRetryAndReplacement(t *testing.T) {
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
	input := map[string]any{"orderId": id, "documentIds": []string{uploaded.Files[0].ID, uploaded.Files[1].ID}, "perPage": 2, "paperSize": "A4", "orientation": "portrait", "split": 50}
	call := func(auth string, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/portal/compose-images", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Upload-Token", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("compose %d want %d: %s", resp.StatusCode, want, data)
		}
		out := map[string]any{}
		json.Unmarshal(data, &out)
		return out
	}
	call("wrong", 404)
	input["perPage"] = 3
	call(token, 400)
	var count int
	db.DB().QueryRow("SELECT COUNT(*) FROM documents WHERE order_id=?", id).Scan(&count)
	if count != 2 {
		t.Fatal("failed compose changed sources")
	}
	input["perPage"] = 2
	output := call(token, 200)
	retry := call(token, 200)
	if output["documentId"] != retry["documentId"] || output["pageCount"] != float64(1) {
		t.Fatal("retry changed output or incorrect page count")
	}
	db.DB().QueryRow("SELECT COUNT(*) FROM documents WHERE order_id=?", id).Scan(&count)
	if count != 1 {
		t.Fatal("sources were not replaced exactly once")
	}
	db.DB().Exec("UPDATE orders SET submitted_at=1 WHERE id=?", id)
	call(token, 409)
}
