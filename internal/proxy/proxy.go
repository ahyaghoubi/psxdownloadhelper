// Package proxy is the HTTP proxy the console points at.
//
// For every request it:
//  1. classifies the URL and records PlayStation CDN files in the tracker;
//  2. serves the file from the library when a complete copy is there;
//  3. otherwise forwards the request to Sony unchanged.
//
// HTTPS (CONNECT) is tunnelled untouched; psxdh never decrypts PSN traffic.
// Requests that are not proxy requests (a browser opening
// http://<pc>:8080/) go to the dashboard, so one address does both jobs.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/parts"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/tracker"
)

// Deps are the proxy's collaborators.
type Deps struct {
	Rules     *match.RuleSet
	Library   *library.Library
	Tracker   *tracker.Tracker
	Dashboard http.Handler
	// IsSelf reports whether a host:port is psxdh itself, so a browser that
	// sends absolute URLs to us reaches the dashboard instead of looping.
	IsSelf func(hostport string) bool
	// UpstreamProxy optionally routes forwarded HTTP requests via another proxy.
	UpstreamProxy *url.URL
	Logger        *slog.Logger
	// Context bounds background part searches. Defaults to Background.
	Context context.Context
	// DisableFindParts turns off searching for the other parts of a game.
	DisableFindParts bool
}

// Server implements http.Handler.
type Server struct {
	d         Deps
	serve     *serve.Handler
	transport *http.Transport
	finder    *parts.Finder
	log       *slog.Logger

	mu        sync.Mutex
	searched  map[string]bool // series already searched since start
	userAgent atomic.Value    // the console's User-Agent, for searches started from the dashboard
}

