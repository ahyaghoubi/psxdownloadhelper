package parts

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndexAndWithIndex(t *testing.T) {
	u, _ := url.Parse("http://h/x/EP1004-PPSA01547_00-GTA_51.pkg?tok=a")
	if Index(u) != 51 {
		t.Fatalf("Index = %d", Index(u))
	}
	got := WithIndex(u, 7).String()
	if got != "http://h/x/EP1004-PPSA01547_00-GTA_7.pkg?tok=a" {
		t.Fatalf("WithIndex = %s", got)
	}
	if u.String() != "http://h/x/EP1004-PPSA01547_00-GTA_51.pkg?tok=a" {
		t.Fatal("WithIndex must not modify its input")
	}
	sc, _ := url.Parse("http://h/x/EP1004-PPSA01547_00-GTA_sc.pkg")
	if Index(sc) != -1 {
		t.Fatal("_sc.pkg has no part number")
	}
	if SeriesKey(u) != SeriesKey(WithIndex(u, 3)) {
		t.Fatal("parts of one game must share a series key")
	}
}

// cdn serves parts 0..n-1, each part i being 1000+i bytes. With rangeOK
// false it ignores Range like a CDN that only does full responses.
func cdn(t *testing.T, n int, rangeOK bool, wantUA string) (*httptest.Server, *atomic.Int64) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if wantUA != "" && r.UserAgent() != wantUA {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		i := Index(r.URL)
		if i < 0 || i >= n {
			http.NotFound(w, r)
			return
		}
		body := bytes.Repeat([]byte{'x'}, 1000+i)
		if !rangeOK {
			r.Header.Del("Range")
		}
		http.ServeContent(w, r, "p.pkg", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func collect(t *testing.T, seedURL string, known map[int]bool, hdr http.Header) map[int]int64 {
	t.Helper()
	seed, _ := url.Parse(seedURL)
	var mu sync.Mutex
	got := map[int]int64{}
	(&Finder{}).Discover(context.Background(), seed, hdr,
		func(i int) bool { return known[i] },
		func(f Found) {
			mu.Lock()
			defer mu.Unlock()
			if Index(f.URL) != f.Index || !strings.HasSuffix(f.URL.RawQuery, "tok=1") {
				t.Errorf("bad URL for part %d: %s", f.Index, f.URL)
			}
			got[f.Index] = f.Size
		})
	return got
}

func TestDiscoverFindsAllPartsAroundSeed(t *testing.T) {
	srv, _ := cdn(t, 6, true, "")
	got := collect(t, srv.URL+"/g/GAME_3.pkg?tok=1", map[int]bool{3: true}, nil)
	keys := make([]int, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	if len(keys) != 5 || keys[0] != 0 || keys[4] != 5 {
		t.Fatalf("found parts %v, want 0,1,2,4,5", keys)
	}
	for i, size := range got {
		if size != int64(1000+i) {
			t.Errorf("part %d size %d", i, size)
		}
	}
}

func TestDiscoverSkipsKnownParts(t *testing.T) {
	srv, hits := cdn(t, 3, true, "")
	got := collect(t, srv.URL+"/g/GAME_0.pkg?tok=1", map[int]bool{0: true, 1: true}, nil)
	if len(got) != 1 || got[2] != 1002 {
		t.Fatalf("got %v", got)
	}
	if hits.Load() != 2 { // part 2, then the miss at 3
		t.Errorf("CDN hit %d times", hits.Load())
	}
}

func TestDiscoverWhenRangeIgnored(t *testing.T) {
	srv, _ := cdn(t, 2, false, "")
	got := collect(t, srv.URL+"/g/GAME_0.pkg?tok=1", map[int]bool{0: true}, nil)
	if got[1] != 1001 {
		t.Fatalf("got %v", got)
	}
}

func TestDiscoverSendsConsoleUserAgent(t *testing.T) {
	srv, _ := cdn(t, 2, true, "libhttp/9.00 (PlayStation 5)")
	hdr := http.Header{"User-Agent": {"libhttp/9.00 (PlayStation 5)"}}
	got := collect(t, srv.URL+"/g/GAME_0.pkg?tok=1", map[int]bool{0: true}, hdr)
	if got[1] != 1001 {
		t.Fatalf("console User-Agent not forwarded: %v", got)
	}
}

func TestDiscoverIgnoresFilesWithoutNumber(t *testing.T) {
	srv, hits := cdn(t, 2, true, "")
	collect(t, srv.URL+"/g/GAME_sc.pkg?tok=1", nil, nil)
	if hits.Load() != 0 {
		t.Fatal("no probing for un-numbered files")
	}
}
