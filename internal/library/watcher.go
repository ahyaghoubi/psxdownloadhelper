package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatcherConfig configures the partial-write debounce policy. See
// docs/architecture.md (Library lifecycle → Watcher).
type WatcherConfig struct {
	// Settle is how long a file's size must remain unchanged before we emit
	// KindStable. Default 2s, matches the v1.0 DoD "detect new files within
	// 2 s" in docs/roadmap.md.
	Settle time.Duration
	// PollInterval is how often we re-stat in-flight files. Must be < Settle.
	// Default 500ms.
	PollInterval time.Duration
	// IgnoreSuffixes are filename suffixes that mark in-progress writes from
	// download managers (e.g. ".part" from FDM, ".crdownload" from Chrome).
	// Files matching any of these never enter the state machine; the final
	// rename into the real name then triggers a fresh Create event.
	IgnoreSuffixes []string
	// Logger receives slog records. Optional.
	Logger *slog.Logger
}

func (c *WatcherConfig) defaults() {
	if c.Settle <= 0 {
		c.Settle = 2 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
}

// Watcher observes a library directory and emits Events. It is the
// single source of truth for the lifecycle of a file in the library.
type Watcher struct {
	cfg     WatcherConfig
	index   *Index
	fsw     *fsnotify.Watcher
	events  chan Event
	mu      sync.Mutex
	pending map[string]*pendingFile
}

type pendingFile struct {
	size     int64
	settleAt time.Time
}

// NewWatcher creates a watcher rooted at index.Root(). The directory is
// created if it does not exist (it must, for fsnotify to add it).
func NewWatcher(index *Index, cfg WatcherConfig) (*Watcher, error) {
	cfg.defaults()
	if index == nil {
		return nil, errors.New("library: nil index")
	}
	if err := os.MkdirAll(index.Root(), 0o755); err != nil {
		return nil, fmt.Errorf("create library root %q: %w", index.Root(), err)
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify.NewWatcher: %w", err)
	}
	if err := fsw.Add(index.Root()); err != nil {
		fsw.Close()
		return nil, fmt.Errorf("watch %q: %w", index.Root(), err)
	}
	return &Watcher{
		cfg:     cfg,
		index:   index,
		fsw:     fsw,
		events:  make(chan Event, 64),
		pending: make(map[string]*pendingFile),
	}, nil
}

// Events returns the channel of library events. Consumers must drain it
// promptly or the watcher will block on emit.
func (w *Watcher) Events() <-chan Event { return w.events }

// Run blocks until ctx is canceled, dispatching fsnotify events and polling
// in-flight files for the settle condition. The events channel is closed on
// return so consumers ranging over it see a clean shutdown.
func (w *Watcher) Run(ctx context.Context) error {
	defer close(w.events)
	defer w.fsw.Close()
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handleFS(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.cfg.Logger.Warn("library watcher fsnotify error", "err", err)
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *Watcher) handleFS(ev fsnotify.Event) {
	if w.shouldIgnore(ev.Name) {
		return
	}
	switch {
	case ev.Has(fsnotify.Create):
		w.markPending(ev.Name)
		w.emit(Event{Path: ev.Name, Basename: filepath.Base(ev.Name), Kind: KindCreated})
	case ev.Has(fsnotify.Write):
		w.markPending(ev.Name)
		w.emit(Event{Path: ev.Name, Basename: filepath.Base(ev.Name), Kind: KindWritten})
	case ev.Has(fsnotify.Rename), ev.Has(fsnotify.Remove):
		w.drop(ev.Name)
		w.index.Remove(ev.Name)
		w.emit(Event{Path: ev.Name, Basename: filepath.Base(ev.Name), Kind: KindRemoved})
	}
}

func (w *Watcher) shouldIgnore(p string) bool {
	base := filepath.Base(p)
	for _, suf := range w.cfg.IgnoreSuffixes {
		if suf != "" && strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

func (w *Watcher) markPending(p string) {
	fi, err := os.Stat(p)
	if err != nil {
		return
	}
	if fi.IsDir() {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[p] = &pendingFile{
		size:     fi.Size(),
		settleAt: time.Now().Add(w.cfg.Settle),
	}
}

func (w *Watcher) drop(p string) {
	w.mu.Lock()
	delete(w.pending, p)
	w.mu.Unlock()
}

// poll re-stats each in-flight file. Size changes reset the settle window;
// stable size past the settle deadline transitions to KindStable and adds
// the file to the index.
func (w *Watcher) poll() {
	now := time.Now()
	w.mu.Lock()
	type entry struct {
		path     string
		size     int64
		settleAt time.Time
	}
	snapshot := make([]entry, 0, len(w.pending))
	for p, pf := range w.pending {
		snapshot = append(snapshot, entry{path: p, size: pf.size, settleAt: pf.settleAt})
	}
	w.mu.Unlock()

	for _, e := range snapshot {
		fi, err := os.Stat(e.path)
		if err != nil {
			w.drop(e.path)
			w.index.Remove(e.path)
			w.emit(Event{Path: e.path, Basename: filepath.Base(e.path), Kind: KindRemoved})
			continue
		}
		curSize := fi.Size()
		if curSize != e.size {
			w.mu.Lock()
			if cur, ok := w.pending[e.path]; ok {
				cur.size = curSize
				cur.settleAt = now.Add(w.cfg.Settle)
			}
			w.mu.Unlock()
			continue
		}
		if !now.Before(e.settleAt) && curSize > 0 {
			w.drop(e.path)
			w.index.Add(e.path)
			w.emit(Event{
				Path:     e.path,
				Basename: filepath.Base(e.path),
				Size:     curSize,
				Kind:     KindStable,
			})
		}
	}
}

func (w *Watcher) emit(ev Event) {
	select {
	case w.events <- ev:
	default:
		// Consumer is slow; drop. Library events are advisory — a missed
		// "written" is fine; the next "stable" still arrives via poll.
		w.cfg.Logger.Warn("library watcher dropped event", "path", ev.Path, "kind", ev.Kind)
	}
}
