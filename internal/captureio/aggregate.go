// Package captureio aggregates capture events into per-title part lists, so
// the dashboard, export CLI, and import handler all share one canonical view
// of "which parts have we seen for this title". The aggregation rules mirror
// session.Store.Record: dedupe by basename, last write wins.
package captureio

import (
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

// Part is one (basename, URL, kind, part_index) tuple aggregated from one or
// more capture events.
type Part struct {
	Title     string
	Basename  string
	URL       *url.URL
	Kind      match.Kind
	PartIndex int
}

// AggregateByTitle groups pushable capture events by TitleHint (or "unknown")
// and dedupes parts by basename. Returned slices are sorted by part_index, then
// basename for determinism. Non-pushable events (manifests, CRC, ignore) are
// dropped — the export and cluster paths only care about PKG payloads.
func AggregateByTitle(events []capture.Event) map[string][]Part {
	type key struct{ title, basename string }
	merged := make(map[key]Part, len(events))
	for _, ev := range events {
		if ev.URL == nil || !match.IsPushableKind(ev.Kind) {
			continue
		}
		base := basenameOf(ev.URL)
		if base == "" {
			continue
		}
		title := ev.Hint.TitleHint
		if title == "" {
			title = "unknown"
		}
		merged[key{title, base}] = Part{
			Title:     title,
			Basename:  base,
			URL:       ev.URL,
			Kind:      ev.Kind,
			PartIndex: ev.Hint.PartIndex,
		}
	}
	out := make(map[string][]Part, 8)
	for k, p := range merged {
		out[k.title] = append(out[k.title], p)
	}
	for _, parts := range out {
		sort.Slice(parts, func(i, j int) bool {
			if parts[i].PartIndex != parts[j].PartIndex {
				return parts[i].PartIndex < parts[j].PartIndex
			}
			return parts[i].Basename < parts[j].Basename
		})
	}
	return out
}

// PickEnumerateSeed returns the URL most suitable as a starting point for
// cluster.Enumerate: prefer the lowest-indexed pkg-app / pkg-base part (the
// canonical _0.pkg form), then any other pushable PKG. Returns nil for an
// empty slice.
func PickEnumerateSeed(parts []Part) *url.URL {
	var primary, fallback *url.URL
	for i := range parts {
		p := parts[i]
		if p.URL == nil {
			continue
		}
		if isPrimary(p.Kind) {
			if primary == nil || (p.PartIndex >= 0 && betterIndex(p, primary, parts)) {
				primary = p.URL
			}
		} else if fallback == nil {
			fallback = p.URL
		}
	}
	if primary != nil {
		return primary
	}
	return fallback
}

// URLsForExport flattens parts (optionally filtered by title) into the
// stringified URL list that export.WriteTxt / export.WriteAria2 consume. Parts
// are sorted by title then part index.
func URLsForExport(byTitle map[string][]Part, titleFilter string) []string {
	titles := make([]string, 0, len(byTitle))
	for t := range byTitle {
		if titleFilter != "" && t != titleFilter {
			continue
		}
		titles = append(titles, t)
	}
	sort.Strings(titles)
	var out []string
	for _, t := range titles {
		parts := append([]Part(nil), byTitle[t]...)
		sort.Slice(parts, func(i, j int) bool {
			if parts[i].PartIndex != parts[j].PartIndex {
				return parts[i].PartIndex < parts[j].PartIndex
			}
			return parts[i].Basename < parts[j].Basename
		})
		for _, p := range parts {
			if p.URL == nil {
				continue
			}
			out = append(out, p.URL.String())
		}
	}
	return out
}

func basenameOf(u *url.URL) string {
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return ""
	}
	name := path.Base(p)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}

func isPrimary(k match.Kind) bool {
	return k == match.KindPKGApp || k == match.KindPKGBase
}

// betterIndex returns true when p has a strictly lower PartIndex than the
// current primary URL. We re-scan parts to find the existing primary's index;
// the slice is small so the cost is irrelevant.
func betterIndex(p Part, current *url.URL, parts []Part) bool {
	if p.PartIndex < 0 {
		return false
	}
	curIdx := -1
	for _, q := range parts {
		if q.URL == current && isPrimary(q.Kind) {
			curIdx = q.PartIndex
			break
		}
	}
	return curIdx < 0 || p.PartIndex < curIdx
}
