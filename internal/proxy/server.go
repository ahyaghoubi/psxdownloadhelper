// Package proxy is the HTTP proxy core. It accepts absolute-URI GET/HEAD
// requests from a console pointed at its listen address, classifies the
// target URL via match, and either serves a local file from the library
// or forwards the request upstream — preserving headers, query string,
// and Range semantics end-to-end. CONNECT requests are tunnelled without
// MITM. See docs/architecture.md (Request handling pipeline).
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/lifecycle"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/retry"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
)

// Deps bundles the collaborators the proxy needs. Keeping it as a struct
// makes test wiring easier than a long constructor signature.
type Deps struct {
	Config   *config.Config
	Rules    *match.RuleSet
	Resolver library.Resolver
	Serve    *serve.Handler
	Bus      capture.Bus
	Logger   *slog.Logger
	// UpstreamClient is optional; New supplies a sane default when nil.
	UpstreamClient *http.Client
}

// Server is the stdlib-net/http-based proxy. ADR 0001 records the choice;
// alternative implementations would conform to the same Handler shape so
// cmd/psxdh can swap them with a constructor change.
type Server struct {
	cfg    *config.Config
	rules  *match.RuleSet
	res    library.Resolver
	serve  *serve.Handler
	bus    capture.Bus
	logger *slog.Logger
	client *http.Client
	retry  *retry.Policy
	pcache *partialCache
	httpd  *http.Server
}

// New constructs a Server from Deps. Any nil-able field gets a safe default.
func New(d Deps) (*Server, error) {
	if d.Config == nil {
		return nil, errors.New("proxy: nil config")
	}
	if d.Rules == nil {
		return nil, errors.New("proxy: nil rules")
	}
	if d.Resolver == nil {
		return nil, errors.New("proxy: nil resolver")
	}
	if d.Serve == nil {
		return nil, errors.New("proxy: nil serve handler")
	}
	if d.Bus == nil {
		return nil, errors.New("proxy: nil capture bus")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := d.UpstreamClient
	if client == nil {
		client = defaultUpstreamClient()
	}
	retryPolicy := &retry.Policy{
		MaxAttempts:    d.Config.Forward.Retry.MaxAttempts,
		InitialBackoff: d.Config.Forward.Retry.InitialBackoff(),
		MaxBackoff:     d.Config.Forward.Retry.MaxBackoff(),
		Multiplier:     d.Config.Forward.Retry.Multiplier,
		Jitter:         d.Config.Forward.Retry.Jitter,
	}
	var pcache *partialCache
	if d.Config.Forward.PartialCache.Enabled {
		pcache = newPartialCache(
			d.Config.Library.Dir,
			d.Config.Forward.PartialCache.MinSizeBytes,
			d.Config.Forward.PartialCache.Resume,
			logger,
		)
	}
	return &Server{
		cfg:    d.Config,
		rules:  d.Rules,
		res:    d.Resolver,
		serve:  d.Serve,
		bus:    d.Bus,
		logger: logger,
		client: client,
		retry:  retryPolicy,
		pcache: pcache,
	}, nil
}

// Handler returns the http.Handler the proxy serves. Useful for httptest
// and for embedding the proxy under a different ListenAndServe.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

// ListenAndServe binds Config.Proxy.Listen and serves until ctx is canceled.
// On cancellation, in-flight requests drain for up to lifecycle.DefaultTimeout.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.httpd = &http.Server{
		Addr:    s.cfg.Proxy.Listen,
		Handler: s.Handler(),
		// No idle timeouts: large PKG downloads can take hours.
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("proxy listening", "addr", s.cfg.Proxy.Listen)
		err := s.httpd.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		lifecycle.ShutdownHTTP(s.logger, "proxy", s.httpd)
		return nil
	case err := <-errCh:
		return err
	}
}

func defaultUpstreamClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			// Stream large bodies; do not buffer.
			DisableCompression: true,
		},
		// 30x must reach the console so it can re-resolve. Don't follow.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		// No client-level timeout: PKG transfers can be hours.
	}
}

// verificationStore is the optional integrity surface a resolver may expose.
// library.Index implements it; tests may stub it.
type verificationStore interface {
	VerifyStateOf(path string) library.VerifyState
	ExpectedSize(basename string) (int64, bool)
}

// expectedSizeSetter lets the forward path record observed upstream sizes.
type expectedSizeSetter interface {
	SetExpectedSize(basename string, size int64)
}

// libraryServeOK reports whether a resolved local file is safe to serve. It is
// false when the file is known-corrupt (a `.crc` verification failed) or, when
// verify.require_size_match is set, when its on-disk size disagrees with the
// upstream Content-Length we recorded for the same basename.
func (s *Server) libraryServeOK(path string, u *url.URL) bool {
	store, ok := s.res.(verificationStore)
	if !ok {
		return true
	}
	if store.VerifyStateOf(path) == library.VerifyFailed {
		return false
	}
	if s.cfg.Verify.RequireSizeMatch {
		if want, has := store.ExpectedSize(basenameFromURL(u)); has {
			if fi, err := os.Stat(path); err == nil && fi.Size() != want {
				return false
			}
		}
	}
	return true
}

// handle dispatches GET/HEAD vs CONNECT. Anything else gets 405.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodConnect:
		s.handleCONNECT(w, r)
	case http.MethodGet, http.MethodHead:
		s.handleHTTP(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, CONNECT")
		http.Error(w, fmt.Sprintf("method %s not allowed", r.Method), http.StatusMethodNotAllowed)
	}
}
