// Package handoff pushes captured CDN URLs to an external downloader so the
// user never has to copy-paste. The aria2 client speaks JSON-RPC to a running
// `aria2c --enable-rpc`, which is the best fit for the throttled/unstable links
// psxdh targets: segmented multi-connection downloads with native resume.
package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Aria2Client talks to an aria2 JSON-RPC endpoint (e.g. http://127.0.0.1:6800/jsonrpc).
type Aria2Client struct {
	rpcURL string
	secret string
	http   *http.Client
}

// NewAria2 builds a client for rpcURL. An empty secret means the aria2 daemon
// was started without --rpc-secret. The supplied http.Client may be nil.
func NewAria2(rpcURL, secret string, hc *http.Client) *Aria2Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Aria2Client{rpcURL: rpcURL, secret: secret, http: hc}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("aria2 rpc error %d: %s", e.Code, e.Message) }

// AddURI queues rawURL for download in aria2, instructing it to save the file
// under dir with the URL's original basename so the library watcher picks it
// up. It returns aria2's GID (download handle) on success.
func (c *Aria2Client) AddURI(ctx context.Context, rawURL, dir string) (gid string, err error) {
	opts := map[string]string{}
	if name := basename(rawURL); name != "" {
		opts["out"] = name
	}
	if dir != "" {
		opts["dir"] = dir
	}

	params := []any{}
	if c.secret != "" {
		params = append(params, "token:"+c.secret)
	}
	params = append(params, []string{rawURL}, opts)

	raw, err := c.call(ctx, "aria2.addUri", params)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &gid); err != nil {
		return "", fmt.Errorf("aria2: decode gid: %w", err)
	}
	return gid, nil
}

// JobStatus is the parsed result of aria2.tellStatus for one GID.
type JobStatus struct {
	Status       string // active | waiting | paused | error | complete | removed
	Completed    int64
	Total        int64
	SpeedBPS     int64
	ErrorMessage string
}

// TellStatus queries aria2 for a download's progress. aria2 returns all
// numeric fields as strings, so we parse them defensively.
func (c *Aria2Client) TellStatus(ctx context.Context, gid string) (JobStatus, error) {
	params := []any{}
	if c.secret != "" {
		params = append(params, "token:"+c.secret)
	}
	keys := []string{"status", "completedLength", "totalLength", "downloadSpeed", "errorMessage"}
	params = append(params, gid, keys)

	raw, err := c.call(ctx, "aria2.tellStatus", params)
	if err != nil {
		return JobStatus{}, err
	}
	var fields struct {
		Status          string `json:"status"`
		CompletedLength string `json:"completedLength"`
		TotalLength     string `json:"totalLength"`
		DownloadSpeed   string `json:"downloadSpeed"`
		ErrorMessage    string `json:"errorMessage"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return JobStatus{}, fmt.Errorf("aria2: decode status: %w", err)
	}
	atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	return JobStatus{
		Status:       fields.Status,
		Completed:    atoi(fields.CompletedLength),
		Total:        atoi(fields.TotalLength),
		SpeedBPS:     atoi(fields.DownloadSpeed),
		ErrorMessage: fields.ErrorMessage,
	}, nil
}

// Remove cancels a download (aria2.forceRemove) and forgets it.
func (c *Aria2Client) Remove(ctx context.Context, gid string) error {
	params := []any{}
	if c.secret != "" {
		params = append(params, "token:"+c.secret)
	}
	params = append(params, gid)
	_, err := c.call(ctx, "aria2.forceRemove", params)
	return err
}

// Ping verifies the daemon is reachable by calling aria2.getVersion.
func (c *Aria2Client) Ping(ctx context.Context) error {
	var params []any
	if c.secret != "" {
		params = append(params, "token:"+c.secret)
	}
	_, err := c.call(ctx, "aria2.getVersion", params)
	return err
}

func (c *Aria2Client) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: "psxdh", Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aria2: rpc call %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("aria2: rpc %s returned HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var rr rpcResponse
	if err := json.Unmarshal(data, &rr); err != nil {
		return nil, fmt.Errorf("aria2: decode response: %w", err)
	}
	if rr.Error != nil {
		return nil, rr.Error
	}
	return rr.Result, nil
}

// basename extracts the trailing filename of a URL path, ignoring the query.
func basename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return ""
	}
	name := path.Base(p)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}
