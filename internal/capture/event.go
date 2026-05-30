// Package capture defines the event bus that fans out URL capture events
// from the proxy to session aggregators, the admin server, and export sinks.
// See plan.md §6.2.
package capture

import (
	"net/http"
	"net/url"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
)

// Event records a single classified request seen by the proxy.
// URL is the absolute URL exactly as the console requested it,
// query string intact (preservation is required by plan.md §6.3).
type Event struct {
	URL        *url.URL
	Method     string
	Kind       match.Kind
	Hint       match.Hint
	Headers    http.Header
	Time       time.Time
	ClientAddr string
}
