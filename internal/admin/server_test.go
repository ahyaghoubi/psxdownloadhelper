package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

func newTestServer(t *testing.T, token string) (*httptest.Server, capture.Bus) {
	t.Helper()
	cfg := config.Default()
	bus := capture.NewBus(8)
	s, err := New(Deps{
		Config:   cfg,
		Token:    token,
		Version:  "test",
		Bus:      bus,
		Sessions: session.New(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, bus
}

func TestTokenRequired(t *testing.T) {
	srv, _ := newTestServer(t, "secret-token")

	// No token → 401.
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", resp.StatusCode)
	}

	// Query-param token → 200.
	resp, err = http.Get(srv.URL + "/api/status?token=secret-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("query token: got %d, want 200", resp.StatusCode)
	}

	// Header token → 200.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
	req.Header.Set("X-Psxdh-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("header token: got %d, want 200", resp.StatusCode)
	}

	// Wrong token → 401.
	resp, err = http.Get(srv.URL + "/api/status?token=nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", resp.StatusCode)
	}
}

func TestNoTokenNoAuth(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("loopback no-token: got %d, want 200", resp.StatusCode)
	}
}

func TestStatusIncludesLANIPs(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["lan_ips"]; !ok {
		t.Fatalf("status missing lan_ips: %+v", body)
	}
	if body["proxy_port"] != "8080" {
		t.Errorf("proxy_port = %v, want 8080", body["proxy_port"])
	}
}

func TestSessionsEndpoint(t *testing.T) {
	cfg := config.Default()
	bus := capture.NewBus(8)
	store := session.New(nil)
	s, _ := New(Deps{Config: cfg, Bus: bus, Sessions: store, Version: "t"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	u, _ := url.Parse("http://cdn.example/cusa/CUSA12345_0.pkg")
	store.Record(capture.Event{URL: u, Method: "GET", Kind: match.KindPKGApp, Hint: match.Hint{TitleHint: "CUSA12345", PartIndex: 0}, Time: time.Now()})

	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sessions []session.Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "CUSA12345" {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}
	if sessions[0].Parts[0].Basename != "CUSA12345_0.pkg" {
		t.Errorf("part basename = %q", sessions[0].Parts[0].Basename)
	}
}

func TestAria2DisabledReturns503(t *testing.T) {
	srv, _ := newTestServer(t, "")
	resp, err := http.Post(srv.URL+"/api/handoff/aria2", "application/json", strings.NewReader(`{"url":"http://x/y.pkg"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("aria2 disabled: got %d, want 503", resp.StatusCode)
	}
}

func TestEventsSSE(t *testing.T) {
	srv, bus := newTestServer(t, "")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	u, _ := url.Parse("http://cdn.example/CUSA999_0.pkg")
	// Publish after a beat so the subscription is live.
	go func() {
		time.Sleep(50 * time.Millisecond)
		bus.Publish(capture.Event{URL: u, Method: "GET", Kind: match.KindPKGApp, Time: time.Now()})
	}()

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()
	select {
	case got := <-done:
		if !strings.Contains(got, "CUSA999_0.pkg") {
			t.Errorf("SSE payload missing URL: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event received")
	}
}
