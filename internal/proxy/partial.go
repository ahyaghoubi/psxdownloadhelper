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
	"strconv"
	"strings"
	"sync"
)

// partialCache tees a successful upstream forward to disk under
// library.dir/.psxdh-partial/<basename>, and atomically renames to
// library.dir/<basename> when the response completes cleanly.
//
// Scope (see docs/decisions/0003-network-resilience.md and
// docs/network-resilience.md):
//   - GET forwards are cached when they describe trustworthy whole-object bytes:
//     200 OK bodies from byte zero, or 206 ranges with a parseable Content-Range.
//   - We honour Content-Length: the rename happens only when the byte count
//     matches.
//   - When resume is enabled, a `.partial` left behind by a dropped forward is
//     continued on the next eligible forward by issuing an upstream
//     Range/If-Range request for the remainder — gated on the upstream
//     validators (ETag / Last-Modified / total size) still matching. On any
//     mismatch we fall back to a fresh download; we never stitch bytes from two
//     different objects.
type partialCache struct {
	libDir    string
	minSize   int64
	resume    bool
	logger    *slog.Logger
	onPromote func(string)

	mu       sync.Mutex
	inflight map[string]struct{} // basenames currently being written
}

func newPartialCache(libDir string, minSize int64, resume bool, logger *slog.Logger, onPromote func(string)) *partialCache {
	return &partialCache{
		libDir:    libDir,
		minSize:   minSize,
		resume:    resume,
		logger:    logger,
		onPromote: onPromote,
		inflight:  make(map[string]struct{}),
	}
}

// forwardPlan is the pre-forward decision for a candidate request. A nil plan
// means "forward normally, do not cache". resumeFrom > 0 means a resumable
// `.partial` was found and the forward should request the remainder.
type forwardPlan struct {
	name        string
	clientRange bool
	resumeFrom  int64
	validator   string // If-Range value
	meta        *partialMeta
}

// teeResult describes how forward should stream the upstream response to the
// client. When ok is false the response is streamed verbatim without caching.
// When err is non-nil the forward must abort (the partial state was invalid
// and has been discarded); the client should retry.
type teeResult struct {
	reader        io.Reader
	done          func(error)
	status        int   // client-facing status code
	contentLength int64 // when > 0, override Content-Length and drop Content-Range
	ok            bool
	err           error
}

// partDir returns the hidden directory holding in-progress downloads.
func (p *partialCache) partDir() string { return filepath.Join(p.libDir, ".psxdh-partial") }

// Plan inspects an incoming request and decides whether it is a partial-cache
// candidate, and whether a resumable `.partial` already exists for it.
func (p *partialCache) Plan(req *http.Request) *forwardPlan {
	if p == nil || req.Method != http.MethodGet {
		return nil
	}
	name := basenameFromURL(req.URL)
	if name == "" {
		return nil
	}
	p.mu.Lock()
	_, busy := p.inflight[name]
	p.mu.Unlock()
	if busy {
		return nil
	}
	if _, err := os.Stat(filepath.Join(p.libDir, name)); err == nil {
		return nil // already in the library
	}
	plan := &forwardPlan{name: name, clientRange: req.Header.Get("Range") != ""}
	if p.resume && !plan.clientRange {
		if m, from := p.resumeState(req, name); m != nil {
			plan.resumeFrom = from
			plan.validator = m.validator()
			plan.meta = m
		}
	}
	return plan
}

