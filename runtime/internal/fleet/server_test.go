package fleet

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFleetIsolationPersistenceAndOffline(t *testing.T) {
	c := Config{AdminToken: strings.Repeat("a", 32), Devices: []Device{{ID: "pi1", Token: strings.Repeat("b", 32)}, {ID: "pi2", Token: strings.Repeat("c", 32)}}}
	path := filepath.Join(t.TempDir(), "state.json")
	s, e := New(c, path)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/api/heartbeat", c.AdminToken, `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/heartbeat", c.Devices[0].Token, `{"paused":true,"pendingOrders":2}`); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/kiosks", c.Devices[0].Token, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	var rows []Record
	w := call("GET", "/api/kiosks", c.AdminToken, "")
	if json.Unmarshal(w.Body.Bytes(), &rows) != nil || !rows[0].Online || rows[1].Online || rows[0].Snapshot.PendingOrders != 2 {
		t.Fatal(w.Body.String())
	}
	s, e = New(c, path)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return now.Add(91 * time.Second) }
	w = call("GET", "/api/kiosks", c.AdminToken, "")
	json.Unmarshal(w.Body.Bytes(), &rows)
	if rows[0].Online || rows[0].ReceivedAt != 1000 {
		t.Fatal(w.Body.String())
	}
	if w = call("POST", "/api/heartbeat", c.Devices[0].Token, `{"customerDocument":"secret"}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
