package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ahyaghoubi/psxdownloadhelper/internal/admin"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/bandwidth"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/capture"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/circuit"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/cluster"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/config"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/downloader"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/handoff"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/jobs"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/library"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/lifecycle"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/match"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/mdns"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netinfo"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/netresolve"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/persist"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/proxy"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/serve"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/session"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/upstream"
	"github.com/ahyaghoubi/psxdownloadhelper/internal/verify"

	"github.com/spf13/cobra"
)

func newProxyCmd() *cobra.Command {
	var cfgPath, listenAddr, libDir, logLevel string
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the HTTP proxy + library server",
		Long:  "Loads configuration, starts the library watcher, and runs the HTTP proxy until interrupted. See docs/configuration.md for the flag and config-file reference.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			loadPath, persistPath, err := config.ResolveConfigPath(cfgPath)
			if err != nil {
				return fmt.Errorf("resolve config path: %w", err)
			}
			cfg, err := config.Load(loadPath)
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
			sessions := session.New(idx)
			jobStore := jobs.NewStateStore(cfg.Jobs.StatePath)

			var verifier verify.Verifier
			if cfg.Verify.OnStable {
				verifier = verify.DefaultVerifier()
			}
			watcher, err := library.NewWatcher(idx, library.WatcherConfig{
				Settle:         cfg.Library.StableSettle(),
				IgnoreSuffixes: cfg.Library.IgnoreSuffixes,
				Verifier:       verifier,
				Logger:         logger,
			})
			if err != nil {
				return fmt.Errorf("init library watcher: %w", err)
			}

			// Build the upstream HTTP client with the configured DNS
			// resolver, optional proxy chain, circuit breaker, and
			// bandwidth cap. See docs/network-resilience.md for the
			// user-facing knobs and docs/decisions/0003-network-resilience.md
			// for the design.
			upClient, dnsHealth, err := buildUpstreamClient(cfg)
			if err != nil {
				return fmt.Errorf("init upstream client: %w", err)
			}

			proxySrv, err := proxy.New(proxy.Deps{
				Config:         cfg,
				Rules:          rules,
				Resolver:       idx,
				Serve:          serveH,
				Bus:            bus,
				Logger:         logger,
				UpstreamClient: upClient,
			})
			if err != nil {
				return fmt.Errorf("init proxy: %w", err)
			}

			// Optional: persistent JSONL capture log.
			var persistWorker *persist.Worker
			if cfg.Capture.Persist.Enabled {
				sink, err := persist.Open(cfg.Capture.Persist.Path, cfg.Capture.Persist.FSync)
				if err != nil {
					return fmt.Errorf("init persist: %w", err)
				}
				defer func() { _ = sink.Close() }()
				persistWorker = sink.Subscribe(bus)
				logger.Info("persisting capture events", "path", cfg.Capture.Persist.Path, "fsync", cfg.Capture.Persist.FSync)
			}

			// Optional: announce psxdh on the LAN via mDNS so the console
			// setup doesn't require hunting for the PC's IP.
			if cfg.MDNS.Enabled {
				if port, perr := mdns.PortFromListen(cfg.Proxy.Listen); perr != nil {
					logger.Warn("mdns: skipping announce", "err", perr)
				} else if ann, aerr := mdns.Announce(cfg.MDNS.InstanceName, port); aerr != nil {
					logger.Warn("mdns: announce failed", "err", aerr)
				} else {
					logger.Info("mdns: announcing on LAN", "instance", cfg.MDNS.InstanceName, "service", mdns.ServiceType, "port", port)
					defer ann.Close()
				}
			}

			// Optional: aria2 JSON-RPC handoff client.
			var aria2Client *handoff.Aria2Client
			if cfg.Handoff.Aria2.Enabled {
				aria2Client = handoff.NewAria2(cfg.Handoff.Aria2.RPCURL, cfg.Handoff.Aria2.RPCSecret, nil)
			}

			// Optional: cluster master orchestrator. The PS5 proxies through
			// this node; it enumerates each game's parts and farms them out to
			// slave nodes, then collects the finished parts into the library.
			var clusterMgr *cluster.Manager
			var clusterProber cluster.Prober
			var localDL downloader.Downloader
			if cfg.Cluster.Enabled && cfg.Cluster.Role == "master" {
				if cfg.Cluster.Token == "" {
					cfg.Cluster.Token = genToken()
					logger.Info("generated cluster token")
				}
				clusterMgr = cluster.NewManager(cluster.Deps{
					LibDir:  cfg.Library.Dir,
					Token:   cfg.Cluster.Token,
					Library: idx,
					Logger:  logger,
				})
				clusterProber = cluster.NewHTTPProber(upClient)
				if cfg.Cluster.MasterAsNode {
					dl, engine, derr := buildDownloader(cfg, logger)
					if derr != nil {
						return fmt.Errorf("init master-as-node downloader: %w", derr)
					}
					localDL = dl
					localName := cfg.Cluster.NodeName
					if localName == "" {
						localName = "master"
					}
					_, _ = clusterMgr.AddLocalNode(context.Background(),
						cluster.NewLocalNode(localName, version, engine, cfg.Library.Dir, dl),
						"local",
					)
					logger.Info("cluster: master is also a node", "engine", engine)
				}
			}

			// Optional: embedded web dashboard. Resolve the token now so we can
			// print it in the banner; a non-loopback bind without a token gets
			// one generated.
			var jobDebouncer *jobs.Debouncer
			if jobStore != nil {
				saveFn := func() {
					derived := jobs.DeriveJobs(jobs.DeriveInput{
						Sessions: sessions.Snapshot(),
						Library:  idx,
					})
					if err := jobStore.Save(derived); err != nil {
						logger.Warn("jobs: save state failed", "err", err)
					}
				}
				jobDebouncer = jobs.NewDebouncer(500*time.Millisecond, saveFn)
			}

			var adminSrv *admin.Server
			if cfg.Admin.Enabled {
				token := cfg.Admin.Token
				if token == "" && !cfg.Admin.IsLoopbackBind() {
					token = genToken()
					logger.Info("generated admin token (LAN bind requires auth)")
				}
				cfg.Admin.Token = token
				var onJobsChanged func()
				if jobDebouncer != nil {
					onJobsChanged = jobDebouncer.Trigger
				}
				adminSrv, err = admin.New(admin.Deps{
					Config:        cfg,
					ConfigPath:    persistPath,
					Token:         token,
					Version:       version,
					Bus:           bus,
					Sessions:      sessions,
					Index:         idx,
					DNSHealth:     dnsHealth,
					Aria2:         aria2Client,
					Cluster:       clusterMgr,
					Prober:        clusterProber,
					OnJobsChanged: onJobsChanged,
					Logger:        logger,
				})
				if err != nil {
					return fmt.Errorf("init admin server: %w", err)
				}
			}

			printBanner(cmd, cfg, idx, rules)
			printProxyFooter(cmd, cfg)

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			if cfg.Jobs.ImportOnStart != "" {
				if err := runImportOnStart(ctx, cfg.Jobs, sessions, clusterMgr, clusterProber, logger); err != nil {
					logger.Warn("jobs: import_on_start failed", "path", cfg.Jobs.ImportOnStart, "err", err)
				}
			}
			if jobStore != nil {
				if err := runStateRestore(jobStore, sessions, clusterMgr, idx, logger); err != nil {
					logger.Warn("jobs: state restore failed", "path", jobStore.Path(), "err", err)
				}
			}

			services := []func(context.Context) error{
				lifecycle.GoService(func(ctx context.Context) {
					for {
						select {
						case <-ctx.Done():
							return
						case ev, ok := <-watcher.Events():
							if !ok {
								return
							}
							logger.Debug("library event", "kind", ev.Kind, "path", ev.Path, "size", ev.Size)
							if jobDebouncer != nil {
								jobDebouncer.Trigger()
							}
						}
					}
				}),
				lifecycle.GoService(func(ctx context.Context) { sessions.Run(ctx, bus) }),
				lifecycle.GoService(func(ctx context.Context) { jobSaveOnCapture(ctx, bus, jobDebouncer) }),
				func(ctx context.Context) error {
					if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
						return fmt.Errorf("watcher: %w", err)
					}
					return nil
				},
				func(ctx context.Context) error {
					if err := proxySrv.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
						return fmt.Errorf("proxy: %w", err)
					}
					return nil
				},
			}
			if dnsHealth != nil && cfg.Network.DNS.Health.ReprobeInterval() > 0 {
				interval := cfg.Network.DNS.Health.ReprobeInterval()
				services = append(services, lifecycle.GoService(func(ctx context.Context) {
					reprobeLoop(ctx, dnsHealth, interval, logger)
				}))
			}
			if aria2Client != nil && cfg.Handoff.Aria2.AutoPush {
				services = append(services, lifecycle.GoService(func(ctx context.Context) {
					autoPushAria2(ctx, bus, aria2Client, cfg.Library.Dir, logger)
				}))
			}
			if clusterMgr != nil {
				services = append(services,
					lifecycle.GoService(func(ctx context.Context) {
						enumerateAndSubmit(ctx, bus, clusterMgr, clusterProber, logger, jobDebouncer)
					}),
					lifecycle.GoService(func(ctx context.Context) {
						ticker := time.NewTicker(2 * time.Second)
						defer ticker.Stop()
						for {
							select {
							case <-ctx.Done():
								return
							case <-ticker.C:
								if jobDebouncer != nil {
									jobDebouncer.Trigger()
								}
							}
						}
					}),
					func(ctx context.Context) error {
						if err := clusterMgr.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
							return fmt.Errorf("cluster: %w", err)
						}
						return nil
					},
				)
			}
			if persistWorker != nil {
				services = append(services, func(ctx context.Context) error {
					if err := persistWorker.Run(ctx, func(e error) {
						logger.Warn("persist write failed", "err", e)
					}); err != nil && !errors.Is(err, context.Canceled) {
						return fmt.Errorf("persist: %w", err)
					}
					return nil
				})
			}
			if adminSrv != nil {
				services = append(services, func(ctx context.Context) error {
					if err := adminSrv.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
						return fmt.Errorf("admin: %w", err)
					}
					return nil
				})
			}

			runErr := lifecycle.Run(ctx, lifecycle.Options{Logger: logger, Cancel: cancel}, services...)
			if localDL != nil {
				_ = localDL.Close()
			}
			if runErr != nil {
				return runErr
			}
			logger.Info("shutdown complete")
			return nil
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
	lanAddrs, _ := netinfo.IPv4Addrs()
	primary := netinfo.PrimaryIPv4()

	fmt.Fprintf(out, "psxdh %s\n", version)
	if len(lanAddrs) <= 1 {
		if len(lanAddrs) == 1 {
			fmt.Fprintf(out, "  LAN IP:        %s (%s)\n", lanAddrs[0].IP, lanAddrs[0].Interface)
		} else {
			fmt.Fprintf(out, "  LAN IP:        (none detected)\n")
		}
	} else {
		fmt.Fprintf(out, "  LAN IPs:\n")
		for _, a := range lanAddrs {
			fmt.Fprintf(out, "    %-15s (%s)\n", a.IP, a.Interface)
		}
	}
	fmt.Fprintf(out, "  proxy listen:  %s\n", cfg.Proxy.Listen)
	if cfg.Admin.Enabled {
		fmt.Fprintf(out, "  dashboard:     %s\n", adminURL(cfg.Admin, primary))
	} else {
		fmt.Fprintf(out, "  dashboard:     disabled (set admin.enabled: true)\n")
	}
	if cfg.Cluster.Enabled && cfg.Cluster.Role == "master" {
		fmt.Fprintf(out, "  cluster:       master (token %s)\n", cfg.Cluster.Token)
	}
	fmt.Fprintf(out, "  library dir:   %s\n", idx.Root())
	fmt.Fprintf(out, "  library layout: %s\n", idx.Layout())
	fmt.Fprintf(out, "  match rules:   %d\n", rules.Len())
}