// New returns a proxy Server.
func New(d Deps) (*Server, error) {
	if d.Rules == nil || d.Library == nil || d.Tracker == nil || d.Dashboard == nil {
		return nil, errors.New("proxy: missing dependency")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.IsSelf == nil {
		d.IsSelf = func(string) bool { return false }
	}
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		DisableCompression:    true, // pass bytes through exactly as Sony sent them
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if d.UpstreamProxy != nil {
		tr.Proxy = http.ProxyURL(d.UpstreamProxy)
	}
	if d.Context == nil {
		d.Context = context.Background()
	}
	return &Server{
		d: d, serve: serve.New(d.Logger), transport: tr, log: d.Logger,
		finder:   &parts.Finder{Transport: tr},
		searched: make(map[string]bool),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.d.Tracker.ConsoleSeen(r.RemoteAddr)
		s.tunnel(w, r)
		return
	}
	if !r.URL.IsAbs() || s.d.IsSelf(r.URL.Host) {
		s.d.Dashboard.ServeHTTP(w, r)
		return
	}
	s.d.Tracker.ConsoleSeen(r.RemoteAddr)

	kind, hint := s.d.Rules.Classify(r.URL)
	key := ""
	if kind != match.KindUnknown && kind != match.KindIgnore {
		key = s.d.Tracker.Seen(r.URL, kind, hint)
	}

	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && s.serveLocal(w, r, kind, key) {
		return
	}
	start := rangeStart(r.Header.Get("Range"))
	if key != "" {
		s.userAgent.Store(r.UserAgent())
		// Only look for the other parts when the console has to go to
		// Sony (if this part is on the PC, the user has the links), and
		// not for packages the user marked as updates.
		if !s.d.Tracker.PackageOf(key).ConsoleDownloads() {
			s.findParts(r.URL, r.Header, kind, hint)
		}
	}
	s.forward(w, r, key, start)
}

// findParts starts a background search for the other parts of a numbered
// package the first time the console asks for any part of it, so the user
// can download the whole game without keeping the console connected.
func (s *Server) findParts(u *url.URL, header http.Header, kind match.Kind, hint match.Hint) {
	if s.d.DisableFindParts || !match.IsPackage(kind) || parts.Index(u) < 0 {
		return
	}
	// Keyed by list generation too, so after Clear the next request searches again.
	series := fmt.Sprintf("%d/%s", s.d.Tracker.Generation(), parts.SeriesKey(u))
	s.mu.Lock()
	if s.searched[series] {
		s.mu.Unlock()
		return
	}
	s.searched[series] = true
	s.mu.Unlock()

	seed := *u
	header = header.Clone()
	gen, done := s.d.Tracker.StartFinding(hint.TitleHint)
	go func() {
		defer done()
		var n atomic.Int64 // found is called from parallel searches
		s.finder.Discover(s.d.Context, &seed, header,
			func(i int) bool { return s.d.Tracker.Has(tracker.KeyOf(parts.WithIndex(&seed, i))) },
			func(f parts.Found) {
				if s.d.Tracker.Found(gen, f.URL, kind, hint.TitleHint, f.Index, f.Size) {
					n.Add(1)
				}
			})
		s.log.Info("finished looking for other parts", "title", hint.TitleHint, "found", n.Load())
	}()
}

// FindPartsFor searches for every part of the package that rawURL belongs
// to. The dashboard uses it when the user decides to download a package on
// the PC that psxdh had treated as an update.
func (s *Server) FindPartsFor(rawURL string, kind match.Kind, title string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	h := http.Header{}
	if ua, _ := s.userAgent.Load().(string); ua != "" {
		h.Set("User-Agent", ua)
	}
	s.findParts(u, h, kind, match.Hint{TitleHint: title, PartIndex: parts.Index(u)})
}

// rangeStart returns the first byte offset of a "bytes=N-…" Range header, or 0.
func rangeStart(h string) int64 {
	v, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok {
		return 0
	}
	v, _, _ = strings.Cut(v, "-")
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// serveLocal serves the request from the library when a complete copy
// exists. It reports whether it handled the request.
func (s *Server) serveLocal(w http.ResponseWriter, r *http.Request, kind match.Kind, key string) bool {
	name := path.Base(r.URL.Path)
	// Only PlayStation CDN files (or anything ending .pkg) are served from
	// disk, so an unrelated URL that happens to share a name is never hijacked.
	if kind == match.KindUnknown && !strings.EqualFold(path.Ext(name), ".pkg") {
		return false
	}
	// Sony publishes a new revision of a package under the same file names.
	// Once a name has more than one revision, only a copy written while the
	// requested revision was the newest can be that revision.
	win := s.d.Tracker.Window(key)
	f, ok := s.d.Library.LookupFunc(name, func(f library.File) bool { return win.Fits(f.ModTime) })
	if !ok {
		if old, had := s.d.Library.Lookup(name); had {
			s.log.Warn("library file is from an older revision of this package; downloading from Sony instead",
				"file", old.Rel, "modified", old.ModTime)
		}
		return false
	}
	if want := s.d.Tracker.Size(key); want > 0 && f.Size != want {
		s.log.Warn("library file has the wrong size; downloading from Sony instead",
			"file", f.Rel, "have", f.Size, "want", want)
		return false
	}
	// A console resuming a download it started from Sony sends If-Range with
	// Sony's ETag or Last-Modified. Neither matches the local copy, so
	// ServeContent would ignore Range and send the whole file with a 200,
	// which the console appends at its resume offset and then reports as
	// corrupted. The file name (and size, when known) already ties this copy
	// to Sony's, so honour the Range unconditionally.
	if r.Header.Get("If-Range") != "" {
		r = r.Clone(r.Context())
		r.Header.Del("If-Range")
	}
	s.log.Info("serving from library", "file", f.Rel, "range", r.Header.Get("Range"))
	cw := &countingWriter{ResponseWriter: w}
	if key != "" {
		cw.add = func(n int64) { s.d.Tracker.AddFromLibrary(key, n) }
	}
	s.serve.ServeFile(cw, r, f.Path)
	return true
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, key string, start int64) {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	if r.ContentLength == 0 {
		out.Body = nil
	}
	stripHopByHop(out.Header)

	resp, err := s.transport.RoundTrip(out)
	if err != nil {
		s.log.Warn("upstream request failed", "url", r.URL.String(), "err", err)
		http.Error(w, "psxdh could not reach "+r.URL.Host+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if key != "" {
		s.d.Tracker.SetSize(key, fullSize(resp))
	}
	stripHopByHop(resp.Header)
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	cw := &countingWriter{ResponseWriter: w}
	// Count file bytes only: an error page from Sony isn't part of the file.
	if key != "" && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {
		var written int64
		cw.add = func(n int64) {
			written += n
			s.d.Tracker.AddFromSony(key, n, start+written)
		}
	}
	buf := bufPool.Get().(*[]byte)
	defer bufPool.Put(buf)
	_, _ = io.CopyBuffer(cw, resp.Body, *buf)
}

// tunnel relays an HTTPS CONNECT without inspecting it.
func (s *Server) tunnel(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT not supported", http.StatusInternalServerError)
		return
	}
	upstream, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
	if err != nil {
		s.log.Warn("CONNECT dial failed", "host", r.Host, "err", err)
		http.Error(w, "psxdh could not reach "+r.Host, http.StatusBadGateway)
		return
	}
	client, buffered, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		// Bytes the client sent right after CONNECT may sit in the reader.
		_, _ = io.Copy(upstream, buffered)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
	client.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		_ = tc.CloseWrite()
		return
	}
	_ = c.Close()
}

// fullSize returns the whole-file size from a 200 or 206 response.
func fullSize(resp *http.Response) int64 {
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.ContentLength
	case http.StatusPartialContent:
		// Content-Range: bytes 0-99/12345
		cr := resp.Header.Get("Content-Range")
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer",
	"Transfer-Encoding", "Upgrade", "Proxy-Authenticate", "Proxy-Authorization",
}

func stripHopByHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, k := range hopByHop {
		h.Del(k)
	}
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 256<<10); return &b }}

type countingWriter struct {
	http.ResponseWriter
	add func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	if c.add != nil && n > 0 {
		c.add(int64(n))
	}
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
