package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/downloader"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/lifecycle"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
)

// Agent is the slave-side server. It accepts download assignments, drives the
// embedded downloader into its work directory, reports progress, and serves
// finished parts so the master can pull them.
type Agent struct {
	name    string
	version string
	token   string
	workDir string
	engine  string
	dl      downloader.Downloader
	serveH  *serve.Handler
	logger  *slog.Logger

	mu    sync.Mutex
	jobs  map[string]*agentJob // jobID → job
	httpd *http.Server
}

type agentJob struct {
	basename string
	dlID     string
}

// AgentDeps bundles an Agent's collaborators.
type AgentDeps struct {
	Name    string
	Version string
	Token   string
	WorkDir string
	Engine  string
	Down    downloader.Downloader
	Logger  *slog.Logger
}

// NewAgent constructs a slave agent. WorkDir is created if missing.
func NewAgent(d AgentDeps) (*Agent, error) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if err := os.MkdirAll(d.WorkDir, 0o755); err != nil {
		return nil, err
	}
	return &Agent{
		name:    d.Name,
		version: d.Version,
		token:   d.Token,
		workDir: d.WorkDir,
		engine:  d.Engine,
		dl:      d.Down,
		serveH:  serve.New(d.Logger),
		logger:  d.Logger,
		jobs:    make(map[string]*agentJob),
	}, nil
}

// Handler returns the token-guarded agent HTTP handler (useful for httptest).
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/node/info", a.handleInfo)
	mux.HandleFunc("/node/assign", a.handleAssign)
	mux.HandleFunc("/node/status", a.handleStatus)
	mux.HandleFunc("/node/part/", a.handlePart)
	mux.HandleFunc("/node/cancel", a.handleCancel)
	return tokenAuth(a.token, mux)
}

// ListenAndServe binds the agent on bind until ctx is canceled.
func (a *Agent) ListenAndServe(ctx context.Context, bind string) error {
	a.httpd = &http.Server{Addr: bind, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		a.logger.Info("cluster agent listening", "addr", bind, "work_dir", a.workDir)
		err := a.httpd.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		lifecycle.ShutdownHTTP(a.logger, "cluster agent", a.httpd)
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *Agent) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, NodeInfo{Name: a.name, Engine: a.engine, Version: a.version})
}

func (a *Agent) handleAssign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req AssignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.URL == "" || req.JobID == "" {
		http.Error(w, "bad assign request", http.StatusBadRequest)
		return
	}
	dlID, err := a.dl.Add(r.Context(), req.URL, a.workDir)
	if err != nil {
		a.logger.Warn("cluster agent: download add failed", "url", req.URL, "err", err)
		http.Error(w, "download add failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	a.mu.Lock()
	a.jobs[req.JobID] = &agentJob{basename: req.Basename, dlID: dlID}
	a.mu.Unlock()
	a.logger.Info("cluster agent: assigned", "job", req.JobID, "basename", req.Basename)
	w.WriteHeader(http.StatusAccepted)
}

func (a *Agent) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	type pair struct {
		jobID string
		j     agentJob
	}
	snapshot := make([]pair, 0, len(a.jobs))
	for id, j := range a.jobs {
		snapshot = append(snapshot, pair{jobID: id, j: *j})
	}
	a.mu.Unlock()

	report := StatusReport{Node: NodeInfo{Name: a.name, Engine: a.engine, Version: a.version}}
	for _, p := range snapshot {
		st, err := a.dl.Status(r.Context(), p.j.dlID)
		jr := JobReport{JobID: p.jobID, Basename: p.j.basename}
		if err != nil {
			jr.State = string(downloader.StateError)
			jr.Err = err.Error()
		} else {
			jr.State = string(st.State)
			jr.Completed, jr.Total, jr.SpeedBPS, jr.Err = st.Completed, st.Total, st.SpeedBPS, st.Err
		}
		report.Jobs = append(report.Jobs, jr)
	}
	writeJSON(w, report)
}

// handlePart streams a finished file from the work dir, honouring Range so the
// master's pull is resumable.
func (a *Agent) handlePart(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimPrefix(r.URL.Path, "/node/part/")
	if base == "" || strings.ContainsAny(base, `/\`) {
		http.Error(w, "bad part name", http.StatusBadRequest)
		return
	}
	a.serveH.ServeFile(w, r, filepath.Join(a.workDir, base))
}

func (a *Agent) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req CancelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.JobID == "" {
		http.Error(w, "bad cancel request", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	j := a.jobs[req.JobID]
	delete(a.jobs, req.JobID)
	a.mu.Unlock()
	if j != nil {
		_ = a.dl.Remove(r.Context(), j.dlID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
