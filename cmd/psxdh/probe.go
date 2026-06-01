package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/doctor"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netresolve"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/upstream"
)

func newProbeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "probe <url>",
		Short: "Classify a single URL and issue a HEAD/GET against it",
		Long: `Useful when an FDM-side download succeeds but the console-side
replay misbehaves: probe runs the same rule classifier psxdh uses at
runtime, resolves the URL via the configured DNS resolvers, and prints
the headers and status code of the response.

It honours the same network configuration as the proxy command —
custom DNS, upstream proxy, IPv4 preference — so you can verify that a
given URL is reachable through the resolver chain you intend to deploy.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			rules, err := match.LoadDefaults(cfg.Match.PS4, cfg.Match.PS5)
			if err != nil {
				return fmt.Errorf("load rules: %w", err)
			}
			resolver, err := netresolve.NewFromConfig(netresolve.Config{
				Mode:            cfg.Network.DNS.Mode,
				Resolvers:       cfg.Network.DNS.Resolvers,
				Timeout:         cfg.Network.DNS.Timeout(),
				CacheTTL:        cfg.Network.DNS.CacheTTL(),
				CacheMaxEntries: cfg.Network.DNS.CacheMaxEntries,
			})
			if err != nil {
				return fmt.Errorf("init resolver: %w", err)
			}
			client, err := buildProbeClient(cfg, resolver)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			ctx, cancel2 := context.WithTimeout(ctx, 30*time.Second)
			defer cancel2()

			result, err := doctor.Probe(ctx, args[0], rules, resolver, client)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
			doctor.RenderProbe(cmd.OutOrStdout(), result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to config.yaml (defaults are used when empty)")
	return cmd
}

// buildProbeClient mirrors the proxy's upstream-client construction so
// probe and runtime behave identically.
func buildProbeClient(cfg *config.Config, resolver netresolve.Resolver) (*http.Client, error) {
	upCfg := upstream.Config{
		Resolver:    resolver,
		PreferIPv4:  cfg.Network.PreferIPv4,
		DialTimeout: cfg.Network.DialTimeout(),
	}
	if cfg.Network.UpstreamProxy.Enabled {
		upCfg.UpstreamProxy = cfg.Network.UpstreamProxy.URL
		upCfg.UpstreamProxyOnlyForHosts = cfg.Network.UpstreamProxy.OnlyForHosts
	}
	return upstream.New(upCfg)
}
