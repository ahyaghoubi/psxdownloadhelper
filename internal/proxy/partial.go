package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// partialCache tees a successful upstream forward to disk under
// library.dir/.psxdh-partial/<basename>, and atomically renames to
// library.dir/<basename> when the response completes cleanly.
//
// v1 scope (see docs/decisions/0003-network-resilience.md):
//   - Only non-Range GETs are eligible (clients sending Range want a
//     specific slice, not the whole file).
//   - We never resume from a previous partial. A failed forward leaves
//     the .partial file on disk as a debugging breadcrumb; the next
//     forward overwrites it.
//   - We honour Content-Length when present: if we wrote at least
//     MinSize bytes AND the byte count matches Content-Length, the
//     rename happens. Otherwise the partial is left behind.
type partialCache struct {
	libDir  string
	minSize int64
	logger  *slog.Logger

	mu       sync.Mutex
	inflight map[string]struct{} // basenames currently being written
}

func newPartialCache(libDir string, minSize int64, logger *slog.Logger) *partialCache {
	return &partialCache{
		libDir:   libDir,
		minSize:  minSize,
		logger:   logger,
		inflight: make(map[string]struct{}),
	}
}

// Eligible reports whether (req, resp) should be tee'd. The conditions are:
//   - GET method
//   - No Range header on the request
//   - Response is 200 OK with a Content-Length we can parse
//   - URL path ends in a filename that doesn't collide with an in-flight
//     write
func (p *partialCache) Eligible(req *http.Request, resp *http.Response) bool {
	if p == nil || req.Method != http.MethodGet {
		return false
	}
	if req.Header.Get("Range") != "" {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if resp.ContentLength <= 0 {
		return false
	}
	if p.minSize > 0 && resp.ContentLength < p.minSize {
		return false
	}
	name := basenameFromURL(req.URL)
	if name == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, busy := p.inflight[name]; busy {
		return false
	}
	if _, err := os.Stat(filepath.Join(p.libDir, name)); err == nil {
		// File is already in the library — no need to cache again.
		return false
	}
	return true
}

// Tee returns an io.Reader that yields the same bytes resp.Body would, while
// also writing them to a temp file. The caller must drain the returned reader
// in full (e.g. via io.Copy to the client) and then call done(err) where
// err is nil on success and non-nil on any client-side write failure.
func (p *partialCache) Tee(req *http.Request, resp *http.Response) (io.Reader, func(error), error) {
	name := basenameFromURL(req.URL)
	partDir := filepath.Join(p.libDir, ".psxdh-partial")
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("partial: mkdir %s: %w", partDir, err)
	}
	partPath := filepath.Join(partDir, name+".partial")
	f, err := os.Create(partPath)
	if err != nil {
		return nil, nil, fmt.Errorf("partial: create %s: %w", partPath, err)
	}

	p.mu.Lock()
	p.inflight[name] = struct{}{}
	p.mu.Unlock()

	// MultiWriter writes every chunk both to the client and to disk.
	body := resp.Body
	reader := io.TeeReader(body, f)

	expected := resp.ContentLength
	done := func(cause error) {
		_ = f.Sync()
		closeErr := f.Close()

		p.mu.Lock()
		delete(p.inflight, name)
		p.mu.Unlock()

		if cause != nil {
			p.logger.Warn("partial: forward errored; keeping .partial",
				"basename", name, "err", cause)
			return
		}
		if closeErr != nil {
			p.logger.Warn("partial: close failed", "basename", name, "err", closeErr)
			return
		}
		fi, err := os.Stat(partPath)
		if err != nil {
			p.logger.Warn("partial: stat failed", "basename", name, "err", err)
			return
		}
		if fi.Size() != expected {
			p.logger.Warn("partial: size mismatch (probably truncated)",
				"basename", name, "got", fi.Size(), "want", expected)
			return
		}
		final := filepath.Join(p.libDir, name)
		if err := os.Rename(partPath, final); err != nil {
			p.logger.Warn("partial: rename failed",
				"basename", name, "err", err, "src", partPath, "dst", final)
			return
		}
		p.logger.Info("partial: promoted to library",
			"basename", name, "size", fi.Size())
	}
	return reader, done, nil
}

// basenameFromURL extracts the trailing filename component of u's path. It
// returns "" for paths that end in a slash or are otherwise unsuitable
// (empty / dot / parent).
func basenameFromURL(u *url.URL) string {
	if u == nil {
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
	if strings.ContainsAny(name, `/\:*?"<>|`) {
		return ""
	}
	return name
}
