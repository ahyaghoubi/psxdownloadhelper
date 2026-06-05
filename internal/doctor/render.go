package doctor

import (
	"fmt"
	"io"
	"strings"
)

// Render writes a human-readable summary of rep to w. It is intentionally
// rendered as plain text (no ANSI colour) so the output diffs cleanly in
// bug reports.
func Render(w io.Writer, rep *Report) {
	if rep == nil {
		_, _ = fmt.Fprintln(w, "psxdh doctor: empty report")
		return
	}

	_, _ = fmt.Fprintln(w, "psxdh doctor")
	_, _ = fmt.Fprintln(w, strings.Repeat("─", 60))

	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "DNS resolvers")
	for _, r := range rep.Resolvers {
		if r.Err != "" {
			_, _ = fmt.Fprintf(w, "  [skip] %s — %s\n", r.Label, r.Err)
			continue
		}
		_, _ = fmt.Fprintf(w, "  • %s\n", r.Label)
		for _, l := range r.Lookups {
			if l.Err != "" {
				_, _ = fmt.Fprintf(w, "      %-40s  FAIL (%v) %s\n", l.Host, l.Latency.Round(1e6), trim(l.Err, 80))
				continue
			}
			_, _ = fmt.Fprintf(w, "      %-40s  ok   (%v) %s\n", l.Host, l.Latency.Round(1e6), strings.Join(l.IPs, ", "))
		}
	}

	if len(rep.Hosts) > 0 {
		_, _ = fmt.Fprintln(w, "")
		_, _ = fmt.Fprintln(w, "Direct TLS handshakes (port 443)")
		for _, h := range rep.Hosts {
			if h.Err != "" {
				_, _ = fmt.Fprintf(w, "  %-40s  FAIL (%v) %s\n", h.Host, h.Latency.Round(1e6), trim(h.Err, 80))
				continue
			}
			_, _ = fmt.Fprintf(w, "  %-40s  ok   (%v)\n", h.Host, h.Latency.Round(1e6))
		}
	}

	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Embedded downloader (aria2c)")
	if rep.Aria2.Found {
		_, _ = fmt.Fprintf(w, "  ok    %s\n", rep.Aria2.Path)
		return
	}
	_, _ = fmt.Fprintln(w, "  FAIL  aria2c not available")
	if rep.Aria2.Err != "" {
		_, _ = fmt.Fprintf(w, "        %s\n", trim(rep.Aria2.Err, 120))
	}
	if rep.Aria2.Hint != "" {
		_, _ = fmt.Fprintf(w, "  Install: %s\n", rep.Aria2.Hint)
	}
}

// RenderProbe writes a probe result to w.
func RenderProbe(w io.Writer, r *ProbeResult) {
	if r == nil {
		_, _ = fmt.Fprintln(w, "psxdh probe: empty result")
		return
	}
	_, _ = fmt.Fprintf(w, "URL:        %s\n", r.URL)
	_, _ = fmt.Fprintf(w, "Classified: %s\n", r.Kind)
	if r.Hint.TitleHint != "" {
		_, _ = fmt.Fprintf(w, "Title hint: %s\n", r.Hint.TitleHint)
	}
	if r.Hint.PartIndex >= 0 {
		_, _ = fmt.Fprintf(w, "Part index: %d\n", r.Hint.PartIndex)
	}
	if r.ResolveErr != "" {
		_, _ = fmt.Fprintf(w, "DNS:        FAIL (%v) %s\n", r.ResolveLatency.Round(1e6), r.ResolveErr)
	} else {
		_, _ = fmt.Fprintf(w, "DNS:        ok   (%v) %s\n", r.ResolveLatency.Round(1e6), strings.Join(r.Resolved, ", "))
	}
	if r.HTTPErr != "" {
		_, _ = fmt.Fprintf(w, "HTTP:       FAIL (%v) %s\n", r.HTTPLatency.Round(1e6), r.HTTPErr)
		return
	}
	_, _ = fmt.Fprintf(w, "HTTP:       %s %d (%v)\n", r.Method, r.Status, r.HTTPLatency.Round(1e6))
	if r.Server != "" {
		_, _ = fmt.Fprintf(w, "  Server:        %s\n", r.Server)
	}
	if r.ContentType != "" {
		_, _ = fmt.Fprintf(w, "  Content-Type:  %s\n", r.ContentType)
	}
	if r.ContentLength > 0 {
		_, _ = fmt.Fprintf(w, "  Content-Length: %d\n", r.ContentLength)
	}
	if r.AcceptRanges != "" {
		_, _ = fmt.Fprintf(w, "  Accept-Ranges: %s\n", r.AcceptRanges)
	}
	if r.Location != "" {
		_, _ = fmt.Fprintf(w, "  Location:      %s\n", r.Location)
	}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
