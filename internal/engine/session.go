package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// session is one running share. Its fields are guarded by Engine.mu unless
// noted otherwise.
type session struct {
	e      *Engine
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	keys       shareKeys
	root       string
	cert       tls.Certificate
	ln         net.Listener
	listenPort int

	idx      *Index
	idxDirty bool
	peers    map[string]*peerConn
	remote   map[string]map[string]*FileInfo // device -> path -> entry
	dial     map[string]*dialState
	warned   map[string]time.Time

	scanReq chan struct{}
	pullReq chan struct{}

	scannedOnce bool
	folderErr   string
	lastSync    time.Time

	inflight  map[string]bool
	running   int
	retry     map[string]*retryState
	transfers map[string]*Transfer
}

type dialState struct {
	dialing bool
	next    time.Time
	backoff time.Duration
}

type retryState struct {
	n    int
	next time.Time
	msg  string
}

func (s *session) kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *session) listen() error {
	port := s.e.opts.ListenPort
	var err error
	if port > 0 {
		for p := port; p < port+10; p++ {
			if s.ln, err = net.Listen("tcp", net.JoinHostPort(s.e.opts.ListenHost, strconv.Itoa(p))); err == nil {
				break
			}
		}
	}
	if s.ln == nil {
		if s.ln, err = net.Listen("tcp", net.JoinHostPort(s.e.opts.ListenHost, "0")); err != nil {
			return fmt.Errorf("can't open a network port: %v", err)
		}
	}
	s.listenPort = s.ln.Addr().(*net.TCPAddr).Port
	return nil
}

func (s *session) goroutine(f func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

func (s *session) run() {
	s.goroutine(s.acceptLoop)
	s.goroutine(s.scanLoop)
	s.goroutine(s.watchLoop)
	s.goroutine(s.pullLoop)
	s.goroutine(s.dialLoop)
	s.goroutine(s.housekeeping)
	if s.e.opts.DiscoveryPort > 0 {
		s.goroutine(s.discoveryLoop)
	}
	s.kick(s.scanReq)
}

func (s *session) stop() {
	s.cancel()
	s.ln.Close()
	s.e.mu.Lock()
	var ps []*peerConn
	for _, p := range s.peers {
		ps = append(ps, p)
	}
	s.e.mu.Unlock()
	for _, p := range ps {
		p.close()
	}
	s.wg.Wait()
}

func (s *session) sleep(d time.Duration) bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ---- connections ----

func (s *session) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			s.sleep(100 * time.Millisecond)
			continue
		}
		go s.handshake(c, false)
	}
}

func tuneTCP(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(10 * time.Second)
		tc.SetNoDelay(true)
	}
}

// handshake authenticates a new connection and registers it. It reports
// whether the device is now connected.
func (s *session) handshake(raw net.Conn, outbound bool) bool {
	tuneTCP(raw)
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	var tc *tls.Conn
	if outbound {
		tc = tls.Client(raw, clientTLS(s.cert))
	} else {
		tc = tls.Server(raw, serverTLS(s.cert))
	}
	if err := tc.HandshakeContext(s.ctx); err != nil {
		raw.Close()
		return false
	}
	s.e.mu.Lock()
	me := authHello{ID: s.e.cfg.DeviceID, Name: s.e.cfg.DeviceName, Port: s.listenPort, Version: AppVersion}
	s.e.mu.Unlock()
	them, err := exchangeAuth(tc, s.keys.auth, me, outbound)
	if err != nil {
		if errors.Is(err, errBadKey) {
			ip := hostOf(raw.RemoteAddr().String())
			s.e.mu.Lock()
			if s.warned == nil {
				s.warned = map[string]time.Time{}
			}
			if time.Since(s.warned[ip]) > 10*time.Minute {
				s.warned[ip] = time.Now()
				s.e.eventLocked("warn", "A device at %s tried to connect with a different share key", ip)
			}
			s.e.mu.Unlock()
		}
		raw.Close()
		return false
	}
	if them.ID == me.ID {
		raw.Close()
		return false
	}
	raw.SetDeadline(time.Time{})
	pc := newPeerConn(s, tc, them, outbound)
	if !s.register(pc) {
		tc.Close()
		return true // an equivalent connection already exists
	}
	pc.start()
	return true
}

func hostOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

func (s *session) register(pc *peerConn) bool {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	replaced := false
	if old := s.peers[pc.id]; old != nil {
		// Two connections to the same device (both sides dialed at once, or a
		// reconnect after a network change). If the old one is still alive,
		// both sides keep the one dialed by the lower device ID so they agree.
		if time.Since(old.lastRecvTime()) < 8*time.Second {
			wantOutbound := e.cfg.DeviceID < pc.id
			if old.outbound == wantOutbound && pc.outbound != wantOutbound {
				return false
			}
		}
		delete(s.peers, pc.id)
		delete(s.remote, pc.id)
		go old.close()
		replaced = true
	}
	s.peers[pc.id] = pc
	s.remote[pc.id] = map[string]*FileInfo{}
	addr := net.JoinHostPort(pc.remoteIP(), strconv.Itoa(pc.port))
	s.rememberLocked(pc.id, pc.name, addr)
	if d := s.dial[pc.id]; d != nil {
		d.backoff, d.next = 0, time.Time{}
	}
	if !replaced {
		e.eventLocked("peer", "Connected to %s", pc.name)
	}
	return true
}

