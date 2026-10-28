package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"folderchain/internal/engine"
)

//go:embed web
var webFS embed.FS

// The UI talks to the app over a localhost-only HTTP API protected by a
// random per-run token, so other programs and web pages can't drive it.
type uiServer struct {
	eng   *engine.Engine
	win   *appWindow
	token string
	host  string
}

func startUIServer(eng *engine.Engine) (*uiServer, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	b := make([]byte, 16)
	rand.Read(b)
	s := &uiServer{eng: eng, token: hex.EncodeToString(b), host: ln.Addr().String()}

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.api)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'")
		files.ServeHTTP(w, r)
	}))
	go http.Serve(ln, mux)
	return s, "http://" + s.host + "/?token=" + s.token, nil
}

type uiState struct {
	engine.State
	Autostart bool     `json:"autostart"`
	LocalIPs  []string `json:"localIPs"`
}

func (s *uiServer) api(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.host || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-FC-Token")), []byte(s.token)) != 1 {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	var body struct {
		Mode       string   `json:"mode"`
		Key        string   `json:"key"`
		Folder     string   `json:"folder"`
		Name       string   `json:"name"`
		Autostart  *bool    `json:"autostart"`
		Paused     bool     `json:"paused"`
		Archive    bool     `json:"archive"`
		Start      string   `json:"start"`
		ID         string   `json:"id"`
		DeviceName *string  `json:"deviceName"`
		ExtraPeers []string `json:"extraPeers"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			reply(w, nil, errors.New("bad request"))
			return
		}
	}
	e := s.eng
	switch strings.TrimPrefix(r.URL.Path, "/api/") {
	case "state":
		reply(w, uiState{State: e.State(), Autostart: autostartEnabled(), LocalIPs: engine.LocalIPs()}, nil)
	case "defaults":
		name, _ := os.Hostname()
		home, _ := os.UserHomeDir()
		reply(w, map[string]string{"deviceName": name, "suggestedFolder": filepath.Join(home, "GCode")}, nil)
	case "setup":
		key := body.Key
		if body.Mode == "create" {
			key = engine.NewShareKey()
		}
		if strings.TrimSpace(body.Folder) == "" {
			reply(w, nil, errors.New("choose a folder to sync"))
			return
		}
		err := e.Setup(key, body.Folder, body.Name)
		if err == nil && body.Autostart != nil {
			setAutostart(*body.Autostart)
		}
		reply(w, map[string]bool{"ok": err == nil}, err)
	case "change-folder":
		reply(w, map[string]bool{"ok": true}, e.Setup(e.ShareKey(), body.Folder, ""))
	case "pick-folder":
		p, err := pickFolder(body.Start, s.win.owner())
		reply(w, map[string]string{"path": p}, err)
	case "open-folder":
		dir := e.Folder()
		if body.Archive {
			dir = filepath.Join(dir, ".folderchain", "archive")
			os.MkdirAll(dir, 0o755)
		}
		reply(w, map[string]bool{"ok": true}, openFolder(dir))
	case "pause":
		e.SetPaused(body.Paused)
		reply(w, map[string]bool{"ok": true}, nil)
	case "settings":
		peers := body.ExtraPeers
		if peers == nil {
			peers = []string{}
		}
		e.UpdateSettings(engine.Settings{DeviceName: body.DeviceName, ExtraPeers: &peers})
		var err error
		if body.Autostart != nil {
			err = setAutostart(*body.Autostart)
		}
		reply(w, map[string]bool{"ok": true}, err)
	case "share-key":
		reply(w, map[string]string{"key": e.ShareKey()}, nil)
	case "firewall":
		reply(w, map[string]bool{"ok": true}, allowThroughFirewall(s.win.owner()))
	case "leave":
		e.Leave()
		reply(w, map[string]bool{"ok": true}, nil)
	case "rescan":
		e.Rescan()
		reply(w, map[string]bool{"ok": true}, nil)
	case "forget":
		e.ForgetDevice(body.ID)
		reply(w, map[string]bool{"ok": true}, nil)
	default:
		http.NotFound(w, r)
	}
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(v)
}