// resumeState returns the saved meta and on-disk byte count when a valid,
// resumable `.partial` exists for name; otherwise (nil, 0).
func (p *partialCache) resumeState(req *http.Request, name string) (*partialMeta, int64) {
	m, err := readMeta(filepath.Join(p.partDir(), name+".partial.meta"))
	if err != nil || m == nil {
		return nil, 0
	}
	if m.validator() == "" || m.URL != req.URL.String() || m.ContentLength <= 0 {
		return nil, 0
	}
	fi, err := os.Stat(filepath.Join(p.partDir(), name+".partial"))
	if err != nil {
		return nil, 0 // meta but no .partial → fresh
	}
	n := fi.Size()
	if n <= 0 || n >= m.ContentLength {
		return nil, 0 // empty or already (over)complete → fresh
	}
	if len(m.Spans) == 0 {
		m.Spans = []span{{Start: 0, End: n}}
	} else {
		m.Spans = normaliseSpans(m.Spans, m.ContentLength)
	}
	from := m.coveredPrefix()
	if from <= 0 || from >= m.ContentLength || from > n {
		return nil, 0
	}
	return m, from
}

// Begin reconciles the upstream response against the plan and returns how to
// stream it to the client. It is the post-response counterpart to Plan.
func (p *partialCache) Begin(plan *forwardPlan, req *http.Request, resp *http.Response) teeResult {
	if plan == nil {
		return teeResult{}
	}
	if plan.clientRange {
		if resp.StatusCode == http.StatusPartialContent {
			return p.beginRange(req, resp, plan.name)
		}
		return teeResult{}
	}
	if plan.resumeFrom > 0 {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			return p.beginResume(plan, resp)
		case http.StatusOK:
			// Server ignored our Range, or the validator changed (If-Range
			// downgraded to a full body). Treat as a fresh full download.
			return p.beginFresh(req, resp, plan.name)
		default:
			return teeResult{} // error response — stream verbatim, keep partial
		}
	}
	return p.beginFresh(req, resp, plan.name)
}

// beginFresh starts a brand-new tee-to-disk for a 200 OK full response.
func (p *partialCache) beginFresh(req *http.Request, resp *http.Response, name string) teeResult {
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return teeResult{}
	}
	if p.minSize > 0 && resp.ContentLength < p.minSize {
		return teeResult{}
	}
	if err := os.MkdirAll(p.partDir(), 0o755); err != nil {
		p.logger.Warn("partial: mkdir failed", "err", err)
		return teeResult{}
	}
	partPath := filepath.Join(p.partDir(), name+".partial")
	f, err := os.Create(partPath)
	if err != nil {
		p.logger.Warn("partial: create failed", "basename", name, "err", err)
		return teeResult{}
	}
	// Record validators up front so a dropped download is resumable next run.
	meta := metaFromResponse(req.URL.String(), name, resp, resp.ContentLength)
	if err := writeMeta(filepath.Join(p.partDir(), name+".partial.meta"), meta); err != nil {
		p.logger.Warn("partial: meta write failed", "basename", name, "err", err)
	}
	p.markInflight(name)
	reader := io.TeeReader(resp.Body, f)
	done := p.finisher(name, partPath, []io.Closer{f}, meta, 0, meta.ContentLength, nil)
	return teeResult{reader: reader, done: done, status: http.StatusOK, ok: true}
}

