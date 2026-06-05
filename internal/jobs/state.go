package jobs

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

// PartState is the persisted lifecycle of one PKG part.
type PartState string

const (
	PartPending  PartState = "pending"
	PartLocal    PartState = "local"
	PartVerified PartState = "verified"
	PartDone     PartState = "done"
)

// JobPart is one file within a title's download job.
type JobPart struct {
	Basename  string    `json:"basename"`
	URL       string    `json:"url"`
	Kind      string    `json:"kind"`
	PartIndex int       `json:"part_index"`
	State     PartState `json:"state"`
}

// Job is the persisted view of one title's download progress.
type Job struct {
	Title      string    `json:"title"`
	Parts      []JobPart `json:"parts"`
	Enumerated bool      `json:"enumerated"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// StateStore persists job snapshots to a JSON file.
type StateStore struct {
	path string
	mu   sync.Mutex
}

// NewStateStore returns a store for path. Empty path disables persistence.
func NewStateStore(path string) *StateStore {
	if path == "" {
		return nil
	}
	return &StateStore{path: path}
}

// Path returns the on-disk file path, or "" when disabled.
func (s *StateStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load reads jobs from disk. A missing file yields nil jobs, not an error.
func (s *StateStore) Load() ([]Job, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobs: read state: %w", err)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("jobs: parse state: %w", err)
	}
	return jobs, nil
}

// Save atomically writes jobs to disk.
func (s *StateStore) Save(jobs []Job) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("jobs: mkdir state dir: %w", err)
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return fmt.Errorf("jobs: marshal state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("jobs: write state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("jobs: rename state: %w", err)
	}
	return nil
}

// DeriveInput is everything needed to build a job snapshot from live state.
type DeriveInput struct {
	Sessions   []session.Session
	Library    libraryBasenameChecker
	Enumerated map[string]bool // title → was CDN-enumerated
}

type libraryBasenameChecker interface {
	HasBasename(name string) bool
}

// DeriveJobs builds persisted jobs from the session read-model, overlaying
// cluster/library truth for part states. UpdatedAt is set to now on every call.
func DeriveJobs(in DeriveInput) []Job {
	now := time.Now().UTC()
	out := make([]Job, 0, len(in.Sessions))
	for _, sess := range in.Sessions {
		if sess.Title == "unknown" {
			continue
		}
		j := Job{
			Title:      sess.Title,
			Enumerated: in.Enumerated[sess.Title],
			UpdatedAt:  now,
		}
		for _, p := range sess.Parts {
			if !match.IsPushableKind(match.Kind(p.Kind)) {
				continue
			}
			st := PartPending
			if p.Local {
				st = PartLocal
			}
			if p.Verified == "ok" {
				st = PartVerified
			}
			if in.Library != nil && in.Library.HasBasename(p.Basename) {
				st = PartDone
			}
			j.Parts = append(j.Parts, JobPart{
				Basename:  p.Basename,
				URL:       p.URL,
				Kind:      p.Kind,
				PartIndex: p.PartIndex,
				State:     st,
			})
		}
		if len(j.Parts) > 0 {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// MergeJobs combines two job lists, keeping the entry with the newer UpdatedAt
// per title.
func MergeJobs(a, b []Job) []Job {
	byTitle := make(map[string]Job, len(a)+len(b))
	for _, j := range a {
		byTitle[j.Title] = j
	}
	for _, j := range b {
		if existing, ok := byTitle[j.Title]; !ok || j.UpdatedAt.After(existing.UpdatedAt) {
			byTitle[j.Title] = j
		}
	}
	out := make([]Job, 0, len(byTitle))
	for _, j := range byTitle {
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// EventsFromJobs converts persisted jobs back into capture events for session
// replay.
func EventsFromJobs(jobs []Job) []capture.Event {
	var out []capture.Event
	now := time.Now().UTC()
	for _, j := range jobs {
		for _, p := range j.Parts {
			if p.URL == "" {
				continue
			}
			u, err := url.Parse(p.URL)
			if err != nil {
				continue
			}
			out = append(out, capture.Event{
				Time:   now,
				Method: "GET",
				URL:    u,
				Kind:   match.Kind(p.Kind),
				Hint:   match.Hint{TitleHint: j.Title, PartIndex: p.PartIndex},
			})
		}
	}
	return out
}

// ResubmitPending queues any title that still has non-done parts in the cluster
// manager. Submit is idempotent for titles already tracked.
func ResubmitPending(jobs []Job, mgr *cluster.Manager) {
	if mgr == nil {
		return
	}
	for _, j := range jobs {
		var pending []cluster.PartURL
		for _, p := range j.Parts {
			if p.State == PartDone {
				continue
			}
			if p.URL == "" {
				continue
			}
			idx := p.PartIndex
			if idx < 0 {
				idx = 0
			}
			pending = append(pending, cluster.PartURL{
				Index:    idx,
				URL:      p.URL,
				Basename: p.Basename,
			})
		}
		if len(pending) > 0 {
			mgr.Submit(j.Title, pending)
		}
	}
}

// Debouncer coalesces frequent save triggers into one write after delay.
type Debouncer struct {
	mu    sync.Mutex
	delay time.Duration
	fn    func()
	timer *time.Timer
}

// NewDebouncer returns a debouncer that calls fn after delay with no further
// triggers. Pass delay <= 0 for 500 ms.
func NewDebouncer(delay time.Duration, fn func()) *Debouncer {
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	return &Debouncer{delay: delay, fn: fn}
}

// Trigger schedules fn; repeated calls reset the timer.
func (d *Debouncer) Trigger() {
	if d == nil || d.fn == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.delay, d.fn)
}
