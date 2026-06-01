package proxy

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
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
//   classify → publish capture event → library hit? serve : forward (per mode)
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
		s.logger.Info("library hit", "url", r.URL.String(), "kind", kind, "path", path)
		s.serve.ServeFile(w, r, path)
		return
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
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	stripHopByHop(outReq.Header)

	resp, err := s.client.Do(outReq)
	if err != nil {
		s.logger.Warn("forward failed", "url", r.URL.String(), "err", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	stripHopByHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
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
