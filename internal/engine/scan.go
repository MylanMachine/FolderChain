package engine

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type diskEntry struct {
	dir   bool
	size  int64
	mtime int64
}

func (s *session) scanLoop() {
	t := time.NewTicker(s.e.opts.ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.scanReq:
			// let bursts of file activity settle into one scan
			if !s.sleep(700 * time.Millisecond) {
				return
			}
			select {
			case <-s.scanReq:
			default:
			}
		case <-t.C:
		}
		if again := s.scan(); again > 0 {
			go func() {
				if s.sleep(again) {
					s.kick(s.scanReq)
				}
			}()
		}
	}
}

// scan compares the folder with the index and records local changes. It
// returns a delay after which it should run again (for files that were
// still being written), or 0.
func (s *session) scan() time.Duration {
	e := s.e
	root := s.root
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		s.setFolderErr("Can't find the folder " + root + " — is the drive connected?")
		return 10 * time.Second
	}

	e.mu.Lock()
	me := e.cfg.DeviceID
	if _, err := os.Stat(filepath.Join(root, metaDir, "folder-id")); err != nil {
		// The marker is missing: the folder was replaced or restored. Forget
		// what we knew rather than treating every missing file as deleted.
		live := 0
		for _, f := range s.idx.Files {
			if !f.Deleted {
				live++
			}
		}
		if live > 0 {
			e.eventLocked("warn", "Folder was replaced or restored — re-checking every file")
			seq := s.idx.Seq
			s.idx = newIndex(root)
			s.idx.Seq = seq
			s.idxDirty = true
		}
		if err := ensureMeta(root, s.keys.shareID); err != nil {
			e.mu.Unlock()
			s.setFolderErr("Can't write to the folder: " + err.Error())
			return 10 * time.Second
		}
	}
	startSeq := s.idx.Seq
	e.mu.Unlock()

	disk := map[string]diskEntry{}
	var badDirs []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			if err != nil && rel == "." {
				badDirs = append(badDirs, "")
			}
			return nil
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			badDirs = append(badDirs, rel)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ignoredName(rel) || !validPath(rel) || d.Type()&(fs.ModeSymlink|fs.ModeIrregular|fs.ModeDevice) != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			disk[rel] = diskEntry{dir: true}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		disk[rel] = diskEntry{size: info.Size(), mtime: info.ModTime().UnixNano()}
		return nil
	})
	for _, b := range badDirs {
		if b == "" {
			s.setFolderErr("Can't read the folder " + root)
			return 10 * time.Second
		}
	}
	underBad := func(p string) bool {
		for _, b := range badDirs {
			if p == b || strings.HasPrefix(p, b+"/") {
				return true
			}
		}
		return false
	}

	var again time.Duration
	type cand struct {
		path string
		de   diskEntry
		prev *FileInfo
	}
	var toHash []cand
	now := time.Now()
	indexed := 0

	e.mu.Lock()
	for p, de := range disk {
		l := s.idx.Files[p]
		if l != nil && l.Seq > startSeq {
			continue // changed by a sync while we were walking
		}
		if de.dir {
			if l == nil || l.Deleted || !l.Dir {
				n := &FileInfo{Path: p, Dir: true, ModifiedBy: me}
				n.Version = prevVersion(l).Bump(me)
				s.putLocked(n)
			}
			continue
		}
		if l != nil && !l.Deleted && !l.Dir && l.Size == de.size && l.ModTime == de.mtime {
			continue
		}
		if age := now.Sub(time.Unix(0, de.mtime)); age < 2*time.Second && age > -2*time.Second {
			again = 3 * time.Second // still being written
			continue
		}
		var prev *FileInfo
		if l != nil {
			prev = cloneInfo(l)
		}
		toHash = append(toHash, cand{p, de, prev})
	}
	for p, l := range s.idx.Files {
		if l.Deleted || l.Seq > startSeq || underBad(p) {
			continue
		}
		if _, ok := disk[p]; ok {
			continue
		}
		n := &FileInfo{Path: p, Dir: l.Dir, Deleted: true, ModTime: now.UnixNano(), ModifiedBy: me}
		n.Version = l.Version.Bump(me)
		s.putLocked(n)
		if !l.Dir {
			e.eventLocked("del", "Deleted %s", p)
		}
	}
	s.folderErr = ""
	e.mu.Unlock()

	for _, c := range toHash {
		if s.ctx.Err() != nil {
			return 0
		}
		abs := absPath(root, c.path)
		h, n, err := hashFile(abs)
		if err != nil {
			again = 10 * time.Second // locked by another program; try later
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || n != c.de.size || info.Size() != c.de.size || info.ModTime().UnixNano() != c.de.mtime {
			again = 3 * time.Second
			continue
		}
		e.mu.Lock()
		l := s.idx.Files[c.path]
		if !sameEntry(l, c.prev) {
			e.mu.Unlock()
			continue
		}
		if l != nil && !l.Deleted && !l.Dir && bytes.Equal(l.Hash, h) && l.Size == n {
			// only the timestamp changed; no new version needed
			l.ModTime = c.de.mtime
			s.idxDirty = true
			e.mu.Unlock()
			continue
		}
		nf := &FileInfo{Path: c.path, Size: n, ModTime: c.de.mtime, Hash: h, ModifiedBy: me}
		nf.Version = prevVersion(l).Bump(me)
		s.putLocked(nf)
		if !s.scannedOnce {
			indexed++
		} else if l == nil || l.Deleted {
			e.eventLocked("info", "Added %s", c.path)
		} else {
			e.eventLocked("info", "Updated %s", c.path)
		}
		e.mu.Unlock()
	}

	e.mu.Lock()
	if !s.scannedOnce {
		s.scannedOnce = true
		s.lastSync = time.Now()
		if indexed > 0 {
			e.eventLocked("info", "Indexed %d file%s", indexed, plural(indexed))
		}
	}
	s.kick(s.pullReq)
	e.mu.Unlock()
	return again
}

func prevVersion(l *FileInfo) Vector {
	if l == nil {
		return Vector{}
	}
	return l.Version
}

func sameEntry(a, b *FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Seq == b.Seq
}
