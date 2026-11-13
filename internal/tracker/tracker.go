// Package tracker remembers what the console has asked for since psxdh
// started: each captured CDN file, its size, and how many bytes went to the
// console from Sony or from the library. It is in memory only.
package tracker

import (
	"net"
	"net/url"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

// Item is one captured CDN file.
type Item struct {
	Key       string // host + path; query strings vary between requests
	URL       string // most recent full URL, safe to paste into a downloader
	Name      string // file name, used to find it in the library
	Kind      match.Kind
	Title     string // PPSA01234 / CUSA01234 when the URL contains one
	Part      int    // _N.pkg index, -1 when absent
	FirstSeen time.Time
	LastSeen  time.Time

	Size        int64 // full size reported by Sony, 0 when unknown
	FromSony    int64 // bytes the console pulled from Sony through psxdh
	FromSonyAt  time.Time
	FromLibrary int64 // bytes psxdh served to the console from the library
	FromLibAt   time.Time
}

// Console is the most recent console that used the proxy.
type Console struct {
	Addr     string
	LastSeen time.Time
}

// Tracker is safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	items   map[string]*Item
	console Console
	now     func() time.Time
}

// New returns an empty Tracker.
func New() *Tracker {
	return &Tracker{items: make(map[string]*Item), now: time.Now}
}

// KeyOf returns the tracking key for u.
func KeyOf(u *url.URL) string { return u.Host + u.Path }

// Seen records a request for a classified URL and returns its key.
func (t *Tracker) Seen(u *url.URL, kind match.Kind, hint match.Hint) string {
	key := KeyOf(u)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	it, ok := t.items[key]
	if !ok {
		it = &Item{
			Key:       key,
			Name:      path.Base(u.Path),
			Kind:      kind,
			Title:     hint.TitleHint,
			Part:      hint.PartIndex,
			FirstSeen: now,
		}
		t.items[key] = it
	}
	it.URL = u.String()
	it.LastSeen = now
	return key
}

// SetSize records the full size of a file as reported by Sony.
func (t *Tracker) SetSize(key string, size int64) {
	if size <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		it.Size = size
	}
}

// Size returns the recorded size for key.
func (t *Tracker) Size(key string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		return it.Size
	}
	return 0
}

// AddFromSony counts bytes forwarded from Sony to the console.
func (t *Tracker) AddFromSony(key string, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		it.FromSony += n
		it.FromSonyAt = t.now()
	}
}

// AddFromLibrary counts bytes served to the console from the library.
func (t *Tracker) AddFromLibrary(key string, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		it.FromLibrary += n
		it.FromLibAt = t.now()
	}
}

// ConsoleSeen records traffic from the console at remoteAddr.
func (t *Tracker) ConsoleSeen(remoteAddr string) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.console = Console{Addr: host, LastSeen: t.now()}
}

// Clear forgets every captured file.
func (t *Tracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = make(map[string]*Item)
}

// Snapshot returns copies of all items plus the console status. Titles the
// console used most recently come first; within a title, files are in part
// order.
func (t *Tracker) Snapshot() ([]Item, Console) {
	t.mu.Lock()
	out := make([]Item, 0, len(t.items))
	for _, it := range t.items {
		out = append(out, *it)
	}
	console := t.console
	t.mu.Unlock()

	latest := map[string]time.Time{}
	for _, it := range out {
		if it.FirstSeen.After(latest[it.Title]) {
			latest[it.Title] = it.FirstSeen
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Title != b.Title {
			if ta, tb := latest[a.Title], latest[b.Title]; !ta.Equal(tb) {
				return ta.After(tb)
			}
			return a.Title < b.Title
		}
		if a.Part != b.Part {
			return a.Part < b.Part
		}
		return a.Name < b.Name
	})
	return out, console
}
