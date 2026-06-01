package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/doctor"
)

func newDoctorCmd() *cobra.Command {
	var (
		cfgPath       string
		hosts         []string
		skipTLS       bool
		timeoutSec    int
		listenForCfg  string // unused; kept for symmetry with proxy flags
		_            = listenForCfg
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe DNS resolvers and the PlayStation CDN for reachability",
		Long: `Runs the connectivity diagnostic suite documented in
docs/network-resilience.md. For each configured DNS resolver the command
resolves a small set of PSN CDN hosts and reports latency plus the
returned IPs; it then attempts a direct TLS handshake to port 443 of
each host so you can see whether DNS or transport is the bottleneck.

The command is non-destructive — it never touches the library or
modifies state. It is safe to run alongside the running proxy.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			opts := doctor.CheckOptions{
				Hosts:            hosts,
				HandshakeTimeout: time.Duration(timeoutSec) * time.Second,
				SkipHandshake:    skipTLS,
			}
			rep := doctor.Check(ctx, cfg.Network, opts)
			doctor.Render(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to config.yaml (defaults are used when empty)")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "Override the default PSN hosts (repeatable)")
	cmd.Flags().BoolVar(&skipTLS, "skip-tls", false, "Skip the direct TLS handshake check")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 5, "Per-host TLS handshake timeout in seconds")
	return cmd
}
