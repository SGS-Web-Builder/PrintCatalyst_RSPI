package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	Dashboard string `json:"dashboard"`
}
type Config struct {
	AdminToken string   `json:"adminToken"`
	Devices    []Device `json:"devices"`
}
type Snapshot struct {
	Paused        bool   `json:"paused"`
	Printers      int    `json:"printers"`
	Alerts        int    `json:"alerts"`
	PendingOrders int    `json:"pendingOrders"`
	Version       string `json:"version"`
}
type Event struct {
	At     int64  `json:"at"`
	Detail string `json:"detail"`
}
type Record struct {
	History    []Event  `json:"history"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Dashboard  string   `json:"dashboard"`
	ReceivedAt int64    `json:"receivedAt"`
	Online     bool     `json:"online"`
	Snapshot   Snapshot `json:"snapshot"`
}
type Server struct {
	mu      sync.Mutex
	config  Config
	path    string
	records map[string]Record
	now     func() time.Time
}

func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func New(config Config, path string) (*Server, error) {
	if len(config.AdminToken) < 32 || strings.Contains(config.AdminToken, "REPLACE_WITH") {
		return nil, errors.New("admin token must contain at least 32 random characters")
	}
	seen := map[string]bool{digest(config.AdminToken): true}
	ids := map[string]bool{}
	for _, d := range config.Devices {
		if d.ID == "" || len(d.ID) > 128 || ids[d.ID] || len(d.Token) < 32 || strings.Contains(d.Token, "REPLACE_WITH") || seen[digest(d.Token)] {
			return nil, errors.New("devices require unique IDs and distinct random tokens")
		}
		if d.Dashboard != "" {
			u, e := url.Parse(d.Dashboard)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return nil, errors.New("dashboard URL must use HTTPS")
			}
		}
		seen[digest(d.Token)] = true
		ids[d.ID] = true
	}
	s := &Server{config: config, path: path, records: map[string]Record{}, now: time.Now}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &s.records); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/heartbeat", s.heartbeat)
	mux.HandleFunc("GET /api/kiosks", s.list)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Write([]byte(page))
	})
	mux.HandleFunc("GET /fleet.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write([]byte(styles))
	})
	mux.HandleFunc("GET /fleet.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Write([]byte(script))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Same-origin access required", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func token(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var device *Device
	hash := digest(token(r))
	for i := range s.config.Devices {
		if digest(s.config.Devices[i].Token) == hash {
			device = &s.config.Devices[i]
			break
		}
	}
	if device == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	var snap Snapshot
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&snap) != nil || snap.Printers < 0 || snap.Alerts < 0 || snap.PendingOrders < 0 || len(snap.Version) > 80 {
		http.Error(w, "Invalid heartbeat", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := Record{ID: device.ID, Name: device.Name, Dashboard: device.Dashboard, ReceivedAt: s.now().Unix(), Snapshot: snap}
	previous := s.records[device.ID]
	record.History = append([]Event{}, previous.History...)
	detail := ""
	if previous.ReceivedAt == 0 {
		detail = "First heartbeat"
	} else if s.now().Unix()-previous.ReceivedAt >= 90 {
		detail = "Connection restored after heartbeat gap"
	} else if previous.Snapshot.Paused != snap.Paused {
		detail = "Intake state changed"
	} else if previous.Snapshot.Alerts != snap.Alerts {
		detail = "Printer alert count changed"
	}
	if detail != "" {
		record.History = append(record.History, Event{At: s.now().Unix(), Detail: detail})
	}
	if len(record.History) > 50 {
		record.History = record.History[len(record.History)-50:]
	}
	next := map[string]Record{}
	for k, v := range s.records {
		next[k] = v
	}
	next[device.ID] = record
	data, err := json.Marshal(next)
	if err != nil {
		http.Error(w, "Storage unavailable", 503)
		return
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err == nil {
		var f *os.File
		f, err = os.CreateTemp(filepath.Dir(s.path), "heartbeat-*")
		if err == nil {
			tmp := f.Name()
			defer os.Remove(tmp)
			err = f.Chmod(0600)
			if err == nil {
				_, err = f.Write(data)
			}
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(tmp, s.path)
			}
		}
	}
	if err != nil {
		http.Error(w, "Storage unavailable", 503)
		return
	}
	s.records = next
	w.WriteHeader(204)
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if digest(token(r)) != digest(s.config.AdminToken) {
		http.Error(w, "Unauthorized", 401)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []Record{}
	for _, d := range s.config.Devices {
		v := s.records[d.ID]
		v.ID = d.ID
		v.Name = d.Name
		v.Dashboard = d.Dashboard
		age := s.now().Unix() - v.ReceivedAt
		v.Online = v.ReceivedAt > 0 && age >= 0 && age < 90
		list = append(list, v)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

const page = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>PrintCatalyst fleet</title><link rel="stylesheet" href="/fleet.css"><script src="/fleet.js" defer></script></head><body><main><h1>PrintCatalyst fleet</h1><form id="login"><label>Fleet access key <input id="token" type="password" autocomplete="off" required></label><button>Connect</button></form><button id="logout" hidden>Sign out</button><p id="message" role="status"></p><section id="kiosks"></section></main></body></html>`
const script = `let key='',timer,generation=0;const $=id=>document.getElementById(id);const node=(tag,text)=>{const n=document.createElement(tag);n.textContent=text;return n;};async function refresh(){clearTimeout(timer);const current=generation;try{const r=await fetch('/api/kiosks',{headers:{Authorization:'Bearer '+key},cache:'no-store',signal:AbortSignal.timeout(10000)});if(!r.ok)throw Error('Could not authenticate or load fleet');const rows=await r.json();if(current!==generation)return;$('kiosks').replaceChildren();for(const k of rows){const card=node('article','');card.append(node('h2',k.name||k.id),node('p',k.online?'Online':'Offline / never seen'),node('p',k.receivedAt?'Last heartbeat '+new Date(k.receivedAt*1000).toLocaleString():'No heartbeat yet'),node('p',k.snapshot.paused?'Intake paused':'Intake status: '+(k.receivedAt?'open at last heartbeat':'unknown')),node('p','Pending orders: '+k.snapshot.pendingOrders+' | Alerts: '+k.snapshot.alerts));if(k.history?.length){const history=node('details',''),summary=node('summary','Recent events');history.append(summary);for(const event of k.history.slice().reverse())history.append(node('p',new Date(event.at*1000).toLocaleString()+' · '+event.detail));card.append(history);}if(k.dashboard){const a=node('a','Manage kiosk');a.href=k.dashboard;a.rel='noopener noreferrer';card.append(a);}$('kiosks').append(card);}$('message').textContent='Updated '+new Date().toLocaleTimeString();$('login').hidden=true;$('logout').hidden=false;}catch(e){if(current!==generation)return;$('message').textContent=e.message+'. Displayed data may be stale.';}if(key)timer=setTimeout(refresh,15000);}$('login').onsubmit=e=>{e.preventDefault();generation++;key=$('token').value;$('token').value='';refresh();};$('logout').onclick=()=>{generation++;key='';clearTimeout(timer);$('kiosks').replaceChildren();$('login').hidden=false;$('logout').hidden=true;$('message').textContent='Signed out';};`

const styles = `:root{font-family:system-ui,sans-serif;color:#16362d;background:#f3f6f5}main{max-width:1200px;margin:auto;padding:24px}h1{font-size:28px}form{background:white;padding:24px;border-radius:12px;max-width:500px}input,button{font:inherit;padding:12px;border:1px solid #bccdc5;border-radius:8px}button,a{background:#176545;color:white;padding:12px;border-radius:8px;text-decoration:none}#kiosks{display:grid;grid-template-columns:repeat(auto-fit,minmax(270px,1fr));gap:16px}article{background:white;border:1px solid #dce6e0;border-radius:12px;padding:24px}article a{display:inline-block;margin-top:12px}details{padding:12px 0}#message{min-height:24px}[hidden]{display:none!important}`
