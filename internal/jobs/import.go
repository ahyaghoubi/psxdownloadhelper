// Package jobs orchestrates the work that turns capture events (live or
// imported from a JSONL file) into cluster downloads and resumable on-disk
// state. It is the glue between session.Store, captureio aggregation, and
// cluster.Manager: feed it events, get a populated dashboard plus queued
// cluster work in return.
package jobs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/captureio"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
)

// ImportOptions configures one ImportFromEvents call.
type ImportOptions struct {
	// Sessions, when non-nil, receives every event (idempotent dedupe).
	Sessions *session.Store
	// Cluster, when non-nil, receives Submit() calls for each title's parts.
	Cluster *cluster.Manager
	// Prober, when non-nil and Enumerate is true, expands captured PKG seeds
	// into the full _0.._N series before submission. Pass nil to skip CDN
	// probing and use only the URLs already in the JSONL file.
	Prober cluster.Prober
	// Enumerate triggers cluster.Enumerate when Prober is non-nil. Disable
	// it when the work network can't reach the PlayStation CDN.
	Enumerate bool
	// Logger receives info/warn lines for the import. Defaults to slog.Default.
	Logger *slog.Logger
}

// ImportResult summarises one import for the caller (CLI, dashboard, log line).
type ImportResult struct {
	Titles     int `json:"titles"`
	Parts      int `json:"parts"`
	Enumerated int `json:"enumerated"`
	Submitted  int `json:"submitted"`
}

// ImportFromEvents replays events into the session store and (optionally)
// queues every observed title in the cluster manager. When Prober is set and
// Enumerate is true, the import probes the CDN for the full part series of
// each title; otherwise the captured URLs are submitted as-is.
//
// All collaborators are optional: with both Sessions and Cluster nil, the
// function still aggregates and reports counts (useful for dry-runs).
func ImportFromEvents(ctx context.Context, events []capture.Event, opts ImportOptions) (ImportResult, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Sessions != nil {
		opts.Sessions.LoadFromEvents(events)
	}
	byTitle := captureio.AggregateByTitle(events)

	var res ImportResult
	for title, parts := range byTitle {
		if title == "unknown" {
			// "unknown" is the catch-all bucket for events without a TitleHint;
			// they shouldn't be queued because we have no stable Submit key.
			continue
		}
		res.Titles++
		res.Parts += len(parts)

		clusterParts := capturedToClusterParts(parts)

		if opts.Enumerate && opts.Prober != nil {
			seed := captureio.PickEnumerateSeed(parts)
			if seed != nil {
				enumerated, err := cluster.Enumerate(ctx, seed, opts.Prober, 0)
				if err != nil {
					opts.Logger.Warn("import: enumerate failed; falling back to captured URLs", "title", title, "err", err)
				} else {
					clusterParts = mergePartsByBasename(clusterParts, enumerated)
					res.Enumerated += len(enumerated)
				}
			}
		}

		if opts.Cluster != nil && len(clusterParts) > 0 {
			before := len(clusterParts)
			opts.Cluster.Submit(title, clusterParts)
			res.Submitted += before
			opts.Logger.Info("import: title submitted to cluster", "title", title, "parts", before)
		}
	}

	if len(events) > 0 && res.Titles == 0 && opts.Cluster != nil {
		return res, errors.New("no titles with TitleHint were imported")
	}
	return res, nil
}

// capturedToClusterParts translates aggregated captureio parts into the
// cluster.PartURL shape. Sizes are unknown until a HEAD probe runs (Enumerate
// fills them in); the cluster manager treats Size=0 as unknown.
func capturedToClusterParts(parts []captureio.Part) []cluster.PartURL {
	out := make([]cluster.PartURL, 0, len(parts))
	for _, p := range parts {
		if p.URL == nil {
			continue
		}
		idx := p.PartIndex
		if idx < 0 {
			idx = 0
		}
		out = append(out, cluster.PartURL{
			Index:    idx,
			URL:      p.URL.String(),
			Basename: p.Basename,
		})
	}
	return out
}

// mergePartsByBasename returns enumerated wherever a basename overlaps and
// keeps any captured-only entries (CDN may not report something the proxy saw
// — keeping it errs on the side of "let the user try"). Enumerated wins
// because it carries authoritative Size data.
func mergePartsByBasename(captured, enumerated []cluster.PartURL) []cluster.PartURL {
	if len(enumerated) == 0 {
		return captured
	}
	have := make(map[string]struct{}, len(enumerated))
	out := append([]cluster.PartURL(nil), enumerated...)
	for _, p := range enumerated {
		have[p.Basename] = struct{}{}
	}
	for _, c := range captured {
		if _, ok := have[c.Basename]; ok {
			continue
		}
		out = append(out, c)
	}
	return out
}
