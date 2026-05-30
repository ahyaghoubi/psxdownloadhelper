package match

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var (
	titleIDRegex = regexp.MustCompile(`(?:UP\d{4}-)?(?:CUSA\d{5}|PPSA\d{5})`)
	partRegex    = regexp.MustCompile(`_(\d+)\.(?:pkg|crc)$`)
)

// Classify returns the Kind for u plus an extracted Hint. If no rule matches,
// Kind is KindUnknown but Hint is still populated when title/part metadata
// can be parsed from the URL path.
func (rs *RuleSet) Classify(u *url.URL) (Kind, Hint) {
	hint := extractHint(u)
	if u == nil {
		return KindUnknown, hint
	}
	for _, r := range rs.rules {
		if !hostMatches(u.Host, r.hostSuffix) {
			continue
		}
		if !r.pathRegex.MatchString(u.Path) {
			continue
		}
		return r.kind, hint
	}
	return KindUnknown, hint
}

// hostMatches treats HostSuffix as either the exact host or a domain suffix.
// "gst.prod.dl.playstation.net" matches "gst.prod.dl.playstation.net" and
// "a.gst.prod.dl.playstation.net" but not "evil.com". Explicit ports are
// stripped from both sides — Sony URLs are plain HTTP/HTTPS with default
// ports, but tests and unusual intermediaries may include one.
func hostMatches(host, suffix string) bool {
	if suffix == "" {
		return true
	}
	host = stripPort(host)
	suffix = stripPort(suffix)
	if host == suffix {
		return true
	}
	return strings.HasSuffix(host, "."+suffix)
}

func stripPort(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func extractHint(u *url.URL) Hint {
	h := Hint{PartIndex: -1}
	if u == nil {
		return h
	}
	if title := titleIDRegex.FindString(u.Path); title != "" {
		h.TitleHint = title
	}
	base := path.Base(u.Path)
	if m := partRegex.FindStringSubmatch(base); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			h.PartIndex = n
		}
	}
	return h
}
