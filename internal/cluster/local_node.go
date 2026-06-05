package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/downloader"
)

// localNode is an in-process nodeTransport implementation used by the master to
// contribute its own WAN link. It downloads assigned parts straight into the
// master's library directory; PullPart is therefore a no-op when dstDir matches
// libDir.
type localNode struct {
	name    string
	version string
	engine  string
	libDir  string
	dl      downloader.Downloader

	mu   sync.Mutex
	jobs map[string]localJob // jobID → (basename, dlID)
}

type localJob struct {
	basename string
	dlID     string
}

func NewLocalNode(name, version, engine, libDir string, dl downloader.Downloader) nodeTransport {
	return &localNode{
		name: name, version: version, engine: engine,
		libDir: libDir, dl: dl,
		jobs: make(map[string]localJob),
	}
}

func (n *localNode) Info(_ context.Context) (NodeInfo, error) {
	return NodeInfo{Name: n.name, Engine: n.engine, Version: n.version}, nil
}

func (n *localNode) Assign(ctx context.Context, req AssignRequest) error {
	if req.JobID == "" || req.URL == "" || req.Basename == "" {
		return fmt.Errorf("local node: bad assign request")
	}
	dlID, err := n.dl.Add(ctx, req.URL, n.libDir)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.jobs[req.JobID] = localJob{basename: req.Basename, dlID: dlID}
	n.mu.Unlock()
	return nil
}

func (n *localNode) Status(ctx context.Context) (StatusReport, error) {
	n.mu.Lock()
	snap := make(map[string]localJob, len(n.jobs))
	for id, j := range n.jobs {
		snap[id] = j
	}
	n.mu.Unlock()

	report := StatusReport{Node: NodeInfo{Name: n.name, Engine: n.engine, Version: n.version}}
	for jobID, j := range snap {
		st, err := n.dl.Status(ctx, j.dlID)
		jr := JobReport{JobID: jobID, Basename: j.basename}
		if err != nil {
			jr.State = string(downloader.StateError)
			jr.Err = err.Error()
		} else {
			jr.State = string(st.State)
			jr.Completed, jr.Total, jr.SpeedBPS, jr.Err = st.Completed, st.Total, st.SpeedBPS, st.Err
		}
		report.Jobs = append(report.Jobs, jr)
	}
	return report, nil
}

func (n *localNode) PullPart(_ context.Context, basename, dstDir string) error {
	// Local node writes directly into libDir. For the master collect path, dstDir
	// is libDir, so there's nothing to do beyond ensuring the file exists.
	if sameDir(dstDir, n.libDir) {
		_, err := os.Stat(filepath.Join(n.libDir, basename))
		return err
	}
	// Defensive: if someone uses this transport with a different dstDir, require
	// the caller to rely on a remote node client instead of silently copying.
	return fmt.Errorf("local node: pull part into %q not supported (lib dir is %q)", dstDir, n.libDir)
}

func (n *localNode) Cancel(ctx context.Context, jobID string) error {
	n.mu.Lock()
	j, ok := n.jobs[jobID]
	if ok {
		delete(n.jobs, jobID)
	}
	n.mu.Unlock()
	if !ok {
		return nil
	}
	return n.dl.Remove(ctx, j.dlID)
}

func sameDir(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return aa == bb
}

var _ nodeTransport = (*localNode)(nil)

