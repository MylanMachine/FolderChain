package engine

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type node struct {
	t      *testing.T
	e      *Engine
	folder string
	port   int
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newNode(t *testing.T, name, key string, port int, peers ...int) *node {
	dir := t.TempDir()
	lg := log.New(&testLog{t, name}, "", 0)
	e, err := New(Options{DataDir: filepath.Join(dir, "data"), ListenHost: "127.0.0.1", ListenPort: port, DiscoveryPort: -1, ScanInterval: 2 * time.Second, Log: lg})
	if err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, p := range peers {
		addrs = append(addrs, "127.0.0.1:"+strconv.Itoa(p))
	}
	e.UpdateSettings(Settings{ExtraPeers: &addrs})
	n := &node{t: t, e: e, folder: filepath.Join(dir, "share"), port: port}
	if err := e.Setup(key, n.folder, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return n
}

type testLog struct {
	t    *testing.T
	name string
}

func (l *testLog) Write(p []byte) (int, error) {
	l.t.Logf("%s: %s", l.name, strings.TrimSpace(string(p)))
	return len(p), nil
}

func pair(t *testing.T) (*node, *node) {
	key := NewShareKey()
	pa, pb := freePort(t), freePort(t)
	a := newNode(t, "Alpha", key, pa, pb)
	b := newNode(t, "Bravo", key, pb, pa)
	waitFor(t, 15*time.Second, "devices connect", func() bool { return online(a) && online(b) })
	return a, b
}

func online(n *node) bool {
	for _, d := range n.e.State().Devices {
		if d.Online {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// write creates a file with an mtime in the past so the scanner doesn't
// treat it as still being written.
func (n *node) write(rel string, data []byte) {
	p := filepath.Join(n.folder, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		n.t.Fatal(err)
	}
	mt := time.Now().Add(-time.Minute).Add(time.Duration(min(len(data), 1000)) * time.Millisecond)
	os.Chtimes(p, mt, mt)
	n.e.Rescan()
}

func (n *node) read(rel string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join(n.folder, filepath.FromSlash(rel)))
	return b, err == nil
}

func (n *node) has(rel string, want []byte) bool {
	b, ok := n.read(rel)
	return ok && bytes.Equal(b, want)
}

func (n *node) gone(rel string) bool {
	_, err := os.Stat(filepath.Join(n.folder, filepath.FromSlash(rel)))
	return os.IsNotExist(err)
}

func (n *node) listing() map[string]string {
	out := map[string]string{}
	filepath.WalkDir(n.folder, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(n.folder, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, metaDir) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func sameListing(a, b *node) bool {
	la, lb := a.listing(), b.listing()
	if len(la) != len(lb) {
		return false
	}
	for k, v := range la {
		if w, ok := lb[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func synced(n *node) bool {
	st := n.e.State()
	return st.Status == "synced"
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestCreateModifyDelete(t *testing.T) {
	a, b := pair(t)

	a.write("parts/bracket.gcode", []byte("G1 X10 Y10\n"))
	waitFor(t, 10*time.Second, "file reaches B", func() bool { return b.has("parts/bracket.gcode", []byte("G1 X10 Y10\n")) })
	sa, _ := os.Stat(filepath.Join(a.folder, "parts", "bracket.gcode"))
	sb, _ := os.Stat(filepath.Join(b.folder, "parts", "bracket.gcode"))
	if !sa.ModTime().Equal(sb.ModTime()) {
		t.Errorf("mtime not preserved: %v vs %v", sa.ModTime(), sb.ModTime())
	}

	b.write("parts/bracket.gcode", []byte("G1 X20 Y20\n"))
	waitFor(t, 10*time.Second, "edit reaches A", func() bool { return a.has("parts/bracket.gcode", []byte("G1 X20 Y20\n")) })
	arch, _ := filepath.Glob(filepath.Join(a.folder, metaDir, "archive", "*", "parts", "bracket~*.gcode"))
	if len(arch) != 1 {
		t.Errorf("expected old version archived on A, got %v", arch)
	}

	os.Remove(filepath.Join(a.folder, "parts", "bracket.gcode"))
	a.e.Rescan()
	waitFor(t, 10*time.Second, "delete reaches B", func() bool { return b.gone("parts/bracket.gcode") })

	os.MkdirAll(filepath.Join(a.folder, "empty", "nested"), 0o755)
	a.e.Rescan()
	waitFor(t, 10*time.Second, "empty dirs reach B", func() bool { return !b.gone("empty/nested") })
	os.RemoveAll(filepath.Join(a.folder, "empty"))
	os.Remove(filepath.Join(a.folder, "parts"))
	a.e.Rescan()
	waitFor(t, 15*time.Second, "dir removal reaches B", func() bool { return b.gone("empty") && b.gone("parts") })
	waitFor(t, 10*time.Second, "both report up to date", func() bool { return synced(a) && synced(b) })
}

func TestManyFilesBothWays(t *testing.T) {
	a, b := pair(t)
	for i := 0; i < 50; i++ {
		a.write(fmt.Sprintf("a/part%02d.gcode", i), randBytes(1000+i))
		b.write(fmt.Sprintf("b/part%02d.gcode", i), randBytes(2000+i))
	}
	a.e.Rescan()
	b.e.Rescan()
	waitFor(t, 30*time.Second, "folders identical", func() bool { return len(a.listing()) == 102 && sameListing(a, b) })
	waitFor(t, 10*time.Second, "both report up to date", func() bool { return synced(a) && synced(b) })
}

func TestConflictKeepsBoth(t *testing.T) {
	a, b := pair(t)
	a.write("job.gcode", []byte("original"))
	waitFor(t, 10*time.Second, "file reaches B", func() bool { return b.has("job.gcode", []byte("original")) })

	// Take B offline, edit on both sides, bring it back.
	b.e.SetPaused(true)
	a.write("job.gcode", []byte("edited on A, the longer edit"))
	b.write("job.gcode", []byte("edited on B"))
	time.Sleep(time.Second)
	b.e.SetPaused(false)

	waitFor(t, 20*time.Second, "folders converge", func() bool { return sameListing(a, b) && len(a.listing()) == 2 })
	l := a.listing()
	var winner, loser string
	for k, v := range l {
		if k == "job.gcode" {
			winner = v
		} else if strings.Contains(k, ".sync-conflict-") {
			loser = v
		}
	}
	// A's edit has the later mtime (see write), so it keeps the name.
	if winner != "edited on A, the longer edit" || loser != "edited on B" {
		t.Fatalf("unexpected conflict result: %v", l)
	}
}

func TestExistingIdenticalFilesMerge(t *testing.T) {
	key := NewShareKey()
	pa, pb := freePort(t), freePort(t)
	// Prepare both folders with the same content before the devices meet.
	da, db := t.TempDir(), t.TempDir()
	for _, d := range []string{da, db} {
		os.WriteFile(filepath.Join(d, "same.gcode"), []byte("identical"), 0o644)
		mt := time.Now().Add(-time.Hour)
		os.Chtimes(filepath.Join(d, "same.gcode"), mt, mt)
	}
	os.WriteFile(filepath.Join(da, "only-a.gcode"), []byte("a"), 0o644)
	mk := func(name string, port, peer int, folder string) *node {
		e, _ := New(Options{DataDir: filepath.Join(t.TempDir(), "data"), ListenHost: "127.0.0.1", ListenPort: port, DiscoveryPort: -1, ScanInterval: 2 * time.Second, Log: log.New(&testLog{t, name}, "", 0)})
		addrs := []string{"127.0.0.1:" + strconv.Itoa(peer)}
		e.UpdateSettings(Settings{ExtraPeers: &addrs})
		if err := e.Setup(key, folder, name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		return &node{t: t, e: e, folder: folder, port: port}
	}
	a := mk("Alpha", pa, pb, da)
	b := mk("Bravo", pb, pa, db)
	waitFor(t, 20*time.Second, "converge", func() bool { return sameListing(a, b) && len(b.listing()) == 2 })
	waitFor(t, 10*time.Second, "up to date", func() bool { return synced(a) && synced(b) })
	if len(a.listing()) != 2 {
		t.Fatalf("unexpected files (conflict copies?): %v", a.listing())
	}
}

func TestResumeAfterDisconnect(t *testing.T) {
	a, b := pair(t)
	big := randBytes(96 << 20)
	a.write("big.gcode", big)
	// Wait until the transfer is partly done, then cut the connection.
	waitFor(t, 20*time.Second, "transfer starts", func() bool {
		for _, tr := range b.e.State().Transfers {
			if tr.Done > 8<<20 {
				return true
			}
		}
		return false
	})
	b.e.SetPaused(true)
	time.Sleep(500 * time.Millisecond)
	tmps, _ := filepath.Glob(filepath.Join(b.folder, metaDir, "tmp", "*.fctmp"))
	if len(tmps) != 1 {
		t.Fatalf("expected a partial temp file, got %v", tmps)
	}
	fi, _ := os.Stat(tmps[0])
	partial := fi.Size()
	b.e.SetPaused(false)
	resumedFrom := int64(-1)
	waitFor(t, 60*time.Second, "big file arrives", func() bool {
		for _, tr := range b.e.State().Transfers {
			if resumedFrom < 0 {
				resumedFrom = tr.Done
			}
		}
		return b.has("big.gcode", big)
	})
	if resumedFrom >= 0 && resumedFrom < partial {
		t.Errorf("transfer restarted from %d instead of resuming at %d", resumedFrom, partial)
	}
	t.Logf("partial=%d resumedFrom=%d", partial, resumedFrom)
}

func TestRestartKeepsState(t *testing.T) {
	a, b := pair(t)
	a.write("x.gcode", []byte("one"))
	waitFor(t, 10*time.Second, "file reaches B", func() bool { return b.has("x.gcode", []byte("one")) })
	// Simulate B crashing and coming back while A changes things.
	b.e.Stop()
	a.write("x.gcode", []byte("two"))
	a.write("y.gcode", []byte("new"))
	if err := b.e.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "B catches up after restart", func() bool { return sameListing(a, b) && b.has("x.gcode", []byte("two")) })
	if l := b.listing(); len(l) != 2 {
		t.Fatalf("unexpected files: %v", l)
	}
}

func TestWrongKeyRejected(t *testing.T) {
	pa, pc := freePort(t), freePort(t)
	a := newNode(t, "Alpha", NewShareKey(), pa, pc)
	c := newNode(t, "Mallory", NewShareKey(), pc, pa)
	c.write("evil.gcode", []byte("nope"))
	time.Sleep(4 * time.Second)
	if online(a) || online(c) {
		t.Fatal("devices with different keys connected")
	}
	if !a.gone("evil.gcode") {
		t.Fatal("file crossed between shares")
	}
	found := false
	for _, ev := range a.e.State().Events {
		if strings.Contains(ev.Text, "different share key") {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning about the different share key")
	}
}

func TestShareKeyFormat(t *testing.T) {
	k := NewShareKey()
	if len(k) != 39 || strings.Count(k, "-") != 7 {
		t.Fatalf("bad key format %q", k)
	}
	n, err := NormalizeShareKey(strings.ToLower(strings.ReplaceAll(k, "-", " ")))
	if err != nil || n != k {
		t.Fatalf("normalize: %q %v", n, err)
	}
	if _, err := ParseShareKey("ABCD-EFGH"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestValidPath(t *testing.T) {
	for _, p := range []string{"../x", "a/../../x", "/abs", "C:/x", "a/con.txt", "a:b", ".folderchain/tmp/x", "a/b."} {
		if validPath(p) {
			t.Errorf("%q should be rejected", p)
		}
	}
	for _, p := range []string{"part.gcode", "Machine 1/Part (2).gcode", "a/b/c.nc"} {
		if !validPath(p) {
			t.Errorf("%q should be allowed", p)
		}
	}
}

// blackholeProxy forwards TCP to target until frozen, after which it keeps
// connections open but silently drops all traffic — what a laptop leaving
// Wi-Fi looks like to the other side.
type blackholeProxy struct {
	ln     net.Listener
	frozen chan struct{}
}

func newBlackholeProxy(t *testing.T, target string) *blackholeProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &blackholeProxy{ln: ln, frozen: make(chan struct{})}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case <-p.frozen:
				c.Close()
				continue
			default:
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if err != nil {
						return
					}
					select {
					case <-p.frozen:
						continue // swallow
					default:
					}
					if _, err := dst.Write(buf[:n]); err != nil {
						return
					}
				}
			}
			go pipe(u, c)
			go pipe(c, u)
		}
	}()
	return p
}

func TestSilentNetworkDrop(t *testing.T) {
	key := NewShareKey()
	pa, pb := freePort(t), freePort(t)
	proxy := newBlackholeProxy(t, "127.0.0.1:"+strconv.Itoa(pb))
	// A only knows B through the proxy; B learns A's real address from the
	// handshake, like a laptop that comes back with a new route.
	a := newNode(t, "Alpha", key, pa, proxy.ln.Addr().(*net.TCPAddr).Port)
	b := newNode(t, "Bravo", key, pb)
	waitFor(t, 15*time.Second, "connect through proxy", func() bool { return online(a) && online(b) })
	a.write("before.gcode", []byte("1"))
	waitFor(t, 10*time.Second, "sync before drop", func() bool { return b.has("before.gcode", []byte("1")) })

	close(proxy.frozen)
	dropped := time.Now()
	a.write("after.gcode", []byte("2"))
	waitFor(t, 45*time.Second, "sync after silent drop", func() bool { return b.has("after.gcode", []byte("2")) })
	t.Logf("recovered %v after the link went dead", time.Since(dropped).Round(time.Second))
}