func printProxyFooter(cmd *cobra.Command, cfg *config.Config) {
	out := cmd.OutOrStdout()
	lanAddrs, _ := netinfo.IPv4Addrs()
	primary := netinfo.PrimaryIPv4()
	port := netinfo.PortOf(cfg.Proxy.Listen)

	fmt.Fprintln(out)
	if len(lanAddrs) > 1 {
		fmt.Fprintln(out, "Point your PS5 proxy at the IP on the interface it is connected to")
		fmt.Fprintf(out, "  (e.g. %s:%s for a direct cable; %s:%s on a shared router).\n",
			lanAddrs[0].IP, port, primary, port)
		fmt.Fprintln(out, "Open the dashboard from your phone using any listed IP.")
	} else if primary != "" {
		fmt.Fprintf(out, "Point your console's HTTP proxy at: %s:%s\n", primary, port)
	} else {
		fmt.Fprintf(out, "Point your console's HTTP proxy at: <this PC's LAN IP>:%s\n", port)
	}
	fmt.Fprintln(out, "Press Ctrl-C to shut down.")
	fmt.Fprintln(out)
}

// buildUpstreamClient assembles the *http.Client used to forward
// upstream traffic, with every configurable resilience knob wired in. When
// DNS health ranking is enabled it also returns the *HealthResolver so the
// caller can run background re-probes and surface stats on the dashboard.
func buildUpstreamClient(cfg *config.Config) (*http.Client, *netresolve.HealthResolver, error) {
	resolver, health, err := netresolve.NewFromConfig(netresolve.Config{
		Mode:            cfg.Network.DNS.Mode,
		Resolvers:       cfg.Network.DNS.Resolvers,
		Timeout:         cfg.Network.DNS.Timeout(),
		CacheTTL:        cfg.Network.DNS.CacheTTL(),
		CacheMaxEntries: cfg.Network.DNS.CacheMaxEntries,
		HealthRanking:   cfg.Network.DNS.Health.Enabled,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("init dns resolver: %w", err)
	}

	upCfg := upstream.Config{
		Resolver:    resolver,
		PreferIPv4:  cfg.Network.PreferIPv4,
		DialTimeout: cfg.Network.DialTimeout(),
	}
	if cfg.Network.UpstreamProxy.Enabled {
		upCfg.UpstreamProxy = cfg.Network.UpstreamProxy.URL
		upCfg.UpstreamProxyOnlyForHosts = cfg.Network.UpstreamProxy.OnlyForHosts
	}
	if cfg.Network.Circuit.Enabled {
		upCfg.Breaker = circuit.New(circuit.Config{
			FailureThreshold: cfg.Network.Circuit.FailureThreshold,
			Cooldown:         cfg.Network.Circuit.Cooldown(),
		})
	}
	if cfg.Network.Bandwidth.ForwardBPS > 0 {
		burst := cfg.Network.Bandwidth.BurstBytes
		if burst <= 0 {
			burst = cfg.Network.Bandwidth.ForwardBPS
		}
		upCfg.Bandwidth = bandwidth.NewBucket(cfg.Network.Bandwidth.ForwardBPS, burst)
	}
	client, err := upstream.New(upCfg)
	if err != nil {
		return nil, nil, err
	}
	return client, health, nil
}

// genToken returns a 128-bit hex token for dashboard auth. crypto/rand failure
// is effectively impossible on supported platforms; we panic rather than ship a
// predictable token.
func genToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("psxdh: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// adminURL formats the dashboard URL, substituting the LAN IP for a wildcard
// bind so the printed link is actually clickable, and appending the token.
func adminURL(a config.AdminConfig, lan string) string {
	host, port, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return "http://" + a.Listen + "/"
	}
	if (host == "0.0.0.0" || host == "" || host == "::") && lan != "" {
		host = lan
	}
	u := fmt.Sprintf("http://%s/", net.JoinHostPort(host, port))
	if a.Token != "" {
		u += "?token=" + a.Token
	}
	return u
}

// autoPushAria2 subscribes to the capture bus and forwards every classified PKG
// URL to aria2 so downloads start without any copy-paste. Failures are logged,
// not fatal — the proxy keeps running.
func autoPushAria2(ctx context.Context, bus capture.Bus, client *handoff.Aria2Client, libDir string, logger *slog.Logger) {
	ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	seen := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.URL == nil || !match.IsPushableKind(ev.Kind) {
				continue
			}
			key := ev.URL.String()
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			gid, err := client.AddURI(ctx, key, libDir)
			if err != nil {
				logger.Warn("aria2 auto-push failed", "url", key, "err", err)
				continue
			}
			logger.Info("aria2 auto-push queued", "url", key, "gid", gid)
		}
	}
}

