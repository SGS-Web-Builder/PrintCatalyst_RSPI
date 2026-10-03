package localserver_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"testing"
)

func TestPortalBranding(t *testing.T) {
	ts, db := portalFixture(t)
	cookie, csrf := signedInAsOwner(t, ts.URL)
	path := "/api/v1/owner/portal-branding"
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("owner branding exposed")
	}
	picture := func(w, h int) string {
		var out bytes.Buffer
		png.Encode(&out, image.NewRGBA(image.Rect(0, 0, w, h)))
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
	}
	value := map[string]string{"title": "My Print Shop", "subtitle": "Fast local printing", "logo": picture(600, 160), "icon": picture(128, 128)}
	save := func(want int) {
		t.Helper()
		body, _ := json.Marshal(value)
		r := putJSON(t, ts.URL, path, cookie, csrf, body)
		defer r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("save status %d want %d", r.StatusCode, want)
		}
	}
	save(200)
	value["icon"] = picture(128, 64)
	save(400)
	value["icon"] = "data:image/svg+xml;base64,PHN2Zz4="
	save(400)
	value["icon"] = ""
	value["logo"] = picture(2049, 2)
	save(400)
	resp, err = http.Get(ts.URL + "/api/v1/portal/branding")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if result["title"] != "My Print Shop" || result["icon"] != picture(128, 128) {
		t.Fatal("invalid update changed saved branding")
	}
	value["logo"] = ""
	save(200)
	if _, err := db.DB().Exec(`UPDATE business_profile SET profile_json=json_set(profile_json,'$.name','Merchant Shop')`); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(ts.URL + "/api/v1/portal/branding")
	if err != nil {
		t.Fatal(err)
	}
	result = map[string]string{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if err != nil || result["businessName"] != "Merchant Shop" || result["logo"] != "" {
		t.Fatalf("business fallback: %v %v", result, err)
	}

}
