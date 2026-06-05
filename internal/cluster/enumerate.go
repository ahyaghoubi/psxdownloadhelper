// Package cluster implements psxdh's master/slave distributed download. The
// master (the node the PS5 proxies through) enumerates a game's parts, assigns
// them to slave nodes that download with an embedded downloader, then collects
// the finished parts back so it can serve them to the console. See ADR 0005.
package cluster

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// partSuffix splits a PKG/CRC basename into its prefix, numeric part index, and
// extension — e.g. "UP0002-PPSA01649_00-CODWZ23DLCPACK01_3.pkg" →
// ("UP0002-PPSA01649_00-CODWZ23DLCPACK01", "3", "pkg"). Mirrors the part regex
// in internal/match/classify.go.
var partSuffix = regexp.MustCompile(`^(.*)_(\d+)\.(pkg|crc)$`)

// DefaultMaxParts caps enumeration so a misbehaving probe can't loop forever.
const DefaultMaxParts = 4096

// PartURL is one enumerated part of a multi-part download.
type PartURL struct {
	Index    int    `json:"index"`
	URL      string `json:"url"`
	Basename string `json:"basename"`
	Size     int64  `json:"size"`
}

// Prober reports whether a candidate part URL exists upstream (and its size).
type Prober interface {
	// Exists returns (true, size) when rawURL resolves to a real object.
	Exists(ctx context.Context, rawURL string) (exists bool, size int64, err error)
}

// Enumerate derives every sibling part URL from a single captured part URL by
// substituting the trailing _N segment and probing index 0 upward until the
// first gap. The query string and the rest of the path (including any
// `f_<hash>` segment) are preserved verbatim — only the _N changes.
//
// It returns the contiguous run [0..N]. A seed whose part can't be parsed
// yields a single-element result (just the seed) so non-multipart assets still
// flow through unchanged.
func Enumerate(ctx context.Context, seed *url.URL, prober Prober, maxParts int) ([]PartURL, error) {
	if seed == nil {
		return nil, fmt.Errorf("cluster: nil seed URL")
	}
	if maxParts <= 0 {
		maxParts = DefaultMaxParts
	}
	base := path.Base(strings.TrimSuffix(seed.Path, "/"))
	m := partSuffix.FindStringSubmatch(base)
	if m == nil {
		// Not a recognisable multi-part name — treat as a lone asset.
		return []PartURL{{Index: 0, URL: seed.String(), Basename: base}}, nil
	}
	prefix, ext := m[1], m[3]
	dir := path.Dir(strings.TrimSuffix(seed.Path, "/"))

	var parts []PartURL
	for i := 0; i < maxParts; i++ {
		name := fmt.Sprintf("%s_%d.%s", prefix, i, ext)
		u := *seed // copy; preserves Scheme/Host/RawQuery
		u.Path = path.Join(dir, name)
		raw := u.String()
		ok, size, err := prober.Exists(ctx, raw)
		if err != nil {
			return parts, fmt.Errorf("cluster: probing part %d (%s): %w", i, name, err)
		}
		if !ok {
			break
		}
		parts = append(parts, PartURL{Index: i, URL: raw, Basename: name, Size: size})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("cluster: no parts found for %s", base)
	}
	return parts, nil
}

// indexOf parses the numeric part index from a basename, or -1.
func indexOf(basename string) int {
	m := partSuffix.FindStringSubmatch(basename)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return -1
	}
	return n
}
