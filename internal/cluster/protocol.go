package cluster

import (
	"crypto/subtle"
	"net/http"
)

// The cluster control protocol is plain HTTP+JSON, token-guarded. The master
// drives slave agents: GET /node/info (enlist/health), POST /node/assign (queue
// a part), GET /node/status (progress), GET /node/part/{basename} (pull the
// finished file), POST /node/cancel.

// NodeInfo identifies a slave agent.
type NodeInfo struct {
	Name    string `json:"name"`
	Engine  string `json:"engine"`
	Version string `json:"version"`
}

// AssignRequest tells a slave to download one part.
type AssignRequest struct {
	JobID    string `json:"job_id"`
	URL      string `json:"url"`
	Basename string `json:"basename"`
}

// JobReport is a slave's progress for one assigned job.
type JobReport struct {
	JobID     string `json:"job_id"`
	Basename  string `json:"basename"`
	State     string `json:"state"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
	SpeedBPS  int64  `json:"speed_bps"`
	Err       string `json:"err,omitempty"`
}

// StatusReport is the full progress snapshot a slave returns.
type StatusReport struct {
	Node NodeInfo    `json:"node"`
	Jobs []JobReport `json:"jobs"`
}

// CancelRequest cancels a job on a slave.
type CancelRequest struct {
	JobID string `json:"job_id"`
}

// tokenAuth wraps h, requiring the shared cluster token (header or ?token=)
// when token is non-empty. Constant-time compare, mirroring the admin server.
func tokenAuth(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := r.Header.Get("X-Psxdh-Token")
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "unauthorised: cluster token required", http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
