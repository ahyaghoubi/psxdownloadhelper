package library

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// titleIDRegex matches the PlayStation title-id forms seen on the CDN.
// Mirrors internal/match's regex; duplicated here so library doesn't have
// to depend on match.
var titleIDRegex = regexp.MustCompile(`UP\d{4}-CUSA\d{5}|CUSA\d{5}|PPSA\d{5}`)

// Resolver maps a captured URL to a local file path that the serve
// package can stream back to the console.
type Resolver interface {
	Resolve(u *url.URL) (path string, ok bool)
}

// Layout controls how Resolve interprets the library tree.
//
//   - LayoutBasename (default): every file is keyed only by its basename;
//     the directory tree is for the user's organisation.
//   - LayoutPerTitle: paths must additionally match a title-id segment from
//     the URL (PPSA / CUSA / UP). Used when multiple titles share basenames.
type Layout string

const (
	LayoutBasename Layout = "basename"
	LayoutPerTitle Layout = "per-title"
)

// Index is a concurrent-safe in-memory catalogue of files in the library
// directory, keyed by basename. It is populated by an initial walk and
// kept current by Watcher.
type Index struct {
	mu         sync.RWMutex
	root       string
	layout     Layout
	byBasename map[string][]string // basename → absolute paths
}

// NewIndex builds an Index by walking root once. The walk includes every
// regular file. Files that arrive later go through Watcher → Index.Add.
func NewIndex(root string, layout Layout) (*Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	i := &Index{
		root:       abs,
		layout:     layout,
		byBasename: make(map[string][]string),
	}
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			// An empty library is valid; the watcher (or user) will populate it.
			return i, nil
		}
		return nil, err
	}
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		i.addLocked(p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return i, nil
}

// Root returns the absolute path of the library root.
func (i *Index) Root() string { return i.root }

// Layout returns the configured layout strategy.
func (i *Index) Layout() Layout { return i.layout }

// Add records a file in the index. Idempotent — the same path will not be
// indexed twice.
func (i *Index) Add(p string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.addLocked(p)
}

func (i *Index) addLocked(p string) {
	base := filepath.Base(p)
	for _, existing := range i.byBasename[base] {
		if existing == p {
			return
		}
	}
	i.byBasename[base] = append(i.byBasename[base], p)
}

// Remove drops a path from the index.
func (i *Index) Remove(p string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	base := filepath.Base(p)
	paths := i.byBasename[base]
	out := paths[:0]
	for _, existing := range paths {
		if existing != p {
			out = append(out, existing)
		}
	}
	if len(out) == 0 {
		delete(i.byBasename, base)
		return
	}
	i.byBasename[base] = out
}

// Resolve implements Resolver. It returns ok=false when nothing matches or
// when multiple files share the same basename and the layout can't
// disambiguate. The proxy's idempotency rule (docs/architecture.md) mandates
// a deterministic mapping: we never auto-pick between ambiguous candidates.
func (i *Index) Resolve(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return "", false
	}
	i.mu.RLock()
	candidates := append([]string(nil), i.byBasename[base]...)
	i.mu.RUnlock()
	if len(candidates) == 0 {
		return "", false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	if i.layout == LayoutPerTitle {
		if hint := extractTitleHint(u.Path); hint != "" {
			match := ""
			for _, p := range candidates {
				if strings.Contains(p, hint) {
					if match != "" && match != p {
						return "", false // ambiguous even within title
					}
					match = p
				}
			}
			if match != "" {
				return match, true
			}
		}
	}
	return "", false // ambiguous
}

// Stats returns counts useful for the admin dashboard.
func (i *Index) Stats() (files, basenames int) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	basenames = len(i.byBasename)
	for _, ps := range i.byBasename {
		files += len(ps)
	}
	return
}

// All returns a snapshot of every (basename, paths) entry in the index.
// Used by the admin API and tests.
func (i *Index) All() map[string][]string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make(map[string][]string, len(i.byBasename))
	for k, v := range i.byBasename {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// extractTitleHint returns the first PlayStation title-id substring in p,
// preferring the longer UP1234-CUSA12345 form. Empty when no id is found.
func extractTitleHint(p string) string {
	return titleIDRegex.FindString(p)
}
