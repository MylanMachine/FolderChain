package engine

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const metaDir = ".folderchain"

// ignoredName reports files that should never be synced: our own metadata,
// OS clutter, and temp files that editors and slicers write before renaming.
func ignoredName(rel string) bool {
	if rel == metaDir || strings.HasPrefix(rel, metaDir+"/") {
		return true
	}
	name := strings.ToLower(path.Base(rel))
	switch name {
	case "desktop.ini", "thumbs.db", ".ds_store", "$recycle.bin", "system volume information":
		return true
	}
	if strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock.") {
		return true
	}
	for _, ext := range []string{".tmp", ".part", ".crdownload", ".partial", ".swp"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

// validPath rejects anything a remote device could use to escape the shared
// folder or that Windows can't store (.., drive letters, streams, reserved names).
func validPath(p string) bool {
	if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") || ignoredName(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, " ") || strings.HasSuffix(seg, ".") {
			return false
		}
		if strings.ContainsAny(seg, `<>:"\|?*`) {
			return false
		}
		for _, r := range seg {
			if r < 32 {
				return false
			}
		}
		base := strings.ToUpper(seg)
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if reservedNames[base] {
			return false
		}
	}
	return true
}

func hashFile(name string) ([]byte, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, 0, err
	}
	return h.Sum(nil), n, nil
}

func writeFileAtomic(name string, data []byte) error {
	tmp := name + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

func stamp(t time.Time) string { return t.Format("20060102-150405") }

// withSuffix turns "dir/part.gcode" into "dir/part<suffix>.gcode".
func withSuffix(rel, suffix string) string {
	ext := path.Ext(rel)
	if ext == path.Base(rel) {
		ext = ""
	}
	return strings.TrimSuffix(rel, ext) + suffix + ext
}

func conflictName(rel, device string) string {
	return withSuffix(rel, fmt.Sprintf(".sync-conflict-%s-%s", stamp(time.Now()), sanitizeName(device)))
}

func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*. `, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if len(s) > 32 {
		s = s[:32]
	}
	if s == "" {
		s = "device"
	}
	return s
}

// archiveFile moves a file that is about to be overwritten or deleted into
// .folderchain/archive/<date>/ so nothing is ever lost to a sync.
func archiveFile(root, rel string) error {
	now := time.Now()
	day := filepath.Join(root, metaDir, "archive", now.Format("2006-01-02"))
	dst := filepath.Join(day, filepath.FromSlash(withSuffix(rel, "~"+now.Format("150405"))))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		dst = filepath.Join(day, filepath.FromSlash(withSuffix(rel, fmt.Sprintf("~%s-%d", now.Format("150405"), now.UnixNano()%1e6))))
	}
	return os.Rename(absPath(root, rel), dst)
}

func absPath(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// pruneArchive removes archive days older than maxAge.
func pruneArchive(root string, maxAge time.Duration) {
	dir := filepath.Join(root, metaDir, "archive")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		t, err := time.ParseInLocation("2006-01-02", e.Name(), time.Local)
		if err == nil && time.Since(t) > maxAge {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// pruneDir deletes files older than maxAge below dir and removes empty folders.
func pruneDir(dir string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir {
				dirs = append(dirs, p)
			}
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // only succeeds when empty
	}
}

func isNotEmpty(err error) bool {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		// ERROR_DIR_NOT_EMPTY (145)
		return strings.Contains(strings.ToLower(pe.Err.Error()), "not empty")
	}
	return false
}
