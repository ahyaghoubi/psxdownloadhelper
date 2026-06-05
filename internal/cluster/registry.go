package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// nodeTransport is how the manager talks to a download node. Implementations:
// - *nodeClient: a remote slave over HTTP.
// - *localNode: the master itself, in-process (downloads straight into lib dir).
type nodeTransport interface {
	Info(ctx context.Context) (NodeInfo, error)
	Assign(ctx context.Context, req AssignRequest) error
	Status(ctx context.Context) (StatusReport, error)
	PullPart(ctx context.Context, basename, dstDir string) error
	Cancel(ctx context.Context, jobID string) error
}

// nodeClient is the master's HTTP client for one slave agent.
type nodeClient struct {
	baseURL string // e.g. http://192.168.2.40:8082
	token   string
	http    *http.Client
}

func newNodeClient(baseURL, token string) *nodeClient {
	return &nodeClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *nodeClient) url(path string) string { return c.baseURL + path }

func (c *nodeClient) do(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("X-Psxdh-Token", c.token)
	}
	return c.http.Do(req)
}

func (c *nodeClient) Info(ctx context.Context) (NodeInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/node/info"), nil)
	resp, err := c.do(req)
	if err != nil {
		return NodeInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return NodeInfo{}, fmt.Errorf("node info: HTTP %d", resp.StatusCode)
	}
	var info NodeInfo
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

func (c *nodeClient) Assign(ctx context.Context, req AssignRequest) error {
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/node/assign"), bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("assign: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *nodeClient) Status(ctx context.Context) (StatusReport, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/node/status"), nil)
	resp, err := c.do(req)
	if err != nil {
		return StatusReport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusReport{}, fmt.Errorf("node status: HTTP %d", resp.StatusCode)
	}
	var sr StatusReport
	return sr, json.NewDecoder(resp.Body).Decode(&sr)
}

// PullPart downloads a finished part into dstDir as <basename>, via a temp file
// and atomic rename so a partial pull never looks complete.
func (c *nodeClient) PullPart(ctx context.Context, basename, dstDir string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/node/part/"+basename), nil)
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull part %s: HTTP %d", basename, resp.StatusCode)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dstDir, basename+".incoming")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, filepath.Join(dstDir, basename))
}

func (c *nodeClient) Cancel(ctx context.Context, jobID string) error {
	body, _ := json.Marshal(CancelRequest{JobID: jobID})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/node/cancel"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

var _ nodeTransport = (*nodeClient)(nil)
