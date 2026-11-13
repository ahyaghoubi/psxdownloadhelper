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

	"github.com/ahyaghoubi/psxdownloadhelper/internal/verify"
	"github.com/fsnotify/fsnotify"
)

const partialCacheDirName = ".psxdh-partial"

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
	// Verifier, when non-nil, is run on each KindStable transition: if a
	// `.crc` sidecar exists next to the stabilised PKG (or the stabilised
	// file is itself a `.crc`), the PKG is verified and its integrity status
	// recorded in the Index. A corrupt file is marked VerifyFailed so the
	// proxy refuses to serve it. ErrUnknownFormat is non-fatal (skipped).
	Verifier verify.Verifier
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
	w := &Watcher{
		cfg:     cfg,
		index:   index,
		fsw:     fsw,
		events:  make(chan Event, 64),
		pending: make(map[string]*pendingFile),
	}
	if err := w.addWatchTree(index.Root()); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	return w, nil
}

// Events returns the channel of library events. Consumers must drain it
// promptly or the watcher will block on emit.
func (w *Watcher) Events() <-chan Event { return w.events }

// Run blocks until ctx is canceled, dispatching fsnotify events and polling
// in-flight files for the settle condition. The events channel is closed on
// return so consumers ranging over it see a clean shutdown.
func (w *Watcher) Run(ctx context.Context) error {
	defer close(w.events)
	defer func() { _ = w.fsw.Close() }()
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
		if w.watchCreatedDir(ev.Name) {
			return
		}
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

// addWatchTree registers a watch on root and every directory beneath it.
// fsnotify is not recursive, so each subdirectory needs its own watch for
// files created inside it to surface.
func (w *Watcher) addWatchTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if filepath.Base(path) == partialCacheDirName && path != root {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(path); err != nil {
			return fmt.Errorf("watch %q: %w", path, err)
		}
		return nil
	})
}

// watchCreatedDir handles a freshly created path that turns out to be a
// directory: it watches the new subtree and indexes any files already inside
// it. Returns true when path was a directory (so the caller skips the
// file-oriented Create handling).
func (w *Watcher) watchCreatedDir(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return false
	}
	if err := w.addWatchTree(path); err != nil {
		w.cfg.Logger.Warn("library watcher could not watch directory", "path", path, "err", err)
	}
	// fsnotify only reports events that happen after a watch is added, so a
	// directory that appears already populated (an atomic rename, or a writer
	// that creates files faster than the create event is delivered) would lose
	// those files. Scan the new subtree once to catch them.
	w.indexExistingFiles(path)
	return true
}

// indexExistingFiles marks every regular file under root as pending so the
// settle poller promotes it. Safe to run alongside fsnotify create events for
// the same files: markPending is idempotent and library events are advisory.
func (w *Watcher) indexExistingFiles(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if w.shouldIgnore(path) {
			return nil
		}
		w.markPending(path)
		w.emit(Event{Path: path, Basename: filepath.Base(path), Kind: KindCreated})
		return nil
	})
}

func (w *Watcher) shouldIgnore(p string) bool {
	return isInsideDir(p, partialCacheDirName) || IsInProgressPath(p, w.cfg.IgnoreSuffixes)
}

func isInsideDir(p, dir string) bool {
	for {
		if filepath.Base(p) == dir {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
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
		if w.shouldIgnore(e.path) {
			w.mu.Lock()
			if cur, ok := w.pending[e.path]; ok {
				cur.size = curSize
				cur.settleAt = now.Add(w.cfg.Settle)
			}
			w.mu.Unlock()
			continue
		}
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
			w.verifyStable(e.path)
			w.emit(Event{
				Path:     e.path,
				Basename: filepath.Base(e.path),
				Size:     curSize,
				Kind:     KindStable,
			})
		}
	}
}

// verifyStable runs integrity verification when a file settles. The stabilised
// file may be either the PKG (we look for "<pkg>.crc" beside it) or the `.crc`
// sidecar itself (we verify its neighbouring PKG). The PKG's status is recorded
// on the Index so the proxy can refuse to serve a corrupt file.
func (w *Watcher) verifyStable(p string) {
	if w.cfg.Verifier == nil {
		return
	}
	pkgPath, crcPath := p, p+".crc"
	if strings.EqualFold(filepath.Ext(p), ".crc") {
		pkgPath = strings.TrimSuffix(p, filepath.Ext(p))
		crcPath = p
	}
	if _, err := os.Stat(crcPath); err != nil {
		return // no sidecar → leave unchecked
	}
	if _, err := os.Stat(pkgPath); err != nil {
		return // sidecar without its PKG yet
	}
	crc, err := verify.ParseCRC(crcPath)
	if err != nil {
		if !errors.Is(err, verify.ErrUnknownFormat) {
			w.cfg.Logger.Warn("verify: parse .crc failed", "crc", crcPath, "err", err)
		}
		return
	}
	ok, err := w.cfg.Verifier.Verify(pkgPath, crc)
	switch {
	case err != nil:
		w.cfg.Logger.Warn("verify: read failed; marking unverified-failed", "pkg", pkgPath, "err", err)
		w.index.SetVerifyState(pkgPath, VerifyFailed)
	case ok:
		w.cfg.Logger.Info("verify: integrity OK", "pkg", pkgPath, "alg", crc.Algorithm)
		w.index.SetVerifyState(pkgPath, VerifyOK)
	default:
		w.cfg.Logger.Warn("verify: integrity FAILED; proxy will not serve", "pkg", pkgPath, "alg", crc.Algorithm)
		w.index.SetVerifyState(pkgPath, VerifyFailed)
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
