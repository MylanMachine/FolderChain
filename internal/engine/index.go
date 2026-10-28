package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"time"
)

// Vector is a version vector: device ID -> counter. It lets two devices tell
// whether one copy of a file supersedes another or whether they were edited
// independently (a conflict).
type Vector map[string]uint64

type Ordering int

const (
	Equal Ordering = iota
	Greater
	Lesser
	Concurrent
)

func (v Vector) Compare(o Vector) Ordering {
	vg, og := false, false
	for k, a := range v {
		if a > o[k] {
			vg = true
		}
	}
	for k, b := range o {
		if b > v[k] {
			og = true
		}
	}
	switch {
	case vg && og:
		return Concurrent
	case vg:
		return Greater
	case og:
		return Lesser
	}
	return Equal
}

func (v Vector) Copy() Vector {
	r := make(Vector, len(v)+1)
	for k, a := range v {
		r[k] = a
	}
	return r
}

func (v Vector) Merge(o Vector) Vector {
	r := v.Copy()
	for k, b := range o {
		if b > r[k] {
			r[k] = b
		}
	}
	return r
}

// Bump returns a copy with this device's counter advanced. The counter is
// time based so that a device whose local index was lost still produces
// versions newer than the ones it made before.
func (v Vector) Bump(id string) Vector {
	r := v.Copy()
	n := uint64(time.Now().Unix())
	if r[id]+1 > n {
		n = r[id] + 1
	}
	r[id] = n
	return r
}

// FileInfo describes one file or directory in the shared folder.
type FileInfo struct {
	Path       string // slash separated, relative to the folder root
	Dir        bool
	Deleted    bool
	Size       int64
	ModTime    int64 // unix nanoseconds
	Hash       []byte
	Version    Vector
	ModifiedBy string
	Seq        uint64 // local change counter, used to send incremental updates
}

// SameContent reports whether two entries describe identical data on disk.
func (f *FileInfo) SameContent(o *FileInfo) bool {
	if f.Deleted || o.Deleted {
		return f.Deleted == o.Deleted
	}
	if f.Dir != o.Dir {
		return false
	}
	if f.Dir {
		return true
	}
	return f.Size == o.Size && bytes.Equal(f.Hash, o.Hash)
}

// winsOver decides between two concurrent versions. Every device evaluates
// this identically, so they all agree on which copy keeps the real name.
func winsOver(a, b *FileInfo) bool {
	if a.Deleted != b.Deleted {
		return !a.Deleted // keeping data beats deleting it
	}
	if a.ModTime != b.ModTime {
		return a.ModTime > b.ModTime
	}
	return a.ModifiedBy > b.ModifiedBy
}

func cloneInfo(f *FileInfo) *FileInfo {
	c := *f
	c.Version = f.Version.Copy()
	c.Hash = append([]byte(nil), f.Hash...)
	return &c
}

type indexFile struct {
	Folder string
	Seq    uint64
	Files  []*FileInfo
}

type Index struct {
	Folder string
	Seq    uint64
	Files  map[string]*FileInfo
}

func newIndex(folder string) *Index {
	return &Index{Folder: folder, Files: map[string]*FileInfo{}}
}

func loadIndex(path, folder string) *Index {
	idx := newIndex(folder)
	b, err := os.ReadFile(path)
	if err != nil {
		return idx
	}
	var f indexFile
	if json.Unmarshal(b, &f) != nil || f.Folder != folder {
		return idx
	}
	idx.Seq = f.Seq
	for _, fi := range f.Files {
		if fi.Version == nil {
			fi.Version = Vector{}
		}
		idx.Files[fi.Path] = fi
	}
	return idx
}

func (idx *Index) save(path string) error {
	f := indexFile{Folder: idx.Folder, Seq: idx.Seq}
	for _, fi := range idx.Files {
		f.Files = append(f.Files, fi)
	}
	sort.Slice(f.Files, func(i, j int) bool { return f.Files[i].Path < f.Files[j].Path })
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// put stores an entry and stamps it with a new local sequence number so it is
// sent to connected devices.
func (idx *Index) put(fi *FileInfo) {
	idx.Seq++
	fi.Seq = idx.Seq
	idx.Files[fi.Path] = fi
}
