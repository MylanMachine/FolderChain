// Package engine implements FolderChain's peer-to-peer folder sync: local
// scanning, LAN discovery, authenticated encrypted connections, and pulling
// changes from other devices.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const AppVersion = "1.0.0"

const (
	DefaultListenPort    = 47124
	DefaultDiscoveryPort = 47123
)

type KnownDevice struct {
	Name     string
	Addrs    []string // most recent first
	LastSeen time.Time
}

type Config struct {
	DeviceID   string
	DeviceName string
	ShareKey   string
	Folder     string
	ExtraPeers []string
	Paused     bool
	Known      map[string]*KnownDevice
}

type Options struct {
	DataDir       string
	ListenHost    string // "" = all interfaces
	ListenPort    int    // 0 = default; -1 = any free port
	DiscoveryPort int    // 0 = default; -1 = discovery disabled
	ScanInterval  time.Duration
	Log           *log.Logger
}

type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // in, out, del, conflict, peer, warn, info
	Text string    `json:"text"`
}

type Engine struct {
	opts Options
	log  *log.Logger

	mu       sync.Mutex
	cfg      Config
	sess     *session
	events   []Event
	cfgDirty bool
}

func New(opts Options) (*Engine, error) {
	if opts.ListenPort == 0 {
		opts.ListenPort = DefaultListenPort
	}
	if opts.DiscoveryPort == 0 {
		opts.DiscoveryPort = DefaultDiscoveryPort
	}
	if opts.ScanInterval == 0 {
		opts.ScanInterval = 60 * time.Second
	}
	if opts.Log == nil {
		opts.Log = log.New(io.Discard, "", 0)
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	e := &Engine{opts: opts, log: opts.Log}
	b, err := os.ReadFile(e.configPath())
	if err == nil {
		if err := json.Unmarshal(b, &e.cfg); err != nil {
			e.log.Printf("config unreadable, starting fresh: %v", err)
		}
	}
	if e.cfg.DeviceID == "" {
		e.cfg.DeviceID = newDeviceID()
	}
	if e.cfg.DeviceName == "" {
		e.cfg.DeviceName, _ = os.Hostname()
	}
	if e.cfg.Known == nil {
		e.cfg.Known = map[string]*KnownDevice{}
	}
	e.saveConfigLocked()
	return e, nil
}

func (e *Engine) configPath() string { return filepath.Join(e.opts.DataDir, "config.json") }
func (e *Engine) indexPath() string  { return filepath.Join(e.opts.DataDir, "index.json") }

func (e *Engine) saveConfigLocked() {
	b, _ := json.MarshalIndent(e.cfg, "", "  ")
	if err := writeFileAtomic(e.configPath(), b); err != nil {
		e.log.Printf("save config: %v", err)
	}
	e.cfgDirty = false
}

func (e *Engine) eventLocked(kind, format string, args ...any) {
	ev := Event{Time: time.Now(), Kind: kind, Text: fmt.Sprintf(format, args...)}
	e.log.Printf("[%s] %s", kind, ev.Text)
	e.events = append(e.events, ev)
	if len(e.events) > 200 {
		e.events = e.events[len(e.events)-200:]
	}
}

func (e *Engine) Configured() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.ShareKey != "" && e.cfg.Folder != ""
}

// Start begins syncing if a share and folder are configured and not paused.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startLocked()
}

func (e *Engine) startLocked() error {
	if e.sess != nil || e.cfg.ShareKey == "" || e.cfg.Folder == "" || e.cfg.Paused {
		return nil
	}
	secret, err := ParseShareKey(e.cfg.ShareKey)
	if err != nil {
		return err
	}
	cert, err := newTLSCert()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{
		e:         e,
		ctx:       ctx,
		cancel:    cancel,
		keys:      deriveKeys(secret),
		root:      e.cfg.Folder,
		cert:      cert,
		idx:       loadIndex(e.indexPath(), e.cfg.Folder),
		peers:     map[string]*peerConn{},
		remote:    map[string]map[string]*FileInfo{},
		dial:      map[string]*dialState{},
		scanReq:   make(chan struct{}, 1),
		pullReq:   make(chan struct{}, 1),
		inflight:  map[string]bool{},
		retry:     map[string]*retryState{},
		transfers: map[string]*Transfer{},
	}
	if err := s.listen(); err != nil {
		cancel()
		return err
	}
	e.sess = s
	s.run()
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	s := e.sess
	e.sess = nil
	e.mu.Unlock()
	if s == nil {
		return
	}
	s.stop()
	e.mu.Lock()
	if err := s.idx.save(e.indexPath()); err != nil {
		e.log.Printf("save index: %v", err)
	}
	e.saveConfigLocked()
	e.mu.Unlock()
}

