package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/downloader"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/lifecycle"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/mdns"

	"github.com/spf13/cobra"
)

// newNodeCmd runs psxdh as a cluster slave: an embedded downloader plus the
// agent API the master drives. It does not proxy for the console. See ADR 0005.
func newNodeCmd() *cobra.Command {
	var cfgPath, bind, masterURL string
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Run as a cluster slave node (download worker)",
		Long:  "Starts the embedded downloader and a control agent the master drives to fetch assigned PKG parts. See docs/architecture.md and ADR 0005.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if bind != "" {
				cfg.Cluster.Bind = bind
			}
			if masterURL != "" {
				cfg.Cluster.MasterURL = masterURL
			}
			cfg.Cluster.Enabled = true
			cfg.Cluster.Role = "slave"
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			logger := setupLogger(cfg.Log)

			name := cfg.Cluster.NodeName
			if name == "" {
				host, _ := os.Hostname()
				name = host
			}

			dl, engine, err := buildDownloader(cfg, logger)
			if err != nil {
				return fmt.Errorf("init downloader: %w", err)
			}

			agent, err := cluster.NewAgent(cluster.AgentDeps{
				Name: name, Version: version, Token: cfg.Cluster.Token,
				WorkDir: cfg.Library.Dir, Engine: engine, Down: dl, Logger: logger,
			})
			if err != nil {
				return fmt.Errorf("init agent: %w", err)
			}

			// Announce on the LAN so the master can auto-discover this node.
			if port, perr := mdns.PortFromListen(cfg.Cluster.Bind); perr == nil {
				if ann, aerr := mdns.AnnounceService(name, mdns.NodeServiceType, port, []string{"app=psxdh", "role=slave"}); aerr == nil {
					defer ann.Close()
					logger.Info("cluster node: announced on LAN", "service", mdns.NodeServiceType, "port", port)
				} else {
					logger.Warn("cluster node: mDNS announce failed", "err", aerr)
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "psxdh node %q\n  agent listen: %s\n  work dir:     %s\n  downloader:   %s\n",
				name, cfg.Cluster.Bind, cfg.Library.Dir, engine)
			fmt.Fprintln(cmd.OutOrStdout(), "Add this node on the master's dashboard, or let it auto-discover. Ctrl-C to stop.")

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			runErr := lifecycle.Run(ctx, lifecycle.Options{Logger: logger, Cancel: cancel},
				func(ctx context.Context) error {
					return agent.ListenAndServe(ctx, cfg.Cluster.Bind)
				},
			)
			_ = dl.Close()
			if runErr != nil {
				return runErr
			}
			logger.Info("shutdown complete")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to config.yaml")
	cmd.Flags().StringVar(&bind, "bind", "", "Override cluster.bind (host:port)")
	cmd.Flags().StringVar(&masterURL, "master", "", "Override cluster.master_url")
	return cmd
}

// buildDownloader constructs the embedded downloader. It prefers a managed
// aria2c (per ADR 0005). When aria2c is missing, startup fails unless
// downloader.allow_http_fallback is set (dev/CI only).
func buildDownloader(cfg *config.Config, logger *slog.Logger) (downloader.Downloader, string, error) {
	d := cfg.Downloader
	aria, err := downloader.StartAria2(downloader.Aria2Options{
		Binary:               d.Aria2Binary,
		RPCPort:              d.RPCPort,
		RPCSecret:            d.RPCSecret,
		ConnectionsPerServer: d.ConnectionsPerServer,
		Split:                d.Split,
		MaxConcurrent:        d.MaxConcurrent,
		Logger:               logger,
	})
	if err == nil {
		return aria, "aria2", nil
	}
	if !d.AllowHTTPFallback {
		return nil, "", fmt.Errorf("downloader: aria2c required: %w\n\nInstall: %s", err, downloader.InstallHint())
	}
	logger.Warn("downloader: aria2c unavailable; using built-in HTTP engine", "err", err)
	return downloader.NewHTTP(nil), "http", nil
}
