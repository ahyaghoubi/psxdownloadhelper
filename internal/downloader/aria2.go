package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/handoff"
)

// Aria2Options configures the managed aria2c subprocess.
type Aria2Options struct {
	Binary               string // path to aria2c; auto-detected on PATH when empty
	RPCPort              int    // RPC listen port (default 6800)
	RPCSecret            string // auto-generated when empty
	ConnectionsPerServer int    // aria2 -x
	Split                int    // aria2 -s
	MaxConcurrent        int    // aria2 -j
	Logger               *slog.Logger
}

// aria2Downloader supervises an aria2c process and drives it over JSON-RPC.
type aria2Downloader struct {
	client *handoff.Aria2Client
	logger *slog.Logger

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	closed bool
}

// StartAria2 locates aria2c, launches it with RPC enabled, waits for it to
// answer, and returns a Downloader. The process is supervised: if it exits
// unexpectedly it is restarted until Close is called.
func StartAria2(opts Aria2Options) (Downloader, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	binary, err := LocateAria2(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("downloader: %w", err)
	}
	port := opts.RPCPort
	if port == 0 {
		port = 6800
	}
	secret := opts.RPCSecret
	if secret == "" {
		secret = genSecret()
	}

	d := &aria2Downloader{
		client: handoff.NewAria2(fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port), secret, nil),
		logger: logger,
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	args := aria2Args(port, secret, opts)
	if err := d.spawn(ctx, binary, args); err != nil {
		cancel()
		return nil, err
	}
	go d.supervise(ctx, binary, args)

	// Wait for the RPC to come up.
	if err := d.waitReady(5 * time.Second); err != nil {
		cancel()
		return nil, fmt.Errorf("downloader: aria2c did not become ready: %w", err)
	}
	logger.Info("downloader: aria2c started", "binary", binary, "rpc_port", port)
	return d, nil
}

func aria2Args(port int, secret string, opts Aria2Options) []string {
	args := []string{
		"--enable-rpc",
		"--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(port),
		"--rpc-secret=" + secret,
		"--continue=true", // resume partial files across runs
		"--auto-file-renaming=false",
		"--allow-overwrite=true",
	}
	if opts.ConnectionsPerServer > 0 {
		args = append(args, "--max-connection-per-server="+strconv.Itoa(opts.ConnectionsPerServer))
	}
	if opts.Split > 0 {
		args = append(args, "--split="+strconv.Itoa(opts.Split))
	}
	if opts.MaxConcurrent > 0 {
		args = append(args, "--max-concurrent-downloads="+strconv.Itoa(opts.MaxConcurrent))
	}
	return args
}

func (d *aria2Downloader) spawn(ctx context.Context, binary string, args []string) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("downloader: start aria2c: %w", err)
	}
	d.mu.Lock()
	d.cmd = cmd
	d.mu.Unlock()
	return nil
}

// supervise restarts aria2c if it dies before Close, with a short backoff.
func (d *aria2Downloader) supervise(ctx context.Context, binary string, args []string) {
	for {
		d.mu.Lock()
		cmd := d.cmd
		d.mu.Unlock()
		if cmd == nil {
			return
		}
		_ = cmd.Wait()
		select {
		case <-ctx.Done():
			return
		default:
		}
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if closed {
			return
		}
		d.logger.Warn("downloader: aria2c exited unexpectedly; restarting")
		time.Sleep(500 * time.Millisecond)
		if err := d.spawn(ctx, binary, args); err != nil {
			d.logger.Error("downloader: aria2c restart failed", "err", err)
			return
		}
	}
}

func (d *aria2Downloader) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := d.client.Ping(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (d *aria2Downloader) Add(ctx context.Context, url, dir string) (string, error) {
	return d.client.AddURI(ctx, url, dir)
}

func (d *aria2Downloader) Status(ctx context.Context, id string) (Status, error) {
	js, err := d.client.TellStatus(ctx, id)
	if err != nil {
		return Status{}, err
	}
	return Status{
		ID:        id,
		State:     mapState(js.Status),
		Completed: js.Completed,
		Total:     js.Total,
		SpeedBPS:  js.SpeedBPS,
		Err:       js.ErrorMessage,
	}, nil
}

func (d *aria2Downloader) Remove(ctx context.Context, id string) error {
	return d.client.Remove(ctx, id)
}

func (d *aria2Downloader) Close() error {
	d.mu.Lock()
	d.closed = true
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel() // CommandContext kills the process
	}
	return nil
}

func mapState(s string) State {
	switch s {
	case "active":
		return StateActive
	case "waiting":
		return StateWaiting
	case "paused":
		return StatePaused
	case "complete":
		return StateComplete
	case "error":
		return StateError
	case "removed":
		return StateRemoved
	default:
		return StateWaiting
	}
}

func genSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("downloader: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ensure interface compliance
var _ Downloader = (*aria2Downloader)(nil)

// ErrClosed is returned by the fake downloader after Close.
var ErrClosed = errors.New("downloader: closed")
