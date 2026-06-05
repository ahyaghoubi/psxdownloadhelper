// Package session aggregates capture events into per-title download sessions
// so the dashboard can show progress (which parts are pending, local, or
// verified) for each game. It is a read-model built from the capture bus,
// cross-referenced against the library at query time.
package session

import (
	"context"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
)

// Library is the read surface the session store needs from the library index.
type Library interface {
	Resolve(u *url.URL) (string, bool)
	VerifyStateOf(path string) library.VerifyState
}

// Part is one file (PKG/manifest/crc) observed for a title.
type Part struct {
	Basename  string `json:"basename"`
	Kind      string `json:"kind"`
	PartIndex int    `json:"part_index"`
	URL       string `json:"url"`
	Local     bool   `json:"local"`
	Verified  string `json:"verified"` // "ok" | "failed" | "unchecked"
}

// Session is the aggregate view of one title's download.
type Session struct {
	Title      string `json:"title"`
	Parts      []Part `json:"parts"`
	LocalCount int    `json:"local_count"`
	TotalCount int    `json:"total_count"`
}

type record struct {
	url       *url.URL
	kind      string
	partIndex int
}

// Store holds the per-title, per-basename records and resolves local state on
// demand. Safe for concurrent Record and Snapshot.
type Store struct {
	lib Library

	mu    sync.Mutex
	parts map[string]map[string]*record // title → basename → record
}

// New creates a Store. lib may be nil (then parts never report Local/Verified).
func New(lib Library) *Store {
	return &Store{lib: lib, parts: make(map[string]map[string]*record)}
}

// Record folds one capture event into the read-model.
func (s *Store) Record(ev capture.Event) {
	if ev.URL == nil {
		return
	}
	base := basenameOf(ev.URL)
	if base == "" {
		return
	}
	title := ev.Hint.TitleHint
	if title == "" {
		title = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byBase, ok := s.parts[title]
	if !ok {
		byBase = make(map[string]*record)
		s.parts[title] = byBase
	}
	byBase[base] = &record{url: ev.URL, kind: string(ev.Kind), partIndex: ev.Hint.PartIndex}
}

// LoadFromEvents replays a slice of capture events through Record. Used by the
// import path (file → store) and the optional startup restore so the dashboard
// shows persisted state without waiting for a fresh PS5 to reconnect. Record
// is idempotent; calling LoadFromEvents twice with the same events is safe.
func (s *Store) LoadFromEvents(events []capture.Event) {
	for _, ev := range events {
		s.Record(ev)
	}
}

// Run subscribes to bus and records events until ctx is canceled.
func (s *Store) Run(ctx context.Context, bus capture.Bus) {
	ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			s.Record(ev)
		}
	}
}

// Snapshot returns the current sessions, sorted by title, parts sorted by
// index then basename. Local/Verified are resolved against the library now.
func (s *Store) Snapshot() []Session {
	s.mu.Lock()
	titles := make([]string, 0, len(s.parts))
	type pair struct {
		base string
		rec  record
	}
	grouped := make(map[string][]pair, len(s.parts))
	for title, byBase := range s.parts {
		titles = append(titles, title)
		for base, rec := range byBase {
			grouped[title] = append(grouped[title], pair{base: base, rec: *rec})
		}
	}
	s.mu.Unlock()

	sort.Strings(titles)
	out := make([]Session, 0, len(titles))
	for _, title := range titles {
		ps := grouped[title]
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].rec.partIndex != ps[j].rec.partIndex {
				return ps[i].rec.partIndex < ps[j].rec.partIndex
			}
			return ps[i].base < ps[j].base
		})
		sess := Session{Title: title}
		for _, p := range ps {
			part := Part{
				Basename:  p.base,
				Kind:      p.rec.kind,
				PartIndex: p.rec.partIndex,
				URL:       p.rec.url.String(),
				Verified:  "unchecked",
			}
			if s.lib != nil {
				if localPath, ok := s.lib.Resolve(p.rec.url); ok {
					part.Local = true
					switch s.lib.VerifyStateOf(localPath) {
					case library.VerifyOK:
						part.Verified = "ok"
					case library.VerifyFailed:
						part.Verified = "failed"
					}
				}
			}
			if part.Local {
				sess.LocalCount++
			}
			sess.TotalCount++
			sess.Parts = append(sess.Parts, part)
		}
		out = append(out, sess)
	}
	return out
}

func basenameOf(u *url.URL) string {
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return ""
	}
	name := path.Base(p)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}