// enumerateAndSubmit subscribes to the capture bus and, the first time it sees
// a PKG part for a title, enumerates the full part series and submits it to the
// cluster manager. Subsequent parts of the same title are ignored. The actual
// enumerate-then-Submit work lives in jobs.ImportFromEvents so that the live
// capture path and the offline JSONL import path share one implementation.
func enumerateAndSubmit(ctx context.Context, bus capture.Bus, mgr *cluster.Manager, prober cluster.Prober, logger *slog.Logger, onChange *jobs.Debouncer) {
	ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	seen := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.URL == nil || !match.IsPushableKind(ev.Kind) {
				continue
			}
			title := ev.Hint.TitleHint
			if title == "" {
				title = ev.URL.Path // fall back to a stable per-asset key
			}
			if _, dup := seen[title]; dup {
				continue
			}
			seen[title] = struct{}{}
			// Inject the synthesised title so jobs.ImportFromEvents can
			// dedupe by it and produce a stable Submit key.
			seedEvent := ev
			seedEvent.Hint.TitleHint = title
			res, err := jobs.ImportFromEvents(ctx, []capture.Event{seedEvent}, jobs.ImportOptions{
				Cluster:   mgr,
				Prober:    prober,
				Enumerate: true,
				Logger:    logger,
			})
			if err != nil {
				logger.Warn("cluster: import failed; will retry", "title", title, "err", err)
				delete(seen, title)
				continue
			}
			if res.Submitted == 0 {
				// Title produced no parts (e.g. enumerate failed and the event
				// had no captured URL). Allow a retry on the next capture.
				delete(seen, title)
			} else if onChange != nil {
				onChange.Trigger()
			}
		}
	}
}

