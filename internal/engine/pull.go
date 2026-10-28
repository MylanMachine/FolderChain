package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type jobKind int

const (
	jobDelete jobKind = iota
	jobMkdir
	jobFile
	jobRmdir
)

type job struct {
	kind     jobKind
	path     string
	local    *FileInfo // snapshot of our entry when planned (nil if none)
	remote   *FileInfo // the version we're moving to
	from     string    // device that has it
	conflict bool      // keep our current file as a conflict copy
	merged   Vector    // version to record once applied
}

const maxParallel = 4

var (
	errStale   = errors.New("changed while syncing; retrying")
	errChanged = errors.New("file was changed on this computer; re-checking")
)

// plan compares our index with every connected device's and returns the
// work needed to catch up. With apply set it also records the cheap
// metadata-only outcomes (identical content, we win a conflict) directly.
func (s *session) plan(apply bool) []job {
	best := map[string]*FileInfo{}
	from := map[string]string{}
	for id, m := range s.remote {
		for p, r := range m {
			cur := best[p]
			if cur == nil {
				best[p], from[p] = r, id
				continue
			}
			c := r.Version.Compare(cur.Version)
			if c == Greater || (c == Concurrent && winsOver(r, cur)) {
				best[p], from[p] = r, id
			}
		}
	}
	var jobs []job
	for p, r := range best {
		l := s.idx.Files[p]
		kindFor := func() jobKind {
			switch {
			case r.Deleted && l != nil && l.Dir:
				return jobRmdir
			case r.Deleted:
				return jobDelete
			case r.Dir:
				return jobMkdir
			}
			return jobFile
		}
		if l == nil {
			if r.Deleted {
				if apply {
					s.putLocked(cloneInfo(r))
				}
				continue
			}
			jobs = append(jobs, job{kind: kindFor(), path: p, remote: r, from: from[p], merged: r.Version})
			continue
		}
		switch r.Version.Compare(l.Version) {
		case Equal, Lesser:
			continue
		case Greater:
			if l.SameContent(r) {
				if apply {
					s.adoptLocked(l, r, r.Version)
				}
				continue
			}
			if !l.Deleted && !r.Deleted && l.Dir != r.Dir && l.Dir {
				continue // a folder became a file elsewhere; leave ours alone
			}
			jobs = append(jobs, job{kind: kindFor(), path: p, local: cloneInfo(l), remote: r, from: from[p], merged: r.Version})
		case Concurrent:
			merged := l.Version.Merge(r.Version)
			if l.SameContent(r) {
				if apply {
					s.adoptLocked(l, r, merged)
				}
				continue
			}
			if !l.Deleted && !r.Deleted && l.Dir != r.Dir {
				continue
			}
			if winsOver(r, l) {
				jobs = append(jobs, job{kind: kindFor(), path: p, local: cloneInfo(l), remote: r, from: from[p],
					conflict: !l.Deleted && !l.Dir && !r.Dir, merged: merged})
			}
			// Otherwise our copy wins. We deliberately don't absorb their
			// version: the other device must see ours as concurrent so it
			// saves its edit as a conflict copy instead of overwriting it.
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		da, db := strings.Count(a.path, "/"), strings.Count(b.path, "/")
		switch a.kind {
		case jobMkdir:
			if da != db {
				return da < db
			}
		case jobRmdir:
			if da != db {
				return da > db
			}
		case jobFile:
			if a.remote.Size != b.remote.Size {
				return a.remote.Size < b.remote.Size
			}
		}
		return a.path < b.path
	})
	return jobs
}

func (s *session) needsLocked() []job { return s.plan(false) }

// adoptLocked records a remote version whose content we already have.
func (s *session) adoptLocked(l, r *FileInfo, v Vector) {
	n := cloneInfo(l)
	n.Version = v
	n.ModifiedBy = r.ModifiedBy
	if !n.Dir && !n.Deleted && r.ModTime != l.ModTime {
		abs := absPath(s.root, l.Path)
		if fi, err := os.Stat(abs); err == nil && fi.Size() == l.Size && fi.ModTime().UnixNano() == l.ModTime {
			mt := time.Unix(0, r.ModTime)
			if os.Chtimes(abs, mt, mt) == nil {
				if fi, err := os.Stat(abs); err == nil {
					n.ModTime = fi.ModTime().UnixNano()
				}
			}
		}
	}
	s.putLocked(n)
}

// theyNeedLocked counts entries the device is missing from us.
func (s *session) theyNeedLocked(id string) int {
	m := s.remote[id]
	n := 0
	for p, l := range s.idx.Files {
		r := m[p]
		if r == nil {
			if !l.Deleted {
				n++
			}
			continue
		}
		if l.SameContent(r) {
			continue
		}
		c := l.Version.Compare(r.Version)
		if c == Greater || (c == Concurrent && winsOver(l, r)) {
			n++
		}
	}
	return n
}

