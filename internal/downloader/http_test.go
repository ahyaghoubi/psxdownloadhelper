package downloader

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestHTTPDownloaderFetchesFile(t *testing.T) {
	body := bytes.Repeat([]byte("psxdh-part-bytes"), 4096) // 64 KiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewHTTP(nil)
	defer d.Close()

	id, err := d.Add(context.Background(), srv.URL+"/GAME_0.pkg", dir)
	if err != nil {
		t.Fatal(err)
	}

	// Poll to completion.
	deadline := time.Now().Add(3 * time.Second)
	var st Status
	for {
		st, err = d.Status(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if st.Done() {
			break
		}
		if st.State == StateError {
			t.Fatalf("download errored: %s", st.Err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not complete; last state=%s completed=%d", st.State, st.Completed)
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, err := os.ReadFile(filepath.Join(dir, "GAME_0.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded file differs: got %d bytes, want %d", len(got), len(body))
	}
	if st.Completed != int64(len(body)) {
		t.Errorf("completed=%d, want %d", st.Completed, len(body))
	}
}

func TestHTTPDownloaderUnknownJob(t *testing.T) {
	d := NewHTTP(nil)
	defer d.Close()
	if _, err := d.Status(context.Background(), "nope"); err == nil {
		t.Error("expected error for unknown job")
	}
}

func TestMapState(t *testing.T) {
	cases := map[string]State{
		"active": StateActive, "waiting": StateWaiting, "complete": StateComplete,
		"error": StateError, "removed": StateRemoved, "paused": StatePaused, "weird": StateWaiting,
	}
	for in, want := range cases {
		if got := mapState(in); got != want {
			t.Errorf("mapState(%q)=%v want %v", in, got, want)
		}
	}
}
