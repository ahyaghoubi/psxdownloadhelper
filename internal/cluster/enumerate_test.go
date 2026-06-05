package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeProber reports parts 0..max-1 as existing.
type fakeProber struct{ max int }

func (f fakeProber) Exists(_ context.Context, rawURL string) (bool, int64, error) {
	idx := indexOf(lastSeg(rawURL))
	if idx >= 0 && idx < f.max {
		return true, int64(1000 + idx), nil
	}
	return false, 0, nil
}

func lastSeg(rawURL string) string {
	u, _ := url.Parse(rawURL)
	p := strings.TrimSuffix(u.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

const seedURL = "http://gst.prod.dl.playstation.net/gst/prod/00/PPSA01649_00/ac/UP0002-PPSA01649_00-CODWZ23DLCPACK01/pkg/239/f_35f85a6ab20e22812053265db5ed3d3aa187ddfb4a974a58e42be92669dc52dd/UP0002-PPSA01649_00-CODWZ23DLCPACK01_0.pkg?product=0283&serverIpAddr=192.168.2.32&r=0000001f"

func TestEnumerateDerivesAllParts(t *testing.T) {
	seed, _ := url.Parse(seedURL)
	parts, err := Enumerate(context.Background(), seed, fakeProber{max: 11}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 11 {
		t.Fatalf("got %d parts, want 11", len(parts))
	}
	if parts[0].Basename != "UP0002-PPSA01649_00-CODWZ23DLCPACK01_0.pkg" {
		t.Errorf("part 0 basename = %q", parts[0].Basename)
	}
	if parts[10].Basename != "UP0002-PPSA01649_00-CODWZ23DLCPACK01_10.pkg" {
		t.Errorf("part 10 basename = %q", parts[10].Basename)
	}
	// Query string + f_<hash> path segment preserved.
	if !strings.Contains(parts[5].URL, "product=0283&serverIpAddr=192.168.2.32&r=0000001f") {
		t.Errorf("query not preserved: %s", parts[5].URL)
	}
	if !strings.Contains(parts[5].URL, "/f_35f85a6ab20e22812053265db5ed3d3aa187ddfb4a974a58e42be92669dc52dd/") {
		t.Errorf("hash path segment not preserved: %s", parts[5].URL)
	}
	if parts[3].Size != 1003 {
		t.Errorf("part 3 size = %d, want 1003", parts[3].Size)
	}
}

func TestEnumerateSeedNotFirstPart(t *testing.T) {
	// Seed is _3.pkg; enumeration still starts from 0.
	seed, _ := url.Parse(strings.Replace(seedURL, "_0.pkg", "_3.pkg", 1))
	parts, err := Enumerate(context.Background(), seed, fakeProber{max: 5}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 5 || parts[0].Index != 0 {
		t.Fatalf("expected 5 parts starting at 0, got %d", len(parts))
	}
}

func TestEnumerateNonMultipart(t *testing.T) {
	seed, _ := url.Parse("http://host/path/version.xml?x=1")
	parts, err := Enumerate(context.Background(), seed, fakeProber{max: 0}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Basename != "version.xml" {
		t.Fatalf("non-multipart should yield the lone asset, got %+v", parts)
	}
}

func TestHTTPProber(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only _0 and _1 exist.
		if strings.Contains(r.URL.Path, "_0.pkg") || strings.Contains(r.URL.Path, "_1.pkg") {
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "2048")
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	seed, _ := url.Parse(srv.URL + "/cdn/GAME_0.pkg?sig=x")
	parts, err := Enumerate(context.Background(), seed, NewHTTPProber(srv.Client()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if parts[0].Size != 2048 {
		t.Errorf("size = %d, want 2048", parts[0].Size)
	}
}