// beginRange tees a client-requested 206 response into the partial file at the
// exact offset described by Content-Range while preserving the 206 for the client.
func (p *partialCache) beginRange(req *http.Request, resp *http.Response, name string) teeResult {
	start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || total <= 0 || end <= start {
		return teeResult{}
	}
	if p.minSize > 0 && total < p.minSize {
		return teeResult{}
	}
	if err := os.MkdirAll(p.partDir(), 0o755); err != nil {
		p.logger.Warn("partial: mkdir failed", "err", err)
		return teeResult{}
	}
	partPath := filepath.Join(p.partDir(), name+".partial")
	meta := metaFromResponse(req.URL.String(), name, resp, total)
	metaPath := filepath.Join(p.partDir(), name+".partial.meta")
	if existing, err := readMeta(metaPath); err == nil && existing != nil {
		existing.Spans = normaliseSpans(existing.Spans, existing.ContentLength)
		if len(existing.Spans) == 0 {
			if fi, statErr := os.Stat(partPath); statErr == nil && fi.Size() > 0 && fi.Size() <= existing.ContentLength {
				existing.Spans = []span{{Start: 0, End: fi.Size()}}
			}
		}
		if !existing.compatible(meta) {
			p.discard(name)
		} else {
			meta = *existing
			meta.ETag = chooseNonEmpty(meta.ETag, resp.Header.Get("ETag"))
			meta.LastModified = chooseNonEmpty(meta.LastModified, resp.Header.Get("Last-Modified"))
		}
	} else if err != nil {
		p.logger.Warn("partial: meta read failed; starting fresh range cache", "basename", name, "err", err)
		p.discard(name)
	}
	if err := writeMeta(metaPath, meta); err != nil {
		p.logger.Warn("partial: meta write failed", "basename", name, "err", err)
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		p.logger.Warn("partial: open failed", "basename", name, "err", err)
		return teeResult{}
	}
	p.markInflight(name)
	wr := &offsetWriter{w: f, off: start}
	reader := io.TeeReader(resp.Body, wr)
	done := p.finisher(name, partPath, []io.Closer{f}, meta, start, end, func() int64 { return wr.n })
	return teeResult{reader: reader, done: done, status: resp.StatusCode, ok: true}
}

// beginResume continues an existing `.partial` from a 206 response carrying the
// remaining bytes. The client receives a synthesised 200 with the full body.
func (p *partialCache) beginResume(plan *forwardPlan, resp *http.Response) teeResult {
	total := parseContentRangeTotal(resp.Header.Get("Content-Range"))
	if total != plan.meta.ContentLength {
		// The object changed underneath us despite If-Range; the on-disk prefix
		// is no longer trustworthy. Discard and force a fresh download.
		p.discard(plan.name)
		return teeResult{err: fmt.Errorf("partial: resume validator mismatch (got total %d, want %d)", total, plan.meta.ContentLength)}
	}
	partPath := filepath.Join(p.partDir(), plan.name+".partial")
	rd, err := os.Open(partPath)
	if err != nil {
		return teeResult{}
	}
	wr, err := os.OpenFile(partPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		_ = rd.Close()
		return teeResult{}
	}
	p.markInflight(plan.name)
	prefix := io.LimitReader(rd, plan.resumeFrom)
	reader := io.MultiReader(prefix, io.TeeReader(resp.Body, wr))
	done := p.finisher(plan.name, partPath, []io.Closer{wr, rd}, *plan.meta, plan.resumeFrom, plan.meta.ContentLength, nil)
	p.logger.Info("partial: resuming download", "basename", plan.name, "from", plan.resumeFrom, "total", total)
	return teeResult{reader: reader, done: done, status: http.StatusOK, contentLength: plan.meta.ContentLength, ok: true}
}