func (s *session) pullLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.pullReq:
		case <-t.C:
		}
		s.schedule()
	}
}

func (s *session) schedule() {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if !s.scannedOnce || s.folderErr != "" || s.ctx.Err() != nil {
		return
	}
	jobs := s.plan(true)
	now := time.Now()
	others := false
	for _, j := range jobs {
		if s.running >= maxParallel {
			return
		}
		if j.kind == jobRmdir && others {
			return // remove folders only once their contents are gone
		}
		others = true
		if s.inflight[j.path] {
			continue
		}
		if r := s.retry[j.path]; r != nil && now.Before(r.next) {
			continue
		}
		s.inflight[j.path] = true
		s.running++
		go s.runJob(j)
	}
}

func (s *session) runJob(j job) {
	var err error
	switch j.kind {
	case jobFile:
		err = s.pullFile(j)
	default:
		s.e.mu.Lock()
		err = s.applyLocked(j, "")
		s.e.mu.Unlock()
	}
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(s.inflight, j.path)
	s.running--
	if err == nil {
		delete(s.retry, j.path)
		s.lastSync = time.Now()
	} else {
		r := s.retry[j.path]
		if r == nil {
			r = &retryState{}
			s.retry[j.path] = r
		}
		wait := 2 * time.Second
		switch {
		case errors.Is(err, errStale):
			r.msg = ""
		case errors.Is(err, errChanged):
			r.msg = ""
			s.kick(s.scanReq)
		default:
			r.n++
			wait = min(time.Duration(1<<min(r.n, 6))*time.Second*2, 2*time.Minute)
			r.msg = err.Error()
			e.log.Printf("sync %s: %v (retry in %v)", j.path, err, wait)
		}
		r.next = time.Now().Add(wait)
	}
	s.kick(s.pullReq)
}

func (s *session) tmpPath(j job) string {
	ph := sha256.Sum256([]byte(j.path))
	return filepath.Join(s.root, metaDir, "tmp", hex.EncodeToString(ph[:6])+"-"+hex.EncodeToString(j.remote.Hash)[:16]+".fctmp")
}

func (s *session) pullFile(j job) error {
	e := s.e
	r := j.remote
	tmp := s.tmpPath(j)
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return err
	}
	var offset int64
	if fi, err := os.Stat(tmp); err == nil {
		if fi.Size() <= r.Size {
			offset = fi.Size() // resume an interrupted transfer
		} else {
			os.Remove(tmp)
		}
	}
	var h hash.Hash
	if offset == 0 {
		h = sha256.New()
	}
	if offset < r.Size || (r.Size == 0 && offset == 0) {
		if err := s.download(j, tmp, offset, h); err != nil {
			return err
		}
	}
	var sum []byte
	if h != nil {
		sum = h.Sum(nil)
	} else {
		var err error
		if sum, _, err = hashFile(tmp); err != nil {
			return err
		}
	}
	if !bytes.Equal(sum, r.Hash) {
		os.Remove(tmp)
		return errors.New("received data didn't match; downloading again")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return s.applyLocked(j, tmp)
}

// download fetches the file into tmp starting at offset.
func (s *session) download(j job, tmp string, offset int64, h hash.Hash) error {
	e := s.e
	r := j.remote
	e.mu.Lock()
	pc := s.peers[j.from]
	if m := s.remote[j.from]; pc == nil || m == nil || m[j.path] == nil || !bytes.Equal(m[j.path].Hash, r.Hash) {
		pc = nil
		for id, m := range s.remote {
			if f := m[j.path]; f != nil && !f.Deleted && bytes.Equal(f.Hash, r.Hash) && s.peers[id] != nil {
				pc = s.peers[id]
				break
			}
		}
	}
	key := "down:" + j.path
	if pc != nil {
		s.transfers[key] = &Transfer{Path: j.path, Size: r.Size, Done: offset, Peer: pc.name, Started: time.Now()}
	}
	e.mu.Unlock()
	if pc == nil {
		return errors.New("waiting for a device that has this file")
	}
	defer func() {
		e.mu.Lock()
		delete(s.transfers, key)
		e.mu.Unlock()
	}()

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset == 0 {
		f.Truncate(0)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	id, ch, err := pc.request(j.path, r.Hash, offset)
	if err != nil {
		return fmt.Errorf("lost connection to %s", pc.name)
	}
	defer pc.endRequest(id)
	timer := time.NewTimer(deadAfter)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return errStale
		case <-pc.done:
			return fmt.Errorf("lost connection to %s; will resume", pc.name)
		case <-timer.C:
			return fmt.Errorf("%s stopped responding; will resume", pc.name)
		case d := <-ch:
			if d.Err != "" {
				return fmt.Errorf("%s: %s", pc.name, d.Err)
			}
			if d.Offset != offset {
				return errors.New("transfer out of order; retrying")
			}
			if len(d.Bytes) > 0 {
				if _, err := f.Write(d.Bytes); err != nil {
					return fmt.Errorf("can't write: %v", err)
				}
				if h != nil {
					h.Write(d.Bytes)
				}
				offset += int64(len(d.Bytes))
				e.mu.Lock()
				if t := s.transfers[key]; t != nil {
					t.Done = offset
				}
				e.mu.Unlock()
			}
			if d.EOF {
				if offset != r.Size {
					return errors.New("file changed on the other device; retrying")
				}
				return f.Close()
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(deadAfter)
		}
	}
}

