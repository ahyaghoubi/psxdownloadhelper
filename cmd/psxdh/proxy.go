package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/proxy"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"

	"github.com/spf13/cobra"
)

func newProxyCmd() *cobra.Command {
	var cfgPath, listenAddr, libDir, logLevel string
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the HTTP proxy + library server",
		Long:  "Loads configuration, starts the library watcher, and (once ADR 0001 lands) the HTTP proxy. Until then, this command runs the watcher half so the wiring can be smoke-tested end-to-end.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			applyOverrides(cfg, listenAddr, libDir, logLevel)
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			logger := setupLogger(cfg.Log)

			rules, err := match.LoadDefaults(cfg.Match.PS4, cfg.Match.PS5)
			if err != nil {
				return fmt.Errorf("load match rules: %w", err)
			}

			idx, err := library.NewIndex(cfg.Library.Dir, library.Layout(cfg.Library.Layout))
			if err != nil {
				return fmt.Errorf("init library index: %w", err)
			}

			bus := capture.NewBus(1024)
			serveH := serve.New(logger)

			watcher, err := library.NewWatcher(idx, library.WatcherConfig{
				Settle:         cfg.Library.StableSettle(),
				IgnoreSuffixes: cfg.Library.IgnoreSuffixes,
				Logger:         logger,
			})
			if err != nil {
				return fmt.Errorf("init library watcher: %w", err)
			}

			proxySrv, err := proxy.New(proxy.Deps{
				Config:   cfg,
				Rules:    rules,
				Resolver: idx,
				Serve:    serveH,
				Bus:      bus,
				Logger:   logger,
			})
			if err != nil {
				return fmt.Errorf("init proxy: %w", err)
			}

			printBanner(cmd, cfg, idx, rules)
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintf(cmd.OutOrStdout(), "Point your console's HTTP proxy at: %s:%s\n",
				lanIP(), portOf(cfg.Proxy.Listen))
			fmt.Fprintln(cmd.OutOrStdout(), "Press Ctrl-C to shut down.")
			fmt.Fprintln(cmd.OutOrStdout())

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			// Drain library events so the watcher buffer doesn't back up.
			go func() {
				for ev := range watcher.Events() {
					logger.Debug("library event", "kind", ev.Kind, "path", ev.Path, "size", ev.Size)
				}
			}()

			// Run watcher and proxy concurrently. The first to error cancels
			// the other; clean shutdown is signalled by ctx.Done with nil error.
			errCh := make(chan error, 2)
			go func() {
				if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					errCh <- fmt.Errorf("watcher: %w", err)
					return
				}
				errCh <- nil
			}()
			go func() {
				if err := proxySrv.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
					errCh <- fmt.Errorf("proxy: %w", err)
					return
				}
				errCh <- nil
			}()

			var firstErr error
			for i := 0; i < 2; i++ {
				if e := <-errCh; e != nil && firstErr == nil {
					firstErr = e
					cancel()
				}
			}
			return firstErr
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to config.yaml (defaults are used when empty)")
	cmd.Flags().StringVar(&listenAddr, "listen", "", "Override proxy.listen (host:port)")
	cmd.Flags().StringVar(&libDir, "library", "", "Override library.dir")
	cmd.Flags().StringVar(&logLevel, "log-level", "", "Override log.level (debug|info|warn|error)")
	return cmd
}

func applyOverrides(cfg *config.Config, listenAddr, libDir, logLevel string) {
	if listenAddr != "" {
		cfg.Proxy.Listen = listenAddr
	}
	if libDir != "" {
		cfg.Library.Dir = libDir
	}
	if logLevel != "" {
		cfg.Log.Level = logLevel
	}
}

func setupLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.JSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func printBanner(cmd *cobra.Command, cfg *config.Config, idx *library.Index, rules *match.RuleSet) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "psxdh %s\n", version)
	fmt.Fprintf(out, "  LAN IP:        %s\n", lanIP())
	fmt.Fprintf(out, "  proxy listen:  %s\n", cfg.Proxy.Listen)
	fmt.Fprintf(out, "  admin listen:  http://%s/\n", cfg.Admin.Listen)
	fmt.Fprintf(out, "  library dir:   %s\n", idx.Root())
	fmt.Fprintf(out, "  library layout: %s\n", idx.Layout())
	fmt.Fprintf(out, "  match rules:   %d\n", rules.Len())
}

// portOf returns the port half of a host:port string, or the input
// unchanged if it can't be parsed.
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}

// lanIP returns the first non-loopback IPv4 address the host has, or an
// empty string when there is none. The console setup wizard uses this to
// tell the user which IP to point at.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}
