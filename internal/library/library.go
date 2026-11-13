// Package library keeps track of the PKG files in the library folder.
//
// It rescans the folder on a short interval rather than relying on
// filesystem notifications: the folder holds tens of files at most, a scan
// is cheap, and polling behaves the same on every OS, network drive and
// download manager. A file is "ready" to serve once it has a non-temporary
// name, is non-empty, and its size and modification time have not changed
// for the settle window.
package library

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// State describes a file in the library folder.
type State string

const (
	// StateWriting: a download manager is still writing the file.
	StateWriting State = "writing"
	// StateReady: the file is complete and can be served to the console.
	StateReady State = "ready"
)

// File is one file in the library folder.
type File struct {
	// Name is the PKG name the file is (or will be once complete): the
	// basename with any temporary suffix such as ".part" removed.
	Name    string    `json:"name"`
	Rel     string    `json:"rel"` // path relative to the library root
	Path    string    `json:"-"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"-"`
	State   State     `json:"state"`
}

// Library scans one folder. The zero value is not usable; call New.
type Library struct {
	root     string
	suffixes []string
	settle   time.Duration
	now      func() time.Time

	mu    sync.RWMutex
	files map[string]*entry // absolute path -> entry
}

type entry struct {
	size      int64
	mod       time.Time
	changedAt time.Time
}

// New returns a Library for root. It creates the folder if needed so users
// can find it, and performs an initial scan.
func New(root string, ignoreSuffixes []string, settle time.Duration) (*Library, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	l := &Library{
		root:     root,
		suffixes: ignoreSuffixes,
		settle:   settle,
		now:      time.Now,
		files:    make(map[string]*entry),
	}
	l.Scan()
	return l, nil
}

// Root returns the absolute library folder.
func (l *Library) Root() string { return l.root }

// Run rescans every interval until ctx is cancelled.
func (l *Library) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Scan()
		}
	}
}

// Scan walks the folder once and updates what it knows.
func (l *Library) Scan() {
	now := l.now()
	seen := make(map[string]fs.FileInfo)
	_ = filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep scanning
		}
		if d.IsDir() {
			// Hidden folders hold other tools' state (for example an old
			// .psxdh-partial cache); never serve from them.
			if p != l.root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			seen[p] = info
		}
		return nil
	})

	l.mu.Lock()
	defer l.mu.Unlock()
	for p := range l.files {
		if _, ok := seen[p]; !ok {
			delete(l.files, p)
		}
	}
	for p, info := range seen {
		e, ok := l.files[p]
		switch {
		case !ok:
			// First sighting: a file last modified long ago is already
			// settled, so it is ready straight away after a restart.
			changed := info.ModTime()
			if changed.After(now) {
				changed = now
			}
			l.files[p] = &entry{size: info.Size(), mod: info.ModTime(), changedAt: changed}
		case e.size != info.Size() || !e.mod.Equal(info.ModTime()):
			e.size, e.mod, e.changedAt = info.Size(), info.ModTime(), now
		}
	}
}

// Files returns every file in the folder, sorted by relative path.
func (l *Library) Files() []File {
	now := l.now()
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]File, 0, len(l.files))
	for p, e := range l.files {
		out = append(out, l.describe(p, e, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// Lookup returns the ready file whose name is name. When several ready files
// share the name (copies in different subfolders) the largest wins, then the
// shortest path, so the result is deterministic.
func (l *Library) Lookup(name string) (File, bool) {
	var best File
	found := false
	for _, f := range l.Files() {
		if f.State != StateReady || f.Name != name {
			continue
		}
		if !found || f.Size > best.Size || (f.Size == best.Size && len(f.Path) < len(best.Path)) {
			best, found = f, true
		}
	}
	return best, found
}

// Find returns the file (ready or still writing) that is or will become name.
// A ready file is preferred over one still being written.
func (l *Library) Find(name string) (File, bool) {
	if f, ok := l.Lookup(name); ok {
		return f, true
	}
	var best File
	found := false
	for _, f := range l.Files() {
		if f.Name == name && (!found || f.Size > best.Size) {
			best, found = f, true
		}
	}
	return best, found
}

func (l *Library) describe(p string, e *entry, now time.Time) File {
	base := filepath.Base(p)
	name, temp := l.trimTempSuffix(base)
	rel, err := filepath.Rel(l.root, p)
	if err != nil {
		rel = base
	}
	f := File{
		Name:    name,
		Rel:     filepath.ToSlash(rel),
		Path:    p,
		Size:    e.size,
		ModTime: e.mod,
		State:   StateWriting,
	}
	settled := now.Sub(e.changedAt) >= l.settle && now.Sub(e.mod) >= l.settle
	// aria2 writes the final name straight away, with a .aria2 control file
	// beside it until the download completes.
	_, hasControl := l.files[p+".aria2"]
	if !temp && e.size > 0 && settled && !hasControl {
		f.State = StateReady
	}
	return f
}

func (l *Library) trimTempSuffix(base string) (string, bool) {
	lower := strings.ToLower(base)
	for _, s := range l.suffixes {
		if s != "" && strings.HasSuffix(lower, strings.ToLower(s)) {
			return base[:len(base)-len(s)], true
		}
	}
	return base, false
}
