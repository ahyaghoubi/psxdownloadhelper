// Command psxdh is a LAN proxy for PlayStation consoles. It shows the
// download links the console asks for, so you can fetch them quickly on your
// PC with Free Download Manager (or any downloader), then serves those files
// back to the console from your library folder.
//
// Usage:
//
//	psxdh [--listen 0.0.0.0:8080] [--library DIR] [--config FILE] [--log-level LEVEL]
//	psxdh version
//
// Running it with no arguments (for example by double-clicking it) starts
// the proxy and opens the dashboard in your browser.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/dashboard"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netinfo"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/proxy"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/tracker"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	bare := len(os.Args) == 1
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, bare)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "psxdh:", err)
		if bare {
			waitForEnter()
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, openBrowser bool) error {
	// "proxy" was the subcommand in earlier releases; accept it so existing
	// scripts and shortcuts keep working.
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-version":
			fmt.Fprintln(out, version)
			return nil
		case "proxy":
			args = args[1:]
		}
	}

	fs := flag.NewFlagSet("psxdh", flag.ContinueOnError)
	fs.SetOutput(out)
	cfgPath := fs.String("config", "", "settings file (default "+config.DefaultPath()+")")
	listen := fs.String("listen", "", "address the console connects to, host:port (default 0.0.0.0:8080)")
	libDir := fs.String("library", "", "folder your download manager saves into (default ~/Downloads/psxdh)")
	logLevel := fs.String("log-level", "", "debug, info, warn or error")
	noBrowser := fs.Bool("no-browser", false, "don't open the dashboard in a browser")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: psxdh [flags]\n       psxdh version\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown argument %q (run psxdh -h for help)", fs.Arg(0))
	}

	path, explicit := *cfgPath, *cfgPath != ""
	if !explicit {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path, explicit)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Proxy.Listen = *listen
	}
	if *libDir != "" {
		cfg.Library.Dir = *libDir
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}
	if err := cfg.Finalise(); err != nil {
		return err
	}
	logger := newLogger(cfg.Log.Level)

	rules, err := match.LoadDefaults(true, true)
	if err != nil {
		return err
	}
	lib, err := library.New(cfg.Library.Dir, cfg.Library.IgnoreSuffixes, 2*time.Second)
	if err != nil {
		return fmt.Errorf("library folder %s: %w", cfg.Library.Dir, err)
	}
	track := tracker.New()
	// Captured links are saved next to the config file so a long download
	// can outlive a restart of psxdh, with the console switched off.
	linksPath := ""
	if path != "" {
		linksPath = filepath.Join(filepath.Dir(path), "links.json")
		if err := track.Load(linksPath); err != nil {
			logger.Warn("could not restore saved links", "err", err)
		}
	}

	ln, err := net.Listen("tcp", cfg.Proxy.Listen)
	if err != nil {
		if isAddrInUse(err) {
			return fmt.Errorf("%s is already in use. Is psxdh already running? To use another port: psxdh --listen 0.0.0.0:8090", cfg.Proxy.Listen)
		}
		return fmt.Errorf("listen on %s: %w", cfg.Proxy.Listen, err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	dash := dashboard.New(dashboard.Info{
		Version:    version,
		Port:       port,
		LibraryDir: lib.Root(),
		ConfigPath: path,
	}, lib, track)
	handler, err := proxy.New(proxy.Deps{
		Rules:         rules,
		Library:       lib,
		Tracker:       track,
		Dashboard:     dash,
		IsSelf:        selfMatcher(port),
		UpstreamProxy: cfg.UpstreamProxyURL(),
		Logger:        logger,
		Context:       ctx,
	})
	if err != nil {
		return err
	}
	dash.OnDownloadOnPC = func(pkg string) {
		items, _ := track.Snapshot()
		for _, it := range items {
			if it.Package == pkg && it.Part >= 0 {
				handler.FindPartsFor(it.URL, it.Kind, it.Title)
				return
			}
		}
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
	}
	go lib.Run(ctx, time.Second)
	saved := make(chan struct{})
	go func() {
		defer close(saved)
		saveLinks(ctx, track, linksPath, logger)
	}()

	printBanner(out, port, lib.Root(), cfg)
	if openBrowser && !*noBrowser {
		openURL("http://localhost:" + port + "/")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	fmt.Fprintln(out, "\nStopping…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	<-saved
	return nil
}

// saveLinks writes the tracker to path whenever it changes, and once more
// when ctx ends.
func saveLinks(ctx context.Context, t *tracker.Tracker, path string, logger *slog.Logger) {
	if path == "" {
		return
	}
	last := t.Version()
	save := func() {
		v := t.Version()
		if v == last {
			return
		}
		if err := t.Save(path); err != nil {
			logger.Warn("could not save links", "path", path, "err", err)
			return
		}
		last = v
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			save()
			return
		case <-tick.C:
			save()
		}
	}
}

func printBanner(out io.Writer, port, libDir string, cfg *config.Config) {
	addrs, _ := netinfo.IPv4Addrs()
	ip := "<this PC's IP>"
	if len(addrs) > 0 {
		ip = addrs[0].IP
	}
	fmt.Fprintf(out, "\n  psxdh %s is running\n\n", version)
	if len(addrs) <= 1 {
		fmt.Fprintf(out, "  Console proxy   %s  port %s\n", ip, port)
	} else {
		for i, a := range addrs {
			label := "Console proxy "
			if i > 0 {
				label = "           or "
			}
			fmt.Fprintf(out, "  %s  %s  port %s  (%s)\n", label, a.IP, port, a.Interface)
		}
	}
	fmt.Fprintf(out, "  Dashboard       http://%s:%s\n", ip, port)
	fmt.Fprintf(out, "  Library folder  %s\n", libDir)
	if u := cfg.UpstreamProxyURL(); u != nil {
		fmt.Fprintf(out, "  Upstream proxy  %s\n", u.Redacted())
	}
	fmt.Fprintf(out, "\n  Press Ctrl+C to stop.\n\n")
}

// selfMatcher reports whether host:port addresses this machine on our port,
// so a browser using psxdh as its proxy can still open the dashboard.
func selfMatcher(port string) func(string) bool {
	return func(hostport string) bool {
		host, p, err := net.SplitHostPort(hostport)
		if err != nil {
			host, p = hostport, "80"
		}
		if p != port {
			return false
		}
		host = strings.Trim(host, "[]")
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			return true
		}
		addrs, _ := netinfo.IPv4Addrs()
		for _, a := range addrs {
			if a.IP == ip.String() {
				return true
			}
		}
		return false
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(strings.ToLower(err.Error()), "address already in use") ||
		strings.Contains(strings.ToLower(err.Error()), "only one usage of each socket address")
}

func openURL(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// waitForEnter keeps a double-clicked console window open long enough to
// read an error.
func waitForEnter() {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return
	}
	fmt.Fprintln(os.Stderr, "\nPress Enter to close…")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
