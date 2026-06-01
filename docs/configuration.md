# Configuration

`psxdh` is configured through three layers, applied in order:

1. **Built-in defaults** (`config.Default()`).
2. **YAML config file** (`--config path/to/config.yaml`). Fields in the file
   overlay defaults; missing fields keep their default value.
3. **CLI flags** (`--listen`, `--library`, `--log-level`). Applied after the
   YAML file, so flags always win.

This document is the canonical reference. The on-disk schema is YAML and is
mirrored exactly by `internal/config/config.go`.

---

## CLI

The single binary is `psxdh`. Commands shipping in Phase 1:

| Command | Purpose |
| --- | --- |
| `psxdh proxy` | Run the HTTP proxy + library watcher until interrupted. |
| `psxdh doctor` | Probe DNS resolvers + PSN CDN reachability. See [network-resilience.md](network-resilience.md#diagnostic-cli). |
| `psxdh probe <url>` | Classify a URL, resolve it, and run a HEAD/GET diagnostic. |
| `psxdh version` | Print the build version. |
| `psxdh --help` | List subcommands. |

### `psxdh proxy` flags

| Flag | Description | Equivalent YAML |
| --- | --- | --- |
| `--config` | Path to a YAML config file. When empty, only defaults + flags apply. | n/a |
| `--listen` | Override the proxy listen address (host:port). | `proxy.listen` |
| `--library` | Override the library directory. | `library.dir` |
| `--log-level` | Override the slog level (`debug`, `info`, `warn`, `error`). | `log.level` |

Examples:

```bash
# Defaults only (proxy on 0.0.0.0:8080, library at ~/Downloads/psxdh).
psxdh proxy

# Custom port and library.
psxdh proxy --listen 0.0.0.0:9000 --library /srv/psxdh/library

# Production config file.
psxdh proxy --config /etc/psxdh/config.yaml

# Config file + per-run override.
psxdh proxy --config config.yaml --log-level debug
```

On start, the proxy prints a banner with the LAN IP, listen address, library
root, and rule count so you can verify your console will be able to reach it:

```
psxdh v0.1.0
  LAN IP:        192.168.1.42
  proxy listen:  0.0.0.0:8080
  admin listen:  http://127.0.0.1:8081/
  library dir:   /home/me/Downloads/psxdh
  library layout: basename
  match rules:   8

Point your console's HTTP proxy at: 192.168.1.42:8080
Press Ctrl-C to shut down.
```

---

## config.yaml reference

Full schema with default values:

```yaml
proxy:
  listen: "0.0.0.0:8080"          # console points here

admin:
  listen: "127.0.0.1:8081"        # dashboard + REST/WebSocket (Phase 2)
  auto_open: false                # open the dashboard URL on startup

library:
  dir: "~/Downloads/psxdh"        # ~ is expanded to the user's home dir
  layout: "basename"              # basename | per-title
  watch: true                     # fsnotify on library.dir
  stable_settle_ms: 2000          # partial-write debounce window
  ignore_suffixes:                # filenames matching any of these are
    - ".part"                     # ignored until renamed to the final name
    - ".fdmdownload"
    - ".tmp"
    - ".crdownload"

match:
  ps4: true                       # load embedded PS4 rule pack
  ps5: true                       # load embedded PS5 rule pack
  rules_dir: ""                   # when set, REPLACES embedded packs with
                                  # the YAML files in this directory

capture:
  log_ignored: false              # publish capture events for KindUnknown URLs
  export_formats:                 # Phase 2 — formats the export package emits
    - "txt"
    - "fdm"
    - "aria2"
  prefetch_sc_metadata: false     # Phase 2 — fetch first 64 KB of _sc.pkg
                                  # to parse param.json for display metadata
  persist:                        # Append-only JSONL log of capture events
    enabled: false
    path: ""                      # e.g. "~/.psxdh/capture.jsonl"
    fsync: false                  # fsync after every write (slow but durable)

handoff:                          # Phase 2 — "Send to FDM" handoff settings
  fdm:
    enabled: true
    fdm_binary: ""                # auto-detect on PATH when empty
    fallback_to_clipboard: true

forward:
  mode: "auto"                    # auto | cache | strict
  passthrough_https: true         # CONNECT tunnel without MITM (do not change)
  retry:                          # Pre-byte-write retry policy. See
                                  # docs/network-resilience.md.
    max_attempts: 1               # 1 = no retry (default, today's behaviour)
    initial_backoff_ms: 200
    max_backoff_ms: 5000
    multiplier: 2.0
    jitter: 0.2
  partial_cache:                  # Tee successful forwards to disk and
                                  # promote them into the library on success.
    enabled: false
    min_size_bytes: 1048576       # 1 MiB; skips tiny manifests

network:                          # Upstream-side resilience. See
                                  # docs/network-resilience.md for recipes.
  dns:
    mode: "system"                # system | udp | doh | doh+udp
    resolvers: []                 # plain "1.1.1.1" or "https://…/dns-query"
    timeout_ms: 1500              # per-resolver budget
    cache_ttl_s: 300              # fallback TTL (when upstream returns 0)
    cache_max_entries: 4096
  prefer_ipv4: false              # try IPv4 addresses first
  dial_timeout_ms: 10000
  upstream_proxy:                 # HTTP/SOCKS5 chain (e.g. local VPN)
    enabled: false
    url: ""                       # http://, https://, socks5://, socks5h://
    only_for_hosts: []            # empty = all hosts go through the proxy
  circuit:                        # Per-host failure breaker
    enabled: false
    failure_threshold: 5
    cooldown_ms: 30000
  bandwidth:                      # Throttle forward path throughput
    forward_bps: 0                # 0 = unlimited
    burst_bytes: 0                # 0 = equal to forward_bps

verify:
  crc: false                      # Phase-0 sidecar verification scaffold

log:
  level: "info"                   # debug | info | warn | error
  json: false                     # emit slog records as JSON
```

### Field reference

#### `proxy`

| Field | Required | Description |
| --- | --- | --- |
| `proxy.listen` | yes | `host:port` for the proxy server. Bind to `0.0.0.0` so the console can reach you over LAN. |

#### `admin` (Phase 2)

| Field | Required | Description |
| --- | --- | --- |
| `admin.listen` | yes | `host:port` for the embedded dashboard. Default loopback-only — only bind to `0.0.0.0` if you understand the security implications. |
| `admin.auto_open` | no | If true, open the dashboard URL in the default browser on startup. |

#### `library`

| Field | Required | Description |
| --- | --- | --- |
| `library.dir` | yes | Directory `psxdh` watches and serves files from. `~` is expanded. The directory is created if it does not exist. |
| `library.layout` | yes | `basename` (default) or `per-title`. See [library layouts](#library-layouts) below. |
| `library.watch` | no | When false, the watcher is disabled and the index is populated only by the initial walk. |
| `library.stable_settle_ms` | no | Milliseconds of unchanged size before a file is considered stable and added to the index. Default 2000. |
| `library.ignore_suffixes` | no | Filename suffixes that mark in-progress downloads. Files with these suffixes are skipped by the watcher; the final rename triggers a fresh `KindCreated`. |

#### `match`

| Field | Required | Description |
| --- | --- | --- |
| `match.ps4` | no | Load the embedded PS4 rule pack. Ignored when `match.rules_dir` is set. |
| `match.ps5` | no | Load the embedded PS5 rule pack. Ignored when `match.rules_dir` is set. |
| `match.rules_dir` | no | Replace embedded defaults with `*.yaml` files from this directory. Files are loaded in lexical order — name files `00-…`, `10-…` to control priority. See [cdn-patterns.md](cdn-patterns.md#overriding-the-default-rules). |

#### `capture`

| Field | Required | Description |
| --- | --- | --- |
| `capture.log_ignored` | no | Publish capture events even for `KindUnknown` URLs. Default false to keep the dashboard signal-to-noise high. |
| `capture.export_formats` | no | Phase 2: list of formats the `export` package emits. Currently honoured only for inventory; only `txt` is implemented today. |
| `capture.prefetch_sc_metadata` | no | Phase 2: when true, background-fetch the first 64 KB of every captured `_sc.pkg` to parse `param.json` for display metadata. |

#### `handoff` (Phase 2)

| Field | Required | Description |
| --- | --- | --- |
| `handoff.fdm.enabled` | no | Try the OS-specific "Send to FDM" action on per-URL clicks. |
| `handoff.fdm.fdm_binary` | no | Override the FDM binary path. Empty = auto-detect on `PATH` or the default install location. |
| `handoff.fdm.fallback_to_clipboard` | no | If the deep-link / binary handoff fails, copy the URL to the clipboard instead. |

#### `forward`

| Field | Required | Description |
| --- | --- | --- |
| `forward.mode` | yes | `auto`: forward everything not in the library. `cache`: forward only classified URLs (block `unknown`). `strict`: never forward — return 502 when no local file exists. See [architecture.md](architecture.md#forward-modes). |
| `forward.passthrough_https` | yes | Always true in v1. `CONNECT` is tunnelled as raw TCP; we never MITM HTTPS. Setting this to false would break PSN login and is rejected by the validator in spirit (kept as a config knob for transparency). |
| `forward.retry.max_attempts` | no | Total attempts (initial + retries). `1` (default) disables retry. |
| `forward.retry.initial_backoff_ms` | no | Wait before the second attempt. |
| `forward.retry.max_backoff_ms` | no | Cap on any single sleep. Must be ≥ `initial_backoff_ms`. |
| `forward.retry.multiplier` | no | Backoff growth factor (default 2.0). |
| `forward.retry.jitter` | no | Fraction in `[0,1]` to randomise each sleep (default 0.2). |
| `forward.partial_cache.enabled` | no | When true, successful non-Range GETs are tee'd to disk and atomically promoted into the library. |
| `forward.partial_cache.min_size_bytes` | no | Minimum response size to cache; default 1 MiB. Below this we skip caching so we don't fill the library with tiny manifests. |

See [docs/network-resilience.md](network-resilience.md) for the
behaviour and the pre-write retry invariant.

#### `network`

All `network.*` fields are documented in detail in
[docs/network-resilience.md](network-resilience.md). Highlights:

| Field | Required | Description |
| --- | --- | --- |
| `network.dns.mode` | no | `system` (default) \| `udp` \| `doh` \| `doh+udp`. |
| `network.dns.resolvers` | no | Resolver list. `udp` accepts `host[:port]`; `doh` requires `https://…/dns-query`; `doh+udp` accepts both. |
| `network.dns.timeout_ms` | no | Per-resolver budget (default 1500). |
| `network.dns.cache_ttl_s` | no | Fallback TTL when the upstream returns 0 (default 300). |
| `network.dns.cache_max_entries` | no | LRU cap on the in-memory resolver cache (default 4096). |
| `network.prefer_ipv4` | no | Try IPv4 addresses before IPv6. |
| `network.dial_timeout_ms` | no | Single-dial timeout (default 10000). |
| `network.upstream_proxy.enabled` | no | Route forward traffic through an HTTP/SOCKS5 proxy. |
| `network.upstream_proxy.url` | when enabled | `http://`, `https://`, `socks5://`, or `socks5h://`. |
| `network.upstream_proxy.only_for_hosts` | no | When set, dial these hosts (and subdomains) through the proxy; others go direct. |
| `network.circuit.enabled` | no | Per-host failure breaker. |
| `network.circuit.failure_threshold` | no | Consecutive failures that open the breaker (default 5). |
| `network.circuit.cooldown_ms` | no | Wait before half-opening (default 30000). |
| `network.bandwidth.forward_bps` | no | Throughput cap on the forward path in bytes/sec (`0` = unlimited). |
| `network.bandwidth.burst_bytes` | no | Token-bucket burst size; `0` defaults to `forward_bps`. |

#### `verify`

| Field | Required | Description |
| --- | --- | --- |
| `verify.crc` | no | Reserved. The `.crc` sidecar parser is a scaffolded stub until Phase 0 captures the real PS5 format. Enabling this today is a no-op. |

#### `capture.persist`

| Field | Required | Description |
| --- | --- | --- |
| `capture.persist.enabled` | no | Append every capture event as one JSON object per line to `path`. |
| `capture.persist.path` | when enabled | Destination file; `~` is expanded; parent directories are created. |
| `capture.persist.fsync` | no | `fsync()` after every write. Slow but durable across power loss. |

#### `log`

| Field | Required | Description |
| --- | --- | --- |
| `log.level` | yes | One of `debug`, `info`, `warn`, `error`. |
| `log.json` | no | When true, emit slog records as JSON to stderr. Useful for shipping logs to a structured log aggregator. |

---

## Library layouts

The library resolver maps a captured URL to a local file via the basename.
Two layouts are supported:

### `basename` (default)

```
library/
├── PPSA01234_00-FAKEPKG_0.pkg
├── PPSA01234_00-FAKEPKG_1.pkg
└── UP1234-CUSA12345_00-OTHERPKG_0.pkg
```

Every file in the tree (recursive walk) is keyed by its basename. This
matches how FDM, aria2, curl, and most browsers save by default. Subfolders
are allowed — they just don't change the lookup.

Limitation: when two titles share a basename (rare in practice, since Sony's
PKG names include the title id), the resolver returns "miss" rather than
auto-picking. Use `per-title` if you hit this.

### `per-title`

```
library/
├── PPSA01234_00-FAKEPKG/
│   ├── PPSA01234_00-FAKEPKG_0.pkg
│   └── PPSA01234_00-FAKEPKG_1.pkg
└── UP1234-CUSA12345_00-OTHERPKG/
    └── UP1234-CUSA12345_00-OTHERPKG_0.pkg
```

Same recursive walk, but `Resolve` additionally requires that the URL's
title-id (`PPSA…`, `CUSA…`, or `UP1234-CUSA…`) appears somewhere in the
local path. Use this when you're managing multiple titles in parallel and
want to be defensive about collisions.

---

## Hot reload

Configuration hot reload is **not** implemented in Phase 1. Restart `psxdh`
to pick up changes. The validation layer will reject malformed config at
startup, so an invalid YAML file fails the start rather than silently
ignoring a typo.

---

## Local overrides

A `config.local.yaml` at the repo root is git-ignored by default
(see `.gitignore`). Useful for keeping a developer-specific library path or
log level out of version control.

```bash
psxdh proxy --config config.local.yaml
```