// jobSaveOnCapture triggers a debounced state save when pushable PKG URLs arrive.
func jobSaveOnCapture(ctx context.Context, bus capture.Bus, d *jobs.Debouncer) {
	if d == nil {
		return
	}
	ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.URL != nil && match.IsPushableKind(ev.Kind) {
				d.Trigger()
			}
		}
	}
}

// runStateRestore merges on-disk job state with the live session store and
// re-queues any parts that are not yet in the library. Runs after
// import_on_start so a fresh home capture wins per title when newer.
func runStateRestore(store *jobs.StateStore, sessions *session.Store, mgr *cluster.Manager, idx *library.Index, logger *slog.Logger) error {
	if store == nil {
		return nil
	}
	diskJobs, err := store.Load()
	if err != nil {
		return err
	}
	liveJobs := jobs.DeriveJobs(jobs.DeriveInput{
		Sessions: sessions.Snapshot(),
		Library:  idx,
	})
	merged := jobs.MergeJobs(diskJobs, liveJobs)
	if len(merged) == 0 {
		return nil
	}
	sessions.LoadFromEvents(jobs.EventsFromJobs(merged))
	jobs.ResubmitPending(merged, mgr)
	if err := store.Save(merged); err != nil {
		return err
	}
	logger.Info("jobs: restored state", "titles", len(merged), "path", store.Path())
	return nil
}

