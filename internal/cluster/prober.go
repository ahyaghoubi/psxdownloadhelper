package cluster

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// httpProber checks part existence with a HEAD, falling back to GET Range:0-0
// for CDNs that reject HEAD (mirrors internal/doctor.Probe). It uses the
// master's upstream client so DNS/proxy/retry resilience applies.
type httpProber struct {
	client *http.Client
}

// NewHTTPProber wraps an *http.Client as a Prober. A nil client uses the default.
func NewHTTPProber(client *http.Client) Prober {
	if client == nil {
		client = http.DefaultClient
	}
	return &httpProber{client: client}
}

func (p *httpProber) Exists(ctx context.Context, rawURL string) (bool, int64, error) {
	// HEAD first.
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false, 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false, 0, err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, resp.ContentLength, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return false, 0, nil
	}

	// Fallback: GET with a zero-length range (works on endpoints that 405 HEAD).
	greq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, 0, err
	}
	greq.Header.Set("Range", "bytes=0-0")
	gresp, err := p.client.Do(greq)
	if err != nil {
		return false, 0, err
	}
	gresp.Body.Close()
	switch gresp.StatusCode {
	case http.StatusPartialContent:
		return true, totalFromContentRange(gresp.Header.Get("Content-Range")), nil
	case http.StatusOK:
		return true, gresp.ContentLength, nil
	default:
		return false, 0, nil
	}
}

// totalFromContentRange parses Total from "bytes 0-0/Total"; -1 when unknown.
func totalFromContentRange(h string) int64 {
	i := strings.LastIndex(h, "/")
	if i < 0 {
		return -1
	}
	total := strings.TrimSpace(h[i+1:])
	if total == "" || total == "*" {
		return -1
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