func (s *session) unregister(pc *peerConn) {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.peers[pc.id] != pc {
		return
	}
	delete(s.peers, pc.id)
	delete(s.remote, pc.id)
	if k := e.cfg.Known[pc.id]; k != nil {
		k.LastSeen = time.Now()
		e.cfgDirty = true
	}
	if s.ctx.Err() == nil {
		e.eventLocked("peer", "Disconnected from %s", pc.name)
	}
	s.kick(s.pullReq)
}

// rememberLocked records where a device can be reached.
func (s *session) rememberLocked(id, name, addr string) {
	e := s.e
	k := e.cfg.Known[id]
	if k == nil {
		k = &KnownDevice{}
		e.cfg.Known[id] = k
	}
	if name != "" {
		k.Name = name
	}
	k.LastSeen = time.Now()
	if addr != "" {
		addrs := []string{addr}
		for _, a := range k.Addrs {
			if a != addr && len(addrs) < 4 {
				addrs = append(addrs, a)
			}
		}
		k.Addrs = addrs
	}
	e.cfgDirty = true
}

// discovered is called when a LAN announcement arrives from a device.
func (s *session) discovered(id, name, addr string) {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == e.cfg.DeviceID {
		return
	}
	s.rememberLocked(id, name, addr)
	if s.peers[id] == nil {
		d := s.dial[id]
		if d == nil {
			d = &dialState{}
			s.dial[id] = d
		}
		d.backoff, d.next = 0, time.Time{}
	}
}

// networkChanged resets reconnect timers so a laptop that just rejoined the
// network reconnects immediately instead of waiting out a backoff.
func (s *session) networkChanged() {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range s.dial {
		d.backoff, d.next = 0, time.Time{}
	}
	e.log.Printf("network change detected")
}

type dialTarget struct {
	key   string
	addrs []string
}

func (s *session) dialLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		e := s.e
		e.mu.Lock()
		now := time.Now()
		var targets []dialTarget
		for id, k := range e.cfg.Known {
			if s.peers[id] != nil || len(k.Addrs) == 0 {
				continue
			}
			if time.Since(k.LastSeen) > 30*24*time.Hour {
				continue
			}
			targets = append(targets, dialTarget{id, append([]string(nil), k.Addrs...)})
		}
		connectedIPs := map[string]bool{}
		for _, p := range s.peers {
			connectedIPs[p.remoteIP()] = true
		}
		for _, a := range e.cfg.ExtraPeers {
			addr := a
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addr = net.JoinHostPort(strings.Trim(addr, "[]"), strconv.Itoa(DefaultListenPort))
			}
			if connectedIPs[hostOf(addr)] {
				continue
			}
			targets = append(targets, dialTarget{"addr:" + addr, []string{addr}})
		}
		var ready []dialTarget
		for _, tg := range targets {
			d := s.dial[tg.key]
			if d == nil {
				d = &dialState{}
				s.dial[tg.key] = d
			}
			if d.dialing || now.Before(d.next) {
				continue
			}
			d.dialing = true
			ready = append(ready, tg)
		}
		e.mu.Unlock()
		for _, tg := range ready {
			go s.dialOne(tg)
		}
	}
}

func (s *session) dialOne(tg dialTarget) {
	ok := false
	for _, addr := range tg.addrs {
		d := net.Dialer{Timeout: 3 * time.Second, KeepAlive: 10 * time.Second}
		c, err := d.DialContext(s.ctx, "tcp", addr)
		if err != nil {
			continue
		}
		if s.handshake(c, true) {
			ok = true
			break
		}
	}
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	d := s.dial[tg.key]
	d.dialing = false
	if ok {
		d.backoff = 0
		d.next = time.Now().Add(10 * time.Second)
		return
	}
	d.backoff = min(max(2*time.Second, d.backoff*2), 30*time.Second)
	d.next = time.Now().Add(d.backoff)
}

// ---- index exchange ----

func (s *session) putLocked(fi *FileInfo) {
	s.idx.put(fi)
	s.idxDirty = true
	for _, p := range s.peers {
		p.kickSend()
	}
}

func (s *session) onIndex(pc *peerConn, files []FileInfo, final bool) {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	m := s.remote[pc.id]
	if s.peers[pc.id] != pc || m == nil {
		return
	}
	for i := range files {
		f := files[i]
		if !validPath(f.Path) {
			continue
		}
		if f.Version == nil {
			f.Version = Vector{}
		}
		m[f.Path] = &f
	}
	if final {
		pc.indexReceived = true
	}
	s.kick(s.pullReq)
}

// ---- housekeeping ----

func (s *session) housekeeping() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		e := s.e
		e.mu.Lock()
		if s.idxDirty {
			if err := s.idx.save(e.indexPath()); err != nil {
				e.log.Printf("save index: %v", err)
			} else {
				s.idxDirty = false
			}
		}
		if e.cfgDirty {
			e.saveConfigLocked()
		}
		e.mu.Unlock()
		if time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			pruneArchive(s.root, 30*24*time.Hour)
			pruneDir(filepath.Join(s.root, metaDir, "tmp"), 3*24*time.Hour)
		}
	}
}

func (s *session) setFolderErr(msg string) {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if msg != "" && s.folderErr != msg {
		e.eventLocked("warn", "%s", msg)
	}
	s.folderErr = msg
}

func ensureMeta(root, shareID string) error {
	dir := filepath.Join(root, metaDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	hideFile(dir)
	return os.WriteFile(filepath.Join(dir, "folder-id"), []byte(shareID+"\n"), 0o644)
}
