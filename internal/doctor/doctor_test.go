package doctor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netresolve"
)

func TestDescribeResolversAppendsSystemFallback(t *testing.T) {
	rs := describeResolvers(config.DNSConfig{Mode: "doh", Resolvers: []string{"https://1.1.1.1/dns-query"}})
	if len(rs) < 2 {
		t.Fatalf("expected at least 2 resolvers, got %d", len(rs))
	}
	if rs[len(rs)-1].label != "system" {
		t.Errorf("last resolver = %q, want system", rs[len(rs)-1].label)
	}
}

func TestDescribeResolversRejectsHTTPInDoH(t *testing.T) {
	rs := describeResolvers(config.DNSConfig{Mode: "doh", Resolvers: []string{"1.1.1.1"}})
	if len(rs) < 1 {
		t.Fatal("expected at least one resolver entry")
	}
	if rs[0].err == "" {
		t.Errorf("expected error message for plain UDP entry in doh mode, got none")
	}
}

func TestDescribeResolversDefaultsToSystem(t *testing.T) {
	rs := describeResolvers(config.DNSConfig{})
	if len(rs) != 1 || rs[0].label != "system" {
		t.Errorf("empty mode should produce single system entry, got %v", rs)
	}
}

// TestCheckRunsAgainstFakeHostsSkipsHandshake ensures Check exercises every
// configured resolver without hitting the real network for the TLS leg.
func TestCheckRunsAgainstFakeHostsSkipsHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rep := Check(ctx, config.NetworkConfig{
		DNS: config.DNSConfig{Mode: "system"},
	}, CheckOptions{
		Hosts:         []string{"localhost"},
		SkipHandshake: true,
	})
	if len(rep.Resolvers) == 0 {
		t.Fatal("no resolver results")
	}
	if len(rep.Hosts) != 0 {
		t.Errorf("SkipHandshake should suppress host probes, got %d", len(rep.Hosts))
	}
}

func TestCheckAria2Found(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "aria2c")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckAria2(config.DownloaderConfig{Aria2Binary: bin})
	if !r.Found || r.Path != bin {
		t.Errorf("CheckAria2 = %+v, want found at %q", r, bin)
	}
}

func TestCheckAria2Missing(t *testing.T) {
	r := CheckAria2(config.DownloaderConfig{Aria2Binary: filepath.Join(t.TempDir(), "missing")})
	if r.Found {
		t.Fatalf("expected missing, got %+v", r)
	}
	if r.Hint == "" {
		t.Error("expected non-empty install hint")
	}
}

func TestRenderAria2Section(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, &Report{
		Aria2: Aria2Result{Found: true, Path: "/usr/bin/aria2c"},
	})
	if !strings.Contains(buf.String(), "Embedded downloader (aria2c)") {
		t.Fatalf("missing aria2 section: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "/usr/bin/aria2c") {
		t.Fatalf("missing path: %s", buf.String())
	}
	buf.Reset()
	Render(&buf, &Report{
		Aria2: Aria2Result{Found: false, Err: "not found", Hint: "brew install aria2"},
	})
	out := buf.String()
	for _, want := range []string{"FAIL", "brew install aria2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderProducesText(t *testing.T) {
	rep := &Report{
		Resolvers: []ResolverResult{
			{Label: "system", Lookups: []LookupResult{
				{Host: "example.com", IPs: []string{"1.2.3.4"}, Latency: 5 * time.Millisecond},
				{Host: "broken.example", Err: "timeout", Latency: time.Second},
			}},
			{Label: "doh-shecan", Err: "unreachable"},
		},
		Hosts: []HostResult{
			{Host: "example.com", TLSOK: true, Latency: 50 * time.Millisecond},
		},
		Aria2: Aria2Result{Found: true, Path: "/bin/aria2c"},
	}
	var buf bytes.Buffer
	Render(&buf, rep)
	out := buf.String()
	for _, want := range []string{"system", "doh-shecan", "example.com", "1.2.3.4", "broken.example", "FAIL", "unreachable", "Embedded downloader"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q: %s", want, out)
		}
	}
}

func TestProbeClassifiesAndIssuesHEAD(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "want HEAD", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Server", "psxdh-test")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "12345")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	rules, _ := match.LoadDefaults(true, true)
	res := netresolve.NewSystem()
	probe, err := Probe(context.Background(), upstream.URL+"/foo.pkg", rules, res, &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if probe.Status != http.StatusOK {
		t.Errorf("status = %d", probe.Status)
	}
	if probe.Server != "psxdh-test" {
		t.Errorf("server = %q", probe.Server)
	}
	if probe.AcceptRanges != "bytes" {
		t.Errorf("accept-ranges = %q", probe.AcceptRanges)
	}
	if probe.Method != http.MethodHead {
		t.Errorf("method = %q, want HEAD", probe.Method)
	}
}

func TestProbeFallsBackToGetOn405(t *testing.T) {
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	}))
	defer upstream.Close()

	rules, _ := match.LoadDefaults(true, true)
	res := netresolve.NewSystem()
	probe, err := Probe(context.Background(), upstream.URL+"/foo.pkg", rules, res, &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if probe.Method != http.MethodGet {
		t.Errorf("method = %q, want GET fallback", probe.Method)
	}
	if probe.Status != http.StatusPartialContent {
		t.Errorf("status = %d", probe.Status)
	}
	if len(seen) != 2 || seen[0] != http.MethodHead || seen[1] != http.MethodGet {
		t.Errorf("requests = %v, want [HEAD GET]", seen)
	}
}

func TestProbeRejectsRelativeURL(t *testing.T) {
	rules, _ := match.LoadDefaults(true, true)
	_, err := Probe(context.Background(), "/foo.pkg", rules, netresolve.NewSystem(), http.DefaultClient)
	if err == nil {
		t.Error("expected error for relative URL")
	}
}
