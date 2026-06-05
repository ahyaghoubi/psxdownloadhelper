package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// httpDownloader is a minimal stdlib downloader: it streams a URL to disk while
// tracking progress and speed. It is the engine used when aria2c is unavailable
// and the deterministic engine the test suite drives (no subprocess, no aria2).
type httpDownloader struct {
	client *http.Client
	mu     sync.Mutex
	jobs   map[string]*httpJob
	seq    int64
}

type httpJob struct {
	id        string
	cancel    context.CancelFunc
	completed atomic.Int64
	total     atomic.Int64
	started   time.Time
	mu        sync.Mutex
	state     State
	errMsg    string
}

// NewHTTP returns an HTTP-based Downloader. A nil client uses a no-timeout
// default (downloads can take hours).
func NewHTTP(client *http.Client) Downloader {
	if client == nil {
		client = &http.Client{}
	}
	return &httpDownloader{client: client, jobs: make(map[string]*httpJob)}
}

func (d *httpDownloader) Add(ctx context.Context, rawURL, dir string) (string, error) {
	name := basename(rawURL)
	if name == "" {
		return "", fmt.Errorf("downloader: cannot derive filename from %q", rawURL)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.seq++
	id := strconv.FormatInt(d.seq, 10)
	jobCtx, cancel := context.WithCancel(context.Background())
	j := &httpJob{id: id, cancel: cancel, started: time.Now(), state: StateActive}
	d.jobs[id] = j
	d.mu.Unlock()

	go d.run(jobCtx, j, rawURL, filepath.Join(dir, name))
	return id, nil
}

func (d *httpDownloader) run(ctx context.Context, j *httpJob, rawURL, dst string) {
	fail := func(err error) {
		j.mu.Lock()
		j.state = StateError
		j.errMsg = err.Error()
		j.mu.Unlock()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		fail(err)
		return
	}
	resp, err := d.client.Do(req)
	if err != nil {
		fail(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Errorf("upstream HTTP %d", resp.StatusCode))
		return
	}
	if resp.ContentLength > 0 {
		j.total.Store(resp.ContentLength)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		fail(err)
		return
	}
	cw := &countingWriter{w: f, n: &j.completed}
	_, copyErr := io.Copy(cw, resp.Body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		fail(copyErr)
		return
	}
	if syncErr != nil {
		fail(syncErr)
		return
	}
	if closeErr != nil {
		fail(closeErr)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		fail(err)
		return
	}
	j.mu.Lock()
	j.state = StateComplete
	j.mu.Unlock()
}

func (d *httpDownloader) Status(_ context.Context, id string) (Status, error) {
	d.mu.Lock()
	j := d.jobs[id]
	d.mu.Unlock()
	if j == nil {
		return Status{}, fmt.Errorf("downloader: unknown job %q", id)
	}
	j.mu.Lock()
	state, errMsg := j.state, j.errMsg
	j.mu.Unlock()
	completed := j.completed.Load()
	elapsed := time.Since(j.started).Seconds()
	var speed int64
	if elapsed > 0 {
		speed = int64(float64(completed) / elapsed)
	}
	return Status{
		ID:        id,
		State:     state,
		Completed: completed,
		Total:     j.total.Load(),
		SpeedBPS:  speed,
		Err:       errMsg,
	}, nil
}

func (d *httpDownloader) Remove(_ context.Context, id string) error {
	d.mu.Lock()
	j := d.jobs[id]
	delete(d.jobs, id)
	d.mu.Unlock()
	if j == nil {
		return fmt.Errorf("downloader: unknown job %q", id)
	}
	j.cancel()
	return nil
}

func (d *httpDownloader) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, j := range d.jobs {
		j.cancel()
	}
	d.jobs = make(map[string]*httpJob)
	return nil
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	written, err := c.w.Write(p)
	c.n.Add(int64(written))
	return written, err
}

// basename extracts the trailing filename of a URL path, ignoring the query.
func basename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
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

var _ Downloader = (*httpDownloader)(nil)
