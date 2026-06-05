// Package persist provides an append-only JSON-Lines sink for
// capture.Event. The proxy can survive a crash or restart without
// losing observed URLs by tee'ing every Publish into a file in this
// format.
//
// Layout: one Event per line, encoded as JSON. The file is opened in
// O_APPEND mode so multiple psxdh runs concatenate cleanly. When
// FSync is true, every write is fsync'd — slow, but bullet-proof for
// the "I want to never lose a URL" use case.
package persist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

// Sink writes capture events to a JSONL file. The zero value is not usable;
// construct via Open. Sink is safe for concurrent Writes.
type Sink struct {
	mu    sync.Mutex
	f     *os.File
	fsync bool
}

// Open creates (or appends to) the JSONL file at path. Parent directories
// are created with 0755 if missing.
func Open(path string, fsync bool) (*Sink, error) {
	if path == "" {
		return nil, errors.New("persist: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("persist: mkdir parent: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("persist: open %s: %w", path, err)
	}
	return &Sink{f: f, fsync: fsync}, nil
}

// Write appends ev to the file as one JSON-encoded line.
func (s *Sink) Write(ev capture.Event) error {
	rec := record{
		Time:       ev.Time.UTC().Format(time.RFC3339Nano),
		Method:     ev.Method,
		URL:        ev.URL.String(),
		Host:       ev.URL.Host,
		Path:       ev.URL.Path,
		Kind:       string(ev.Kind),
		ClientAddr: ev.ClientAddr,
	}
	if ev.Hint.TitleHint != "" || ev.Hint.PartIndex >= 0 {
		rec.Hint = &hint{
			TitleHint: ev.Hint.TitleHint,
			PartIndex: ev.Hint.PartIndex,
		}
	}
	if ev.Headers != nil {
		// Keep a small allow-list of headers — the full set is noisy and
		// often contains tokens / session IDs we don't want on disk.
		if v := headerOf(ev.Headers, "User-Agent"); v != "" {
			rec.UserAgent = v
		}
		if v := headerOf(ev.Headers, "Range"); v != "" {
			rec.Range = v
		}
	}
	buf, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("persist: marshal: %w", err)
	}
	buf = append(buf, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.f.Write(buf); err != nil {
		return fmt.Errorf("persist: write: %w", err)
	}
	if s.fsync {
		if err := s.f.Sync(); err != nil {
			return fmt.Errorf("persist: fsync: %w", err)
		}
	}
	return nil
}

// Close flushes and closes the underlying file.
func (s *Sink) Close() error {
	if s == nil || s.f == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.f.Close()
	s.f = nil
	return err
}

// Subscribe attaches the sink to bus and returns a Worker that drives
// event drains. Callers should call Worker.Run() in a goroutine; events
// published to bus after Subscribe returns are guaranteed to reach the
// worker (no "Publish before Subscribe" race).
func (s *Sink) Subscribe(bus capture.Bus) *Worker {
	ch, unsubscribe := bus.Subscribe()
	return &Worker{sink: s, ch: ch, unsubscribe: unsubscribe}
}

// Worker drives a Sink against a single bus subscription.
type Worker struct {
	sink        *Sink
	ch          <-chan capture.Event
	unsubscribe func()
}

// Run drains the subscription into the sink until ctx is canceled or
// the bus closes the channel. errLog (optional) receives any disk
// write errors so the proxy can continue running.
func (w *Worker) Run(ctx context.Context, errLog func(error)) error {
	if errLog == nil {
		errLog = func(error) {}
	}
	defer w.unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.ch:
			if !ok {
				return nil
			}
			if err := w.sink.Write(ev); err != nil {
				errLog(err)
			}
		}
	}
}

// record is the on-disk JSON shape. We don't reuse capture.Event because
// its URL field is a *url.URL whose default JSON encoding is verbose
// and not stable across Go versions.
type record struct {
	Time       string `json:"time"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Host       string `json:"host"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	ClientAddr string `json:"client_addr,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	Range      string `json:"range,omitempty"`
	Hint       *hint  `json:"hint,omitempty"`
}

type hint struct {
	TitleHint string `json:"title_hint,omitempty"`
	PartIndex int    `json:"part_index,omitempty"`
}

func headerOf(h http.Header, k string) string {
	if h == nil {
		return ""
	}
	return h.Get(k)
}

// ReadAll loads every record from a JSONL file. Useful for offline
// inspection / replay; not used at runtime.
func ReadAll(path string) ([]capture.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("persist: open %s: %w", path, err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var out []capture.Event
	for {
		var r record
		err := dec.Decode(&r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, fmt.Errorf("persist: decode: %w", err)
		}
		u, err := url.Parse(r.URL)
		if err != nil {
			return out, fmt.Errorf("persist: parse url %q: %w", r.URL, err)
		}
		t, _ := time.Parse(time.RFC3339Nano, r.Time)
		ev := capture.Event{
			Method:     r.Method,
			URL:        u,
			Kind:       match.Kind(r.Kind),
			Time:       t,
			ClientAddr: r.ClientAddr,
		}
		if r.Hint != nil {
			ev.Hint = match.Hint{TitleHint: r.Hint.TitleHint, PartIndex: r.Hint.PartIndex}
		} else {
			ev.Hint = match.Hint{PartIndex: -1}
		}
		if r.UserAgent != "" || r.Range != "" {
			ev.Headers = http.Header{}
			if r.UserAgent != "" {
				ev.Headers.Set("User-Agent", r.UserAgent)
			}
			if r.Range != "" {
				ev.Headers.Set("Range", r.Range)
			}
		}
		out = append(out, ev)
	}
	return out, nil
}