// finisher returns the done callback that closes handles and, on a clean
// completion (size matches expected), atomically promotes the file into the
// library and removes the sidecar. On failure the `.partial` and its meta are
// kept so the next forward can resume.
func (p *partialCache) finisher(name, partPath string, closers []io.Closer, meta partialMeta, start, end int64, written func() int64) func(error) {
	return func(cause error) {
		for _, c := range closers {
			if sf, ok := c.(interface{ Sync() error }); ok {
				_ = sf.Sync()
			}
		}
		var closeErr error
		for _, c := range closers {
			if err := c.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		p.clearInflight(name)

		if cause != nil {
			p.logger.Warn("partial: forward errored; keeping .partial for resume", "basename", name, "err", cause)
			return
		}
		if closeErr != nil {
			p.logger.Warn("partial: close failed", "basename", name, "err", closeErr)
			return
		}
		if written != nil && written() != end-start {
			p.logger.Warn("partial: short range write; keeping .partial for resume",
				"basename", name, "got", written(), "want", end-start)
			return
		}
		fi, err := os.Stat(partPath)
		if err != nil {
			p.logger.Warn("partial: stat failed", "basename", name, "err", err)
			return
		}
		if fi.Size() < end {
			p.logger.Warn("partial: size mismatch (probably truncated); keeping for resume",
				"basename", name, "got", fi.Size(), "want-at-least", end)
			return
		}
		meta.addSpan(start, end)
		if !meta.complete() {
			if err := writeMeta(filepath.Join(p.partDir(), name+".partial.meta"), meta); err != nil {
				p.logger.Warn("partial: meta write failed", "basename", name, "err", err)
			}
			return
		}
		if fi.Size() != meta.ContentLength {
			p.logger.Warn("partial: size mismatch (probably sparse or truncated); keeping for resume",
				"basename", name, "got", fi.Size(), "want", meta.ContentLength)
			if err := writeMeta(filepath.Join(p.partDir(), name+".partial.meta"), meta); err != nil {
				p.logger.Warn("partial: meta write failed", "basename", name, "err", err)
			}
			return
		}
		final := filepath.Join(p.libDir, name)
		if err := os.Rename(partPath, final); err != nil {
			p.logger.Warn("partial: rename failed", "basename", name, "err", err, "src", partPath, "dst", final)
			return
		}
		_ = os.Remove(filepath.Join(p.partDir(), name+".partial.meta"))
		if p.onPromote != nil {
			p.onPromote(final)
		}
		p.logger.Info("partial: promoted to library", "basename", name, "size", fi.Size())
	}
}

func (p *partialCache) markInflight(name string) {
	p.mu.Lock()
	p.inflight[name] = struct{}{}
	p.mu.Unlock()
}

func (p *partialCache) clearInflight(name string) {
	p.mu.Lock()
	delete(p.inflight, name)
	p.mu.Unlock()
}

// discard removes the `.partial` and its sidecar so the next forward starts fresh.
func (p *partialCache) discard(name string) {
	_ = os.Remove(filepath.Join(p.partDir(), name+".partial"))
	_ = os.Remove(filepath.Join(p.partDir(), name+".partial.meta"))
}

type offsetWriter struct {
	w   io.WriterAt
	off int64
	n   int64
}

func (w *offsetWriter) Write(p []byte) (int, error) {
	n, err := w.w.WriteAt(p, w.off)
	w.off += int64(n)
	w.n += int64(n)
	return n, err
}

func chooseNonEmpty(current, next string) string {
	if current != "" {
		return current
	}
	return next
}

// parseContentRange extracts a half-open [start, end) span and total from a
// "bytes start-end/total" header.
func parseContentRange(h string) (start, end, total int64, ok bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bytes ") {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.TrimSpace(h[len("bytes "):])
	slash := strings.LastIndex(rangeAndTotal, "/")
	if slash < 0 {
		return 0, 0, 0, false
	}
	rangePart := strings.TrimSpace(rangeAndTotal[:slash])
	totalPart := strings.TrimSpace(rangeAndTotal[slash+1:])
	if totalPart == "" || totalPart == "*" {
		return 0, 0, 0, false
	}
	total, err := strconv.ParseInt(totalPart, 10, 64)
	if err != nil || total <= 0 {
		return 0, 0, 0, false
	}
	dash := strings.Index(rangePart, "-")
	if dash <= 0 || dash == len(rangePart)-1 {
		return 0, 0, 0, false
	}
	start, err = strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, false
	}
	last, err := strconv.ParseInt(strings.TrimSpace(rangePart[dash+1:]), 10, 64)
	if err != nil || last < start || last >= total {
		return 0, 0, 0, false
	}
	return start, last + 1, total, true
}

// parseContentRangeTotal extracts Total from a "bytes start-end/Total" header.
// Returns -1 when absent or unparseable (so it never accidentally equals a
// real Content-Length).
func parseContentRangeTotal(h string) int64 {
	if _, _, total, ok := parseContentRange(h); ok {
		return total
	}
	i := strings.LastIndex(h, "/")
	if i < 0 {
		return -1
	}
	total := strings.TrimSpace(h[i+1:])
	if total == "" || total == "*" {
		return -1
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return -1
	}
	return n
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
