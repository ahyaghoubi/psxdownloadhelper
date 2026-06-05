// Package downloader is psxdh's embedded download engine. ADR 0005 records the
// decision to manage a local aria2c subprocess (reversing the original
// "no built-in downloader" non-goal) so a node can fetch PKG parts directly
// rather than handing URLs to an external tool. The Downloader interface keeps
// the cluster and the test suite independent of a real aria2c.
package downloader

import "context"

// State is the lifecycle of a download job.
type State string

const (
	StateWaiting  State = "waiting"
	StateActive   State = "active"
	StatePaused   State = "paused"
	StateComplete State = "complete"
	StateError    State = "error"
	StateRemoved  State = "removed"
)

// Status is a point-in-time snapshot of a job's progress.
type Status struct {
	ID        string `json:"id"`
	State     State  `json:"state"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
	SpeedBPS  int64  `json:"speed_bps"`
	Err       string `json:"err,omitempty"`
}

// Done reports whether the job finished successfully.
func (s Status) Done() bool { return s.State == StateComplete }

// Downloader fetches a URL to a directory and reports progress by job ID.
// Implementations must be safe for concurrent use.
type Downloader interface {
	// Add queues url for download into dir (the file keeps its URL basename)
	// and returns an opaque job ID.
	Add(ctx context.Context, url, dir string) (id string, err error)
	// Status returns the current progress for a job ID.
	Status(ctx context.Context, id string) (Status, error)
	// Remove cancels/forgets a job.
	Remove(ctx context.Context, id string) error
	// Close releases any resources (e.g. stops a managed subprocess).
	Close() error
}
