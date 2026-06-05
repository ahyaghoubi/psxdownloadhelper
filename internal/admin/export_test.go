package admin

import (
	"io"
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

func TestExportEndpointAria2(t *testing.T) {
	cfg := config.Default()
	cfg.Library.Dir = "/tmp/lib"
	store := session.New(nil)
	for _, seed := range []struct {
		raw   string
		title string
		idx   int
		kind  match.Kind
	}{
		{"http://cdn.example/PPSA01_0.pkg", "PPSA01", 0, match.KindPKGApp},
		{"http://cdn.example/PPSA01_1.pkg", "PPSA01", 1, match.KindPKGApp},
		{"http://cdn.example/manifest.json", "PPSA01", -1, match.KindManifestJSON},
	} {
		u, _ := url.Parse(seed.raw)
		store.Record(capture.Event{URL: u, Method: "GET", Kind: seed.kind, Hint: match.Hint{TitleHint: seed.title, PartIndex: seed.idx}, Time: time.Now()})
	}

	s, err := New(Deps{Config: cfg, Bus: capture.NewBus(8), Sessions: store, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/export?format=aria2")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	got := string(body)
	for _, want := range []string{
		"http://cdn.example/PPSA01_0.pkg",
		"http://cdn.example/PPSA01_1.pkg",
		"out=PPSA01_0.pkg",
		"dir=/tmp/lib",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in body:\n%s", want, got)
		}
	}
	if strings.Contains(got, "manifest.json") {
		t.Errorf("manifest leaked into export: %s", got)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("missing attachment disposition: %q", cd)
	}
}

func TestExportEndpointTitleFilter(t *testing.T) {
	cfg := config.Default()
	store := session.New(nil)
	for _, seed := range []struct {
		raw   string
		title string
		kind  match.Kind
	}{
		{"http://cdn.example/PPSA01_0.pkg", "PPSA01", match.KindPKGApp},
		{"http://cdn.example/CUSA02_0.pkg", "CUSA02", match.KindPKGApp},
	} {
		u, _ := url.Parse(seed.raw)
		store.Record(capture.Event{URL: u, Method: "GET", Kind: seed.kind, Hint: match.Hint{TitleHint: seed.title, PartIndex: 0}, Time: time.Now()})
	}

	s, _ := New(Deps{Config: cfg, Bus: capture.NewBus(8), Sessions: store, Version: "t"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/export?format=txt&title=PPSA01")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	got := string(body)
	if strings.Contains(got, "CUSA02") {
		t.Errorf("title filter should exclude CUSA02:\n%s", got)
	}
	if !strings.Contains(got, "PPSA01_0.pkg") {
		t.Errorf("PPSA01 missing:\n%s", got)
	}
}

func TestExportEndpointEmpty404(t *testing.T) {
	cfg := config.Default()
	store := session.New(nil)
	s, _ := New(Deps{Config: cfg, Bus: capture.NewBus(8), Sessions: store, Version: "t"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/export")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("empty: got %d, want 404", resp.StatusCode)
	}
}
