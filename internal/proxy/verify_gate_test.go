package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
)

// gateResolver resolves every URL to a fixed local path and reports a fixed
// verification state, so we can exercise libraryServeOK in isolation.
type gateResolver struct {
	path  string
	state library.VerifyState
	sizes map[string]int64
}

func (g *gateResolver) Resolve(*url.URL) (string, bool)          { return g.path, true }
func (g *gateResolver) VerifyStateOf(string) library.VerifyState { return g.state }
func (g *gateResolver) ExpectedSize(b string) (int64, bool)      { n, ok := g.sizes[b]; return n, ok }

func makeGateProxy(t *testing.T, res *gateResolver, upHost string, requireSize bool) *httptest.Server {
	t.Helper()
	cfg := defaultCfg()
	cfg.Verify.RequireSizeMatch = requireSize

	ruleDir := t.TempDir()
	body := fmt.Sprintf("platform: test\nrules:\n  - kind: pkg-app\n    host_suffix: %s\n    path_regex: \".*\"\n", upHost)
	if err := os.WriteFile(filepath.Join(ruleDir, "r.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, err := match.LoadOverride(ruleDir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{Config: cfg, Rules: rules, Resolver: res, Serve: serve.New(nil), Bus: capture.NewBus(8)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyFailedFileIsForwardedNotServed(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "game.pkg")
	if err := os.WriteFile(local, []byte("CORRUPT LOCAL BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}

	var upstreamHits atomic.Int32
	const upstreamBody = "fresh correct bytes from upstream"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	res := &gateResolver{path: local, state: library.VerifyFailed}
	proxySrv := makeGateProxy(t, res, mustHost(t, upstream.URL), false)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/game.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != upstreamBody {
		t.Errorf("expected upstream body (corrupt local file must not be served), got %q", got)
	}
	if upstreamHits.Load() == 0 {
		t.Errorf("upstream was never hit; corrupt local file was wrongly served")
	}
}

func TestVerifyOKFileIsServedLocally(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "ok.pkg")
	const localBody = "good local bytes"
	if err := os.WriteFile(local, []byte(localBody), 0o644); err != nil {
		t.Fatal(err)
	}

	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		_, _ = io.WriteString(w, "should not be reached")
	}))
	defer upstream.Close()

	res := &gateResolver{path: local, state: library.VerifyOK}
	proxySrv := makeGateProxy(t, res, mustHost(t, upstream.URL), false)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/ok.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != localBody {
		t.Errorf("expected local body, got %q", got)
	}
	if upstreamHits.Load() != 0 {
		t.Errorf("upstream should not be hit for a verified local file")
	}
}

func TestRequireSizeMatchForwardsOnMismatch(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "size.pkg")
	if err := os.WriteFile(local, []byte("only 11 chars"), 0o644); err != nil { // 13 bytes
		t.Fatal(err)
	}

	var upstreamHits atomic.Int32
	const upstreamBody = "the upstream is bigger than the local file by a lot of bytes"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	// Expected size disagrees with the on-disk file → must forward.
	res := &gateResolver{path: local, state: library.VerifyUnchecked, sizes: map[string]int64{"size.pkg": 9999}}
	proxySrv := makeGateProxy(t, res, mustHost(t, upstream.URL), true)
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/size.pkg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != upstreamBody {
		t.Errorf("size mismatch should forward; got %q", got)
	}
	if upstreamHits.Load() == 0 {
		t.Errorf("upstream was never hit despite size mismatch")
	}
}
