package proxy

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/retry"
)

// hopByHopHeaders are stripped on both forward and response per RFC 7230 §6.1.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
	"Proxy-Authenticate",
	"Proxy-Authorization",
}

// handleHTTP processes absolute-URI GET/HEAD requests received as a forward
// proxy. The pipeline is the one drawn in docs/architecture.md
// (Request handling pipeline):
//
//	classify → publish capture event → library hit? serve : forward (per mode)
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Scheme == "" || r.URL.Host == "" {
		http.Error(w, "absolute URL required for proxied request", http.StatusBadRequest)
		return
	}

	kind, hint := s.rules.Classify(r.URL)

	if kind != match.KindUnknown || s.cfg.Capture.LogIgnored {
		s.bus.Publish(capture.Event{
			URL:        r.URL,
			Method:     r.Method,
			Kind:       kind,
			Hint:       hint,
			Headers:    r.Header.Clone(),
			Time:       time.Now(),
			ClientAddr: r.RemoteAddr,
		})
	}

	if path, ok := s.res.Resolve(r.URL); ok {
		if s.libraryServeOK(path, r.URL) {
			s.logger.Info("library hit", "url", r.URL.String(), "kind", kind, "path", path)
			s.serve.ServeFile(w, r, path)
			return
		}
		// A corrupt or wrong-sized local file must not be served: fall through
		// to the forward path so the console re-fetches correct bytes.
		s.logger.Warn("library file failed integrity gate; forwarding upstream", "url", r.URL.String(), "path", path)
	}

	switch s.cfg.Forward.Mode {
	case "strict":
		s.logger.Info("strict mode: blocking unmatched URL", "url", r.URL.String())
		http.Error(w, "psxdh strict mode: file not in library", http.StatusBadGateway)
		return
	case "cache":
		if kind == match.KindUnknown {
			s.logger.Info("cache mode: blocking unclassified URL", "url", r.URL.String())
			http.Error(w, "psxdh cache mode: unclassified URL", http.StatusBadGateway)
			return
		}
	}

	s.forward(w, r)
}

// forward proxies the request upstream byte-for-byte, preserving query
// strings and Range semantics. Hop-by-hop headers are stripped both ways.
//
// Transient upstream failures are retried per the configured retry policy,
// but only BEFORE any response bytes have been written to the client (see
// the invariant in internal/retry). Once we start streaming, a mid-stream
// failure bubbles up so the console can re-issue with a Range header.
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	// Decide partial-cache handling before forwarding: a resumable .partial
	// turns the upstream request into a Range/If-Range fetch of the remainder.
	var plan *forwardPlan
	if s.pcache != nil {
		plan = s.pcache.Plan(r)
	}

	resp, err := s.retry.Do(r.Context(), retry.DefaultClassifier, func(_ int) (*http.Response, error) {
		outReq := r.Clone(r.Context())
		outReq.RequestURI = ""
		stripHopByHop(outReq.Header)
		if plan != nil && plan.resumeFrom > 0 {
			outReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", plan.resumeFrom))
			outReq.Header.Set("If-Range", plan.validator)
		}
		return s.client.Do(outReq)
	})
	if err != nil {
		s.logger.Warn("forward failed", "url", r.URL.String(), "err", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Record the upstream size so a later library hit can be size-checked.
	if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		if store, ok := s.res.(expectedSizeSetter); ok {
			if name := basenameFromURL(r.URL); name != "" {
				store.SetExpectedSize(name, resp.ContentLength)
			}
		}
	}

	body := io.Reader(resp.Body)
	status := resp.StatusCode
	var finishPartial func(error)
	if plan != nil {
		res := s.pcache.Begin(plan, r, resp)
		if res.err != nil {
			s.logger.Warn("partial cache: resume aborted", "url", r.URL.String(), "err", res.err)
			http.Error(w, "upstream error: stale partial; retry", http.StatusBadGateway)
			return
		}
		if res.ok {
			body = res.reader
			finishPartial = res.done
			status = res.status
			if res.contentLength > 0 {
				// Synthesised full response from a resumed 206: present a 200
				// with the whole-file length, never a Content-Range.
				resp.Header.Del("Content-Range")
				resp.Header.Set("Content-Length", strconv.FormatInt(res.contentLength, 10))
				resp.Header.Set("Accept-Ranges", "bytes")
			}
		}
	}

	stripHopByHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}

	_, copyErr := io.Copy(w, body)
	if finishPartial != nil {
		finishPartial(copyErr)
	}
}

func stripHopByHop(h http.Header) {
	// Anything listed in Connection: ... is also hop-by-hop.
	for _, c := range h.Values("Connection") {
		for _, name := range strings.Split(c, ",") {
			if n := strings.TrimSpace(name); n != "" {
				h.Del(n)
			}
		}
	}
	for _, key := range hopByHopHeaders {
		h.Del(key)
	}
}
