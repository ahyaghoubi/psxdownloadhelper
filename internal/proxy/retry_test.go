package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestForwardRetriesTransient5xx ensures the retry policy kicks in when the
// upstream answers 502/503 and that the proxy returns the eventual 200.
func TestForwardRetriesTransient5xx(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) <= 2 {
			http.Error(w, "transient", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("hello after retries"))
	}))
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.Forward.Retry.MaxAttempts = 4
	cfg.Forward.Retry.InitialBackoffMs = 5
	cfg.Forward.Retry.MaxBackoffMs = 20
	cfg.Forward.Retry.Multiplier = 2.0
	cfg.Forward.Retry.Jitter = 0

	_, proxySrv := makeProxy(t, cfg, stubResolver{})
	client := proxiedClient(t, proxySrv)

	resp, err := client.Get(upstream.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "hello after retries" {
		t.Errorf("body = %q", body)
	}
	if hits.Load() != 3 {
		t.Errorf("upstream hits = %d, want 3 (2 failures + 1 success)", hits.Load())
	}
}

// TestForwardNoRetryWhenDisabled keeps the default MaxAttempts=1 behaviour.
func TestForwardNoRetryWhenDisabled(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer upstream.Close()

	_, proxySrv := makeProxy(t, defaultCfg(), stubResolver{})
	client := proxiedClient(t, proxySrv)
	resp, err := client.Get(upstream.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hits = %d, want 1 (no retry by default)", hits.Load())
	}
}

// TestForwardDoesNotRetry404 verifies non-transient 4xx bypasses the retry.
func TestForwardDoesNotRetry404(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.NotFound(w, nil)
	}))
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.Forward.Retry.MaxAttempts = 5
	cfg.Forward.Retry.InitialBackoffMs = 5
	cfg.Forward.Retry.MaxBackoffMs = 20
	_, proxySrv := makeProxy(t, cfg, stubResolver{})
	client := proxiedClient(t, proxySrv)
	resp, err := client.Get(upstream.URL + "/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("4xx should not be retried; hits = %d", hits.Load())
	}
}

// TestForwardRetryRespectsClientCancel guarantees the retry loop bails out
// when the console cancels the request.
func TestForwardRetryRespectsClientCancel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "always 502", http.StatusBadGateway)
	}))
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.Forward.Retry.MaxAttempts = 100
	cfg.Forward.Retry.InitialBackoffMs = 500
	cfg.Forward.Retry.MaxBackoffMs = 1000
	cfg.Forward.Retry.Jitter = 0
	_, proxySrv := makeProxy(t, cfg, stubResolver{})
	client := proxiedClient(t, proxySrv)
	client.Timeout = 100 * time.Millisecond

	start := time.Now()
	_, err := client.Get(upstream.URL + "/timeout")
	if err == nil {
		t.Fatal("expected client-side timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("retry loop did not honour client cancel (took %v)", time.Since(start))
	}
}
