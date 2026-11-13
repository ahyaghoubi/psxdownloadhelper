package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
)

// partialMeta is the sidecar written next to every `.partial` file. It records
// upstream validators and completed byte spans so a `.partial` left behind by a
// dropped forward can be safely resumed on a later run: we only continue when
// the upstream object is provably unchanged (matching ETag / Last-Modified and
// the same total Content-Length). On any mismatch we fall back to a fresh
// download rather than risk stitching bytes from two different objects.
type partialMeta struct {
	URL           string `json:"url"`
	Basename      string `json:"basename"`
	ContentLength int64  `json:"content_length"`
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"last_modified,omitempty"`
	Spans         []span `json:"spans,omitempty"`
}

// span records a half-open byte range [Start, End) that is complete on disk.
type span struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// metaFromResponse captures the validators from an upstream response. total is
// the complete object size, not necessarily resp.ContentLength for 206 bodies.
func metaFromResponse(rawURL, basename string, resp *http.Response, total int64) partialMeta {
	return partialMeta{
		URL:           rawURL,
		Basename:      basename,
		ContentLength: total,
		ETag:          resp.Header.Get("ETag"),
		LastModified:  resp.Header.Get("Last-Modified"),
	}
}

// validator returns the value to send in an If-Range header when resuming:
// the strong/weak ETag if present, else the Last-Modified date. An empty
// string means we have no validator and must not attempt a resume.
func (m partialMeta) validator() string {
	if m.ETag != "" {
		return m.ETag
	}
	return m.LastModified
}

// compatible reports whether two metadata records can describe the same
// upstream object. Empty validators are treated as "unknown", not a conflict.
func (m partialMeta) compatible(next partialMeta) bool {
	if m.URL != "" && next.URL != "" && m.URL != next.URL {
		return false
	}
	if m.Basename != "" && next.Basename != "" && m.Basename != next.Basename {
		return false
	}
	if m.ContentLength > 0 && next.ContentLength > 0 && m.ContentLength != next.ContentLength {
		return false
	}
	if m.ETag != "" && next.ETag != "" && m.ETag != next.ETag {
		return false
	}
	if m.LastModified != "" && next.LastModified != "" && m.LastModified != next.LastModified {
		return false
	}
	return true
}

// normaliseSpans sorts spans, drops invalid ranges, clamps to total when known,
// and coalesces overlapping or adjacent ranges.
func normaliseSpans(spans []span, total int64) []span {
	if len(spans) == 0 {
		return nil
	}
	clean := make([]span, 0, len(spans))
	for _, s := range spans {
		if s.Start < 0 {
			s.Start = 0
		}
		if total > 0 && s.End > total {
			s.End = total
		}
		if s.End <= s.Start {
			continue
		}
		clean = append(clean, s)
	}
	if len(clean) == 0 {
		return nil
	}
	sort.Slice(clean, func(i, j int) bool {
		if clean[i].Start == clean[j].Start {
			return clean[i].End < clean[j].End
		}
		return clean[i].Start < clean[j].Start
	})
	out := clean[:1]
	for _, s := range clean[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func (m *partialMeta) addSpan(start, end int64) {
	m.Spans = normaliseSpans(append(m.Spans, span{Start: start, End: end}), m.ContentLength)
}

func (m partialMeta) complete() bool {
	return m.ContentLength > 0 &&
		len(m.Spans) == 1 &&
		m.Spans[0].Start == 0 &&
		m.Spans[0].End == m.ContentLength
}

func (m partialMeta) coveredPrefix() int64 {
	spans := normaliseSpans(m.Spans, m.ContentLength)
	if len(spans) == 0 || spans[0].Start != 0 {
		return 0
	}
	return spans[0].End
}

// writeMeta atomically writes m to path (temp file + rename) so a crash mid-write
// never leaves a half-written sidecar.
func writeMeta(path string, m partialMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("partial meta: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("partial meta: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("partial meta: rename %s: %w", path, err)
	}
	return nil
}

// readMeta loads a sidecar. A missing file returns (nil, nil) so callers can
// treat "no resume state" the same as "fresh download".
func readMeta(path string) (*partialMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("partial meta: read %s: %w", path, err)
	}
	var m partialMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("partial meta: parse %s: %w", path, err)
	}
	return &m, nil
}
