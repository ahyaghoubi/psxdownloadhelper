// Package tracker remembers what the console has asked for: each captured
// CDN file, its size, how many bytes went to the console from Sony or from
// the library, and whether each package is a game to download on the PC or
// an update the console fetches itself. Save and Load keep it across
// restarts.
package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/parts"
)

// sparseGap is how far past what the console already has a request must
// start to count as a jump. Updates jump around inside a big package to
// fetch only the changed pieces; a game download moves forward.
const sparseGap = 32 << 20

// Who downloads a package.
const (
	ModeAuto    = ""        // decide from what the console does
	ModePC      = "pc"      // download on the PC (games)
	ModeConsole = "console" // let the console fetch it from Sony (updates)
)

// Package is what psxdh knows about one package (all parts of a game or
// update).
type Package struct {
	Mode   string `json:"mode,omitempty"`
	Sparse bool   `json:"sparse,omitempty"` // console fetched scattered pieces: looks like an update
}

// ConsoleDownloads reports whether the console should fetch this package
// itself rather than the user downloading it on the PC.
func (p Package) ConsoleDownloads() bool {
	return p.Mode == ModeConsole || (p.Mode == ModeAuto && p.Sparse)
}

// Item is one captured CDN file.
type Item struct {
	Key       string // host + path; query strings vary between requests
	URL       string // most recent full URL, safe to paste into a downloader
	Name      string // file name, used to find it in the library
	Kind      match.Kind
	Package   string // name shared by all parts, see parts.PackageName
	Title     string // PPSA01234 / CUSA01234 when the URL contains one
	Part      int    // _N.pkg index, -1 when absent
	FirstSeen time.Time
	LastSeen  time.Time
	// Requested is true once the console itself asked for this file. Parts
	// psxdh found on its own start false.
	Requested bool

	Size        int64 // full size reported by Sony, 0 when unknown
	FromSony    int64 // bytes the console pulled from Sony through psxdh
	FromSonyAt  time.Time
	SonyReach   int64 // furthest byte offset the console has had from Sony
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
	mu       sync.Mutex
	items    map[string]*Item
	packages map[string]*Package
	console  Console
	finding  map[string]int // title -> part searches in progress
	version  uint64         // bumped on every change worth saving
	now      func() time.Time
}

// New returns an empty Tracker.
func New() *Tracker {
	return &Tracker{
		items:    make(map[string]*Item),
		packages: make(map[string]*Package),
		finding:  make(map[string]int),
		now:      time.Now,
	}
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
			Package:   parts.PackageName(path.Base(u.Path)),
			Kind:      kind,
			Title:     hint.TitleHint,
			Part:      hint.PartIndex,
			FirstSeen: now,
		}
		t.items[key] = it
	}
	it.URL = u.String()
	it.LastSeen = now
	it.Requested = true
	t.version++
	return key
}

// Found records a part psxdh discovered on its own. It does nothing if the
// file is already known. It reports whether the item was added.
func (t *Tracker) Found(u *url.URL, kind match.Kind, title string, part int, size int64) bool {
	key := KeyOf(u)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.items[key]; ok {
		return false
	}
	t.items[key] = &Item{
		Key: key, URL: u.String(), Name: path.Base(u.Path), Package: parts.PackageName(path.Base(u.Path)), Kind: kind,
		Title: title, Part: part, FirstSeen: now, LastSeen: now, Size: size,
	}
	t.version++
	return true
}

// Has reports whether key is tracked.
func (t *Tracker) Has(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.items[key]
	return ok
}

// StartFinding marks a part search for title as running; call the returned
// function when it ends.
func (t *Tracker) StartFinding(title string) (done func()) {
	t.mu.Lock()
	t.finding[title]++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.finding[title]--; t.finding[title] <= 0 {
				delete(t.finding, title)
			}
		})
	}
}

// Finding reports the titles with a part search running.
func (t *Tracker) Finding() map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]bool, len(t.finding))
	for k := range t.finding {
		out[k] = true
	}
	return out
}

// SetSize records the full size of a file as reported by Sony.
func (t *Tracker) SetSize(key string, size int64) {
	if size <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok && it.Size != size {
		it.Size = size
		t.version++
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

// AddFromSony counts n bytes forwarded from Sony to the console, ending at
// byte offset end of the file.
func (t *Tracker) AddFromSony(key string, n, end int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		it.FromSony += n
		it.FromSonyAt = t.now()
		if end > it.SonyReach {
			it.SonyReach = end
		}
		t.version++
	}
}

// NoteRequest records that the console asked Sony for key starting at byte
// start. A start well past what the console already has means it is
// fetching scattered pieces, as it does for updates; the package is then
// marked sparse. It returns the package's state after the request.
func (t *Tracker) NoteRequest(key string, start int64) Package {
	t.mu.Lock()
	defer t.mu.Unlock()
	it, ok := t.items[key]
	if !ok {
		return Package{}
	}
	p := t.pkg(it.Package)
	if start > it.SonyReach+sparseGap && !p.Sparse {
		p.Sparse = true
		t.version++
	}
	return *p
}

// PackageOf returns the state of the package key belongs to.
func (t *Tracker) PackageOf(key string) Package {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		if p, ok := t.packages[it.Package]; ok {
			return *p
		}
	}
	return Package{}
}

// SetMode records who downloads a package: ModePC, ModeConsole or
// ModeAuto. It reports whether the package exists.
func (t *Tracker) SetMode(pkg, mode string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	found := false
	for _, it := range t.items {
		if it.Package == pkg {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	t.pkg(pkg).Mode = mode
	t.version++
	return true
}

// Packages returns the state of every package.
func (t *Tracker) Packages() map[string]Package {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Package, len(t.packages))
	for k, v := range t.packages {
		out[k] = *v
	}
	return out
}

// pkg returns the package entry for name, creating it. Callers hold t.mu.
func (t *Tracker) pkg(name string) *Package {
	p, ok := t.packages[name]
	if !ok {
		p = &Package{}
		t.packages[name] = p
	}
	return p
}

// AddFromLibrary counts bytes served to the console from the library.
func (t *Tracker) AddFromLibrary(key string, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it, ok := t.items[key]; ok {
		it.FromLibrary += n
		it.FromLibAt = t.now()
		t.version++
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
	t.packages = make(map[string]*Package)
	t.version++
}

// Version changes whenever there is something new to save.
func (t *Tracker) Version() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version
}

type savedState struct {
	Items    []Item             `json:"items"`
	Packages map[string]Package `json:"packages,omitempty"`
}

// Save writes the captured files to path, so links survive a restart of
// psxdh. The write is atomic.
func (t *Tracker) Save(path string) error {
	t.mu.Lock()
	st := savedState{Items: make([]Item, 0, len(t.items)), Packages: make(map[string]Package, len(t.packages))}
	for _, it := range t.items {
		st.Items = append(st.Items, *it)
	}
	for k, v := range t.packages {
		st.Packages[k] = *v
	}
	t.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load restores files saved by Save. A missing file is not an error.
func (t *Tracker) Load(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st savedState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("read saved links %s: %w", path, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range st.Items {
		it := st.Items[i]
		if it.Key == "" {
			continue
		}
		if it.Package == "" { // saved by a release before packages existed
			it.Package = parts.PackageName(it.Name)
		}
		t.items[it.Key] = &it
	}
	for k, v := range st.Packages {
		p := v
		t.packages[k] = &p
	}
	return nil
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
