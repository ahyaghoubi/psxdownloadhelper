package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// partialMeta is the sidecar written next to every `.partial` file. It records
// the upstream validators so a `.partial` left behind by a dropped forward can
// be safely resumed on a later run: we only continue appending when the
// upstream object is provably unchanged (matching ETag / Last-Modified and the
// same total Content-Length). On any mismatch we fall back to a fresh download
// rather than risk stitching bytes from two different objects.
type partialMeta struct {
	URL           string `json:"url"`
	Basename      string `json:"basename"`
	ContentLength int64  `json:"content_length"`
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"last_modified,omitempty"`
}

// metaFromResponse captures the validators from a fresh 200 OK response.
func metaFromResponse(rawURL, basename string, resp *http.Response) partialMeta {
	return partialMeta{
		URL:           rawURL,
		Basename:      basename,
		ContentLength: resp.ContentLength,
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
