// Package parts finds every part of a multi-part PKG from one known part.
//
// Big games are split into numbered files (…_0.pkg, …_1.pkg, …_51.pkg) that
// share one URL apart from the number. The console asks for them one at a
// time, so without help the user would have to keep the console connected
// for the whole download. Given one part's URL, Discover asks Sony which
// other numbers exist and how big they are, so every link can be shown at
// once.
package parts

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxParts bounds the search in case a CDN answers every number.
const MaxParts = 512

var partRe = regexp.MustCompile(`_(\d+)\.pkg$`)

// Index returns the part number in u's file name, or -1.
func Index(u *url.URL) int {
	m := partRe.FindStringSubmatch(u.Path)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// WithIndex returns a copy of u pointing at part i. The query string (which
// may carry an access token) is kept.
func WithIndex(u *url.URL, i int) *url.URL {
	c := *u
	loc := partRe.FindStringSubmatchIndex(c.Path)
	if loc == nil {
		return &c
	}
	c.Path = c.Path[:loc[2]] + strconv.Itoa(i) + c.Path[loc[3]:]
	c.RawPath = ""
	return &c
}

// SeriesKey identifies all parts of one package: the URL with the number
// blanked out.
func SeriesKey(u *url.URL) string {
	return u.Host + partRe.ReplaceAllString(u.Path, "_#.pkg")
}

// Found is one part that exists on the CDN.
type Found struct {
	URL   *url.URL
	Index int
	Size  int64
}

// Finder asks the CDN which parts exist.
type Finder struct {
	Transport   http.RoundTripper
	Concurrency int           // parallel requests, default 4
	Timeout     time.Duration // per request, default 20s
}

// Discover reports every part of seed's series via found, except the indices
// known already reports true for. It returns when the search is complete.
// header carries request headers to copy (such as the console's User-Agent).
func (f *Finder) Discover(ctx context.Context, seed *url.URL, header http.Header, known func(int) bool, found func(Found)) {
	start := Index(seed)
	if start < 0 {
		return
	}
	workers := f.Concurrency
	if workers <= 0 {
		workers = 4
	}

	// Below the known part every number should exist: check them in parallel.
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i := 0; i < start; i++ {
		if known(i) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			u := WithIndex(seed, i)
			if size, ok := f.probe(ctx, u, header); ok {
				found(Found{URL: u, Index: i, Size: size})
			}
		}(i)
	}
	wg.Wait()

	// Above it, count up until the first number that doesn't exist.
	for i := start + 1; i < MaxParts && ctx.Err() == nil; i++ {
		if known(i) {
			continue
		}
		u := WithIndex(seed, i)
		size, ok := f.probe(ctx, u, header)
		if !ok {
			return
		}
		found(Found{URL: u, Index: i, Size: size})
	}
}

// probe asks for the first byte of u and reports whether it exists and its
// full size. A one-byte range request works on CDNs that refuse HEAD.
func (f *Finder) probe(ctx context.Context, u *url.URL, header http.Header) (int64, bool) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, false
	}
	for _, k := range []string{"User-Agent", "Accept", "Accept-Language"} {
		if v := header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Range", "bytes=0-0")
	rt := f.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
		cr := resp.Header.Get("Content-Range")
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				return n, true
			}
		}
		return 0, true
	case http.StatusOK:
		// Range ignored: the size is the whole body. Closing the body stops
		// the transfer.
		return resp.ContentLength, true
	default:
		return 0, false
	}
}

var packageSuffix = regexp.MustCompile(`(?i)(_\d+|_sc|-DP)?\.pkg$`)

// PackageName returns the name shared by every file of one package: the
// file name without its part number, `_sc` or `-DP` suffix and extension.
// "EP1004-PPSA01547_00-GTA_51.pkg" and "EP1004-PPSA01547_00-GTA_sc.pkg"
// both belong to "EP1004-PPSA01547_00-GTA".
func PackageName(fileName string) string {
	return packageSuffix.ReplaceAllString(fileName, "")
}
