package persist

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOpenAndWriteSingleRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	s, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ev := capture.Event{
		Time:       time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Method:     "GET",
		URL:        mustURL(t, "http://gst.prod.dl.playstation.net/foo.pkg?bar=1"),
		Kind:       match.KindPKGApp,
		Hint:       match.Hint{TitleHint: "CUSA12345", PartIndex: -1},
		ClientAddr: "10.0.0.42:55555",
		Headers: http.Header{
			"User-Agent": []string{"Test/1.0"},
			"Range":      []string{"bytes=0-1024"},
		},
	}
	if err := s.Write(ev); err != nil {
		t.Fatal(err)
	}
	s.Close()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		`"method":"GET"`,
		`"kind":"pkg-app"`,
		`"url":"http://gst.prod.dl.playstation.net/foo.pkg?bar=1"`,
		`"user_agent":"Test/1.0"`,
		`"range":"bytes=0-1024"`,
		`"title_hint":"CUSA12345"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in JSONL line: %s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("JSONL line should end with newline")
	}
}

func TestWriteAppendsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	for i := 0; i < 3; i++ {
		s, err := Open(path, false)
		if err != nil {
			t.Fatal(err)
		}
		ev := capture.Event{
			Time:   time.Now(),
			Method: "GET",
			URL:    mustURL(t, "http://example.com/"),
			Kind:   match.KindUnknown,
			Hint:   match.Hint{PartIndex: -1},
		}
		if err := s.Write(ev); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}

	body, _ := os.ReadFile(path)
	lines := strings.Count(string(body), "\n")
	if lines != 3 {
		t.Errorf("expected 3 lines after 3 separate Open+Write, got %d", lines)
	}
}

func TestReadAllRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	s, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	in := []capture.Event{
		{
			Time:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Method: "GET",
			URL:    mustURL(t, "https://a.example/x.pkg"),
			Kind:   match.KindPKGBase,
			Hint:   match.Hint{TitleHint: "CUSA00001", PartIndex: -1},
		},
		{
			Time:   time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
			Method: "GET",
			URL:    mustURL(t, "https://b.example/y.pkg"),
			Kind:   match.KindPKGPatch,
			Hint:   match.Hint{PartIndex: -1},
		},
	}
	for _, ev := range in {
		if err := s.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	out, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("ReadAll = %d events, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].URL.String() != in[i].URL.String() {
			t.Errorf("event %d URL = %q, want %q", i, out[i].URL, in[i].URL)
		}
		if out[i].Kind != in[i].Kind {
			t.Errorf("event %d Kind = %q, want %q", i, out[i].Kind, in[i].Kind)
		}
	}
}

func TestSinkRunSubscribesToBus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	s, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}

	bus := capture.NewBus(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := s.Subscribe(bus)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, nil) }()

	bus.Publish(capture.Event{
		Time:   time.Now(),
		Method: "GET",
		URL:    mustURL(t, "http://example.com/a.pkg"),
		Kind:   match.KindPKGApp,
		Hint:   match.Hint{PartIndex: -1},
	})
	bus.Publish(capture.Event{
		Time:   time.Now(),
		Method: "GET",
		URL:    mustURL(t, "http://example.com/b.pkg"),
		Kind:   match.KindPKGBase,
		Hint:   match.Hint{PartIndex: -1},
	})

	// Give Run a moment to drain.
	deadline := time.Now().Add(2 * time.Second)
	for {
		body, _ := os.ReadFile(path)
		if strings.Count(string(body), "\n") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run did not write 2 events in time, file=%q", body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done
	s.Close()
}

func TestOpenRequiresPath(t *testing.T) {
	_, err := Open("", false)
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "events.jsonl")
	s, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent not created: %v", err)
	}
}