// Setup joins (or creates) a share and points it at a folder.
func (e *Engine) Setup(shareKey, folder, name string) error {
	key, err := NormalizeShareKey(shareKey)
	if err != nil {
		return err
	}
	folder, err = filepath.Abs(strings.TrimSpace(folder))
	if err != nil || folder == "" {
		return errors.New("choose a folder to sync")
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return fmt.Errorf("can't use that folder: %v", err)
	}
	e.Stop()
	e.mu.Lock()
	defer e.mu.Unlock()
	if key != e.cfg.ShareKey {
		e.cfg.Known = map[string]*KnownDevice{}
		os.Remove(e.indexPath())
	}
	if folder != e.cfg.Folder {
		os.Remove(e.indexPath())
	}
	e.cfg.ShareKey, e.cfg.Folder, e.cfg.Paused = key, folder, false
	if n := strings.TrimSpace(name); n != "" {
		e.cfg.DeviceName = n
	}
	e.saveConfigLocked()
	e.eventLocked("info", "Syncing %s", folder)
	return e.startLocked()
}

// Leave disconnects from the share. Files in the folder are left untouched.
func (e *Engine) Leave() {
	e.Stop()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.ShareKey, e.cfg.Folder = "", ""
	e.cfg.Known = map[string]*KnownDevice{}
	os.Remove(e.indexPath())
	e.saveConfigLocked()
	e.events = nil
}

func (e *Engine) SetPaused(p bool) {
	e.mu.Lock()
	e.cfg.Paused = p
	e.saveConfigLocked()
	e.mu.Unlock()
	if p {
		e.Stop()
		e.mu.Lock()
		e.eventLocked("info", "Syncing paused")
		e.mu.Unlock()
	} else {
		e.mu.Lock()
		e.eventLocked("info", "Syncing resumed")
		err := e.startLocked()
		e.mu.Unlock()
		if err != nil {
			e.log.Printf("resume: %v", err)
		}
	}
}

type Settings struct {
	DeviceName *string   `json:"deviceName"`
	ExtraPeers *[]string `json:"extraPeers"`
}

func (e *Engine) UpdateSettings(st Settings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st.DeviceName != nil && strings.TrimSpace(*st.DeviceName) != "" {
		e.cfg.DeviceName = strings.TrimSpace(*st.DeviceName)
	}
	if st.ExtraPeers != nil {
		var ps []string
		for _, p := range *st.ExtraPeers {
			if p = strings.TrimSpace(p); p != "" {
				ps = append(ps, p)
			}
		}
		e.cfg.ExtraPeers = ps
	}
	e.saveConfigLocked()
	if e.sess != nil {
		e.sess.kick(e.sess.scanReq)
	}
}

func (e *Engine) Rescan() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess != nil {
		e.sess.kick(e.sess.scanReq)
		e.sess.kick(e.sess.pullReq)
	}
}

// ForgetDevice removes an offline device from the list.
func (e *Engine) ForgetDevice(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.cfg.Known, id)
	e.saveConfigLocked()
}

func (e *Engine) ShareKey() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.ShareKey
}

func (e *Engine) Folder() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Folder
}

// ---- status for the UI ----

type DeviceState struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Online   bool      `json:"online"`
	Addr     string    `json:"addr"`
	LastSeen time.Time `json:"lastSeen"`
	RTTms    float64   `json:"rttMs"`
	InSync   bool      `json:"inSync"`
	Pending  int       `json:"pending"`
}

type Transfer struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Done    int64     `json:"done"`
	Peer    string    `json:"peer"`
	Upload  bool      `json:"upload"`
	Started time.Time `json:"started"`
}