// runImportOnStart loads the configured capture log and feeds it through the
// shared import path. Failures are logged but never fatal — a missing file or
// stale state should not block the proxy from coming up.
func runImportOnStart(ctx context.Context, jobsCfg config.JobsConfig, sessions *session.Store, mgr *cluster.Manager, prober cluster.Prober, logger *slog.Logger) error {
	events, err := persist.ReadAll(jobsCfg.ImportOnStart)
	if err != nil {
		return fmt.Errorf("read capture log: %w", err)
	}
	if len(events) == 0 {
		logger.Info("jobs: import_on_start file is empty", "path", jobsCfg.ImportOnStart)
		return nil
	}
	res, err := jobs.ImportFromEvents(ctx, events, jobs.ImportOptions{
		Sessions:  sessions,
		Cluster:   mgr,
		Prober:    prober,
		Enumerate: jobsCfg.ImportEnumerate && prober != nil,
		Logger:    logger,
	})
	if err != nil {
		return err
	}
	logger.Info("jobs: imported capture on start",
		"path", jobsCfg.ImportOnStart,
		"titles", res.Titles,
		"parts", res.Parts,
		"enumerated", res.Enumerated,
		"submitted", res.Submitted,
	)
	return nil
}

// reprobeDefaultHost is the CDN name used to keep resolver health fresh.
const reprobeDefaultHost = "gst.prod.dl.playstation.net"

// reprobeLoop periodically re-resolves a representative CDN host against every
// ranked resolver so the health ordering stays current on an idle link.
func reprobeLoop(ctx context.Context, h *netresolve.HealthResolver, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.Reprobe(ctx, reprobeDefaultHost)
			logger.Debug("dns health re-probe complete", "resolvers", len(h.Snapshot()))
		}
	}
}