// caseTwinLocked finds another live entry whose path differs only by case
// (the same file on Windows).
func (s *session) caseTwinLocked(p string) *FileInfo {
	for q, f := range s.idx.Files {
		if q != p && !f.Deleted && strings.EqualFold(q, p) {
			return f
		}
	}
	return nil
}

// applyLocked makes the on-disk change for a job and records it. It first
// checks that the file on disk is exactly what our index says, so a local
// edit that hasn't been scanned yet is never overwritten.
func (s *session) applyLocked(j job, tmp string) error {
	e := s.e
	cur := s.idx.Files[j.path]
	if !sameEntry(cur, j.local) {
		return errStale
	}
	abs := absPath(s.root, j.path)
	fi, statErr := os.Lstat(abs)
	exists := statErr == nil
	matches := func(f *FileInfo) bool {
		if f.Dir {
			return fi.IsDir()
		}
		return !fi.IsDir() && fi.Size() == f.Size && fi.ModTime().UnixNano() == f.ModTime
	}
	if exists {
		switch {
		case cur != nil && !cur.Deleted:
			if !matches(cur) {
				return errChanged
			}
		case j.kind == jobMkdir && fi.IsDir():
		case j.kind == jobDelete || j.kind == jobRmdir:
		default:
			if t := s.caseTwinLocked(j.path); t == nil || !matches(t) {
				return errChanged
			}
		}
	}

	r := j.remote
	n := cloneInfo(r)
	n.Version = j.merged
	switch j.kind {
	case jobDelete:
		if exists && s.caseTwinLocked(j.path) == nil {
			if err := archiveFile(s.root, j.path); err != nil {
				return inUse(err)
			}
			e.eventLocked("del", "Removed %s (deleted on %s)", j.path, s.nameLocked(r.ModifiedBy))
		}
	case jobRmdir:
		if exists {
			if err := os.Remove(abs); err != nil {
				clearJunk(abs)
				if err := os.Remove(abs); err != nil {
					if isNotEmpty(err) {
						return errors.New("folder still has files in it")
					}
					return inUse(err)
				}
			}
		}
	case jobMkdir:
		if exists && !fi.IsDir() {
			if err := archiveFile(s.root, j.path); err != nil {
				return inUse(err)
			}
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return err
		}
	case jobFile:
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if exists && fi.IsDir() {
			return errors.New("a folder with this name exists here")
		}
		if exists {
			if j.conflict {
				cname := conflictName(j.path, e.cfg.DeviceName)
				if err := os.Rename(abs, absPath(s.root, cname)); err != nil {
					return inUse(err)
				}
				e.eventLocked("conflict", "%s was changed on two computers — kept both (yours is now %s)", j.path, path.Base(cname))
			} else if err := archiveFile(s.root, j.path); err != nil {
				return inUse(err)
			}
		}
		mt := time.Unix(0, r.ModTime)
		os.Chtimes(tmp, mt, mt)
		if err := os.Rename(tmp, abs); err != nil {
			return inUse(err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return err
		}
		n.Size, n.ModTime = st.Size(), st.ModTime().UnixNano()
		e.eventLocked("in", "Received %s from %s", j.path, s.nameLocked(r.ModifiedBy))
	}
	s.putLocked(n)
	return nil
}

func inUse(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "used by another process") {
		return errors.New("file is open in another program; will retry")
	}
	return err
}

func (s *session) nameLocked(id string) string {
	if pc := s.peers[id]; pc != nil {
		return pc.name
	}
	if k := s.e.cfg.Known[id]; k != nil && k.Name != "" {
		return k.Name
	}
	if id == s.e.cfg.DeviceID {
		return "this computer"
	}
	return "another device"
}

// clearJunk removes OS clutter (Thumbs.db etc.) that would keep an otherwise
// empty folder from being deleted.
func clearJunk(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, en := range ents {
		if !en.IsDir() && ignoredName(en.Name()) {
			os.Remove(filepath.Join(dir, en.Name()))
		}
	}
}