type State struct {
	Configured  bool          `json:"configured"`
	Running     bool          `json:"running"`
	Paused      bool          `json:"paused"`
	Status      string        `json:"status"` // setup, paused, error, scanning, syncing, waiting, synced
	StatusText  string        `json:"statusText"`
	DeviceID    string        `json:"deviceId"`
	DeviceName  string        `json:"deviceName"`
	Folder      string        `json:"folder"`
	FolderError string        `json:"folderError"`
	Files       int           `json:"files"`
	Bytes       int64         `json:"bytes"`
	Queue       int           `json:"queue"`
	LastSync    time.Time     `json:"lastSync"`
	Devices     []DeviceState `json:"devices"`
	Transfers   []Transfer    `json:"transfers"`
	Problems    []string      `json:"problems"`
	Events      []Event       `json:"events"`
	ListenPort  int           `json:"listenPort"`
	ExtraPeers  []string      `json:"extraPeers"`
	Version     string        `json:"version"`
}

func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := State{
		Configured: e.cfg.ShareKey != "" && e.cfg.Folder != "",
		Paused:     e.cfg.Paused,
		DeviceID:   e.cfg.DeviceID,
		DeviceName: e.cfg.DeviceName,
		Folder:     e.cfg.Folder,
		ExtraPeers: append([]string{}, e.cfg.ExtraPeers...),
		Version:    AppVersion,
		Devices:    []DeviceState{},
		Transfers:  []Transfer{},
		Problems:   []string{},
	}
	for i := len(e.events) - 1; i >= 0 && len(st.Events) < 60; i-- {
		st.Events = append(st.Events, e.events[i])
	}
	s := e.sess
	if s != nil {
		st.Running = true
		st.ListenPort = s.listenPort
		st.FolderError = s.folderErr
		st.LastSync = s.lastSync
		for _, f := range s.idx.Files {
			if !f.Deleted && !f.Dir {
				st.Files++
				st.Bytes += f.Size
			}
		}
		jobs := s.needsLocked()
		st.Queue = len(jobs)
		perPeer := map[string]int{}
		for _, j := range jobs {
			perPeer[j.from]++
		}
		for _, t := range s.transfers {
			st.Transfers = append(st.Transfers, *t)
		}
		sort.Slice(st.Transfers, func(i, j int) bool { return st.Transfers[i].Started.Before(st.Transfers[j].Started) })
		for p, r := range s.retry {
			if r.msg != "" {
				st.Problems = append(st.Problems, fmt.Sprintf("%s — %s", p, r.msg))
			}
		}
		sort.Strings(st.Problems)
		for id, pc := range s.peers {
			theirs := s.theyNeedLocked(id)
			st.Devices = append(st.Devices, DeviceState{
				ID: id, Name: pc.name, Online: true, Addr: pc.remoteIP(), LastSeen: time.Now(),
				RTTms: float64(pc.rtt.Load()) / 1e6, Pending: perPeer[id] + theirs, InSync: perPeer[id]+theirs == 0 && pc.indexReceived,
			})
		}
	}
	for id, k := range e.cfg.Known {
		if s != nil && s.peers[id] != nil {
			continue
		}
		addr := ""
		if len(k.Addrs) > 0 {
			addr = k.Addrs[0]
		}
		st.Devices = append(st.Devices, DeviceState{ID: id, Name: k.Name, Addr: addr, LastSeen: k.LastSeen})
	}
	sort.Slice(st.Devices, func(i, j int) bool {
		a, b := st.Devices[i], st.Devices[j]
		if a.Online != b.Online {
			return a.Online
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	online := 0
	for _, d := range st.Devices {
		if d.Online {
			online++
		}
	}
	switch {
	case !st.Configured:
		st.Status, st.StatusText = "setup", "Not set up"
	case st.Paused:
		st.Status, st.StatusText = "paused", "Paused"
	case s == nil:
		st.Status, st.StatusText = "error", "Not running"
	case s.folderErr != "":
		st.Status, st.StatusText = "error", "Folder unavailable"
	case !s.scannedOnce:
		st.Status, st.StatusText = "scanning", "Scanning folder…"
	case len(st.Transfers) > 0 || (st.Queue > 0 && online > 0):
		n := max(st.Queue, len(st.Transfers))
		st.Status, st.StatusText = "syncing", fmt.Sprintf("Syncing %d item%s…", n, plural(n))
	case online == 0:
		st.Status, st.StatusText = "waiting", "Looking for devices…"
	default:
		allSynced := true
		for _, d := range st.Devices {
			if d.Online && !d.InSync {
				allSynced = false
			}
		}
		if allSynced {
			st.Status, st.StatusText = "synced", "Up to date"
		} else {
			st.Status, st.StatusText = "syncing", "Syncing…"
		}
	}
	return st
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
