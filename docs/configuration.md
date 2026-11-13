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

The single binary is `psxdh`. Commands:

| Command | Purpose |
| --- | --- |
| `psxdh proxy` | Run the HTTP proxy + library watcher (+ dashboard / cluster master when enabled). |
| `psxdh node` | Run as a cluster slave node with an embedded downloader. |
| `psxdh doctor` | Probe DNS resolvers + PSN CDN reachability. See [network-resilience.md](network-resilience.md#diagnostic-cli). |
| `psxdh probe <url>` | Classify a URL, resolve it, and run a HEAD/GET diagnostic. |
| `psxdh export` | Export pushable URLs from a capture JSONL as txt or aria2 input. |
| `psxdh import` | Import a capture JSONL into a running master's job API. |
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

On start, the proxy prints a banner with every LAN IPv4 address (interface
name in parentheses), the listen address, library root, and rule count. When
the PC has multiple interfaces — common with a PS5 on a direct Ethernet cable
while the PC uses Wi‑Fi — point the console proxy at the IP on the interface
the cable is plugged into. See [README.md](../README.md#ps5-direct-ethernet-cable-to-pc).

```
psxdh v0.1.0
  LAN IPs:
    192.168.2.1    (en8)
    192.168.1.42   (en0)
  proxy listen:  0.0.0.0:8080
  dashboard:     http://192.168.1.42:8081/?token=…
  library dir:   /home/me/Downloads/psxdh
  library layout: basename
  match rules:   8

Point your PS5 proxy at the IP on the interface it is connected to
  (e.g. 192.168.2.1:8080 for a direct cable; 192.168.1.42:8080 on a shared router).
Press Ctrl-C to shut down.
```

---

## config.yaml reference

Defaults target fast PS5 pass-through on networks with unreliable DNS: dashboard
on, DoH+UDP resolvers with health ranking, forward retry, and integrity
verification. Disk-heavy features such as partial cache and the cluster
downloader are opt-in. Override any field in a local `config.yaml`.

Full schema with default values:

```yaml
proxy:
  listen: "0.0.0.0:8080"          # console points here

admin:                            # embedded web dashboard (the GUI)
  enabled: true                   # on by default; set false for headless
  listen: "0.0.0.0:8081"          # LAN-reachable so a phone can open it
  token: ""                       # required for non-loopback binds; auto-
                                  # generated and printed in the banner when empty
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
    - ".aria2"                    # aria2 control file; sibling PKG is in-progress

match:
  ps4: false                      # load embedded PS4 rule pack
  ps5: true                       # load embedded PS5 rule pack (default on)
  rules_dir: ""                   # when set, REPLACES embedded packs with
                                  # the YAML files in this directory

capture:
  log_ignored: false              # publish capture events for KindUnknown URLs
  export_formats:                 # Phase 2 — formats the export package emits
    - "txt"
    - "aria2"
  prefetch_sc_metadata: false     # Phase 2 — fetch first 64 KB of _sc.pkg
                                  # to parse param.json for display metadata
  persist:                        # Append-only JSONL log of capture events
    enabled: false
    path: ""                      # e.g. "~/.psxdh/capture.jsonl"
    fsync: false                  # fsync after every write (slow but durable)

handoff:                          # external-downloader handoff settings
  fdm:
    enabled: true
    fdm_binary: ""                # auto-detect on PATH when empty
    fallback_to_clipboard: true
  aria2:                          # JSON-RPC push to a running aria2c
    enabled: false
    rpc_url: "http://127.0.0.1:6800/jsonrpc"
    rpc_secret: ""                # matches aria2c --rpc-secret
    auto_push: false              # push every captured PKG URL automatically

forward:
  mode: "auto"                    # auto | cache | strict
  passthrough_https: true         # CONNECT tunnel without MITM (do not change)
  retry:                          # Pre-byte-write retry policy. See
                                  # docs/network-resilience.md.
    max_attempts: 4               # 1 = no retry
    initial_backoff_ms: 250
    max_backoff_ms: 4000
    multiplier: 2.0
    jitter: 0.2
  partial_cache:                  # Tee successful forwards/ranges to disk and
                                  # promote when the whole object is cached.
    enabled: false                # true = resumable cache; false = fastest stream-through
    min_size_bytes: 1048576       # 1 MiB; skips tiny manifests
    resume: true                  # continue a dropped .partial next run

network:                          # Upstream-side resilience. See
                                  # docs/network-resilience.md for recipes.
  dns:
    mode: "doh+udp"               # system | udp | doh | doh+udp
    resolvers:                    # UDP first (DoH often blocked); verify with psxdh doctor
      - "1.1.1.1"
      - "9.9.9.9"
      - "8.8.8.8"
      - "8.8.4.4"
      - "178.22.122.100"
      - "185.51.200.2"
      - "https://dns.electrotm.org/dns-query"
      - "https://free.shecan.ir/dns-query"
      - "https://1.1.1.1/dns-query"
      - "https://dns.google/dns-query"
    timeout_ms: 1500              # per-resolver budget
    cache_ttl_s: 300              # fallback TTL (when upstream returns 0)
    cache_max_entries: 4096
    health:                       # rank resolvers by latency/success
      enabled: true
      reprobe_interval_ms: 60000  # 0 = no idle re-probe
  prefer_ipv4: true               # try IPv4 addresses first
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
  crc: false                      # .crc sidecar format flag (Phase-0 locked)
  on_stable: true                 # verify .crc on each library promotion
  require_size_match: false       # refuse to serve a wrong-sized local file

mdns:                             # LAN discovery (ADR 0004)
  enabled: false
  instance_name: "psxdh"

downloader:                       # embedded download engine (ADR 0005)
  engine: "aria2"                 # managed aria2c subprocess
  aria2_binary: ""                # auto-detect on PATH when empty
  allow_http_fallback: false      # true = use built-in HTTP when aria2c missing (dev/CI only)
  rpc_port: 6800
  rpc_secret: ""                  # auto-generated when empty
  connections_per_server: 8
  split: 8
  max_concurrent: 4

cluster:                          # master/slave distributed download (ADR 0005)
  enabled: false
  role: "master"                  # master | slave
  node_name: ""                   # defaults to hostname
  master_as_node: false           # master: also download as a worker node (no loopback HTTP)
  bind: "0.0.0.0:8082"            # cluster/agent API listen
  master_url: ""                  # slave: base URL of the master
  token: ""                       # shared cluster auth; generated on master when empty

log:
  level: "info"                   # debug | info | warn | error
  json: false                     # emit slog records as JSON
```

### Field reference

#### `proxy`

| Field | Required | Description |
| --- | --- | --- |
| `proxy.listen` | yes | `host:port` for the proxy server. Bind to `0.0.0.0` so the console can reach you over LAN. |

#### `admin`

The embedded web dashboard (the GUI): live capture log, per-title session
progress, library state, and a connectivity panel (DNS resolver health + CDN
reachability). Opt-in. Because it is meant to be opened from a phone on the LAN,
it binds beyond loopback by default and requires a token for any non-loopback
bind.

| Field | Required | Description |
| --- | --- | --- |
| `admin.enabled` | no | Start the dashboard. Default true; set false for headless. |
| `admin.listen` | yes (when enabled) | `host:port` for the dashboard. Default `0.0.0.0:8081` so the LAN can reach it. |
| `admin.token` | no | Shared token required for non-loopback binds. Sent via the `X-Psxdh-Token` header or a `?token=` query parameter. Left empty, a token is auto-generated at startup and printed in the banner (with a ready-to-open URL). |
| `admin.auto_open` | no | If true, open the dashboard URL in the default browser on startup. |

#### `library`

| Field | Required | Description |
| --- | --- | --- |
| `library.dir` | yes | Directory `psxdh` watches and serves files from. `~` is expanded. The directory is created if it does not exist. |
| `library.layout` | yes | `basename` (default) or `per-title`. See [library layouts](#library-layouts) below. |
| `library.watch` | no | When false, the watcher is disabled and the index is populated only by the initial walk. |
| `library.stable_settle_ms` | no | Milliseconds of unchanged size before a file is considered stable and added to the index. Default 2000. |
| `library.ignore_suffixes` | no | Filename suffixes that mark in-progress downloads. Files with these suffixes are skipped by the watcher; the final rename triggers a fresh `KindCreated`. `.aria2` is also protected as a built-in sidecar: a final-looking PKG with a sibling `<pkg>.aria2` is not served or indexed until the sidecar disappears. |

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
| `capture.export_formats` | no | Formats the `export` package emits. Supported values are `txt` (one URL per line) and `aria2` (input-file format with `out=`/`dir=`). |
| `capture.prefetch_sc_metadata` | no | Phase 2: when true, background-fetch the first 64 KB of every captured `_sc.pkg` to parse `param.json` for display metadata. |

#### `handoff`

| Field | Required | Description |
| --- | --- | --- |
| `handoff.fdm.enabled` | no | Try the OS-specific "Send to FDM" action on per-URL clicks. |
| `handoff.fdm.fdm_binary` | no | Override the FDM binary path. Empty = auto-detect on `PATH` or the default install location. |
| `handoff.fdm.fallback_to_clipboard` | no | If the deep-link / binary handoff fails, copy the URL to the clipboard instead. |
| `handoff.aria2.enabled` | no | Enable the aria2 JSON-RPC client (the dashboard's "→ aria2" action, and `auto_push`). |
| `handoff.aria2.rpc_url` | when enabled | aria2 RPC endpoint, e.g. `http://127.0.0.1:6800/jsonrpc` (start aria2c with `--enable-rpc`). |
| `handoff.aria2.rpc_secret` | no | Token matching aria2c's `--rpc-secret`. |
| `handoff.aria2.auto_push` | no | Push every captured PKG URL into aria2 automatically (no copy-paste). Files land in `library.dir` with their original basename. |

#### `forward`

| Field | Required | Description |
| --- | --- | --- |
| `forward.mode` | yes | `auto`: forward everything not in the library. `cache`: forward only classified URLs (block `unknown`). `strict`: never forward — return 502 when no local file exists. See [architecture.md](architecture.md#forward-modes). |
| `forward.passthrough_https` | yes | Always true in v1. `CONNECT` is tunnelled as raw TCP; we never MITM HTTPS. Setting this to false is rejected by the validator. |
| `forward.retry.max_attempts` | no | Total attempts (initial + retries). Default `4`; `1` disables retry. |
| `forward.retry.initial_backoff_ms` | no | Wait before the second attempt. |
| `forward.retry.max_backoff_ms` | no | Cap on any single sleep. Must be ≥ `initial_backoff_ms`. |
| `forward.retry.multiplier` | no | Backoff growth factor (default 2.0). |
| `forward.retry.jitter` | no | Fraction in `[0,1]` to randomise each sleep (default 0.2). |
| `forward.partial_cache.enabled` | no | When true, successful GETs are tee'd to disk while streamed to the console. Full `200` responses write from byte zero; `206` Range responses write at their `Content-Range` offsets and promote once the whole object is cached. Disable for maximum stream-through speed when disk writes are the bottleneck. |
| `forward.partial_cache.min_size_bytes` | no | Minimum response size to cache; default 1 MiB. Below this we skip caching so we don't fill the library with tiny manifests. |
| `forward.partial_cache.resume` | no | Continue a `.partial` left behind by a dropped forward (default true). The remainder is fetched with a Range/If-Range request, gated on the upstream validators still matching; any mismatch falls back to a fresh download. See [docs/network-resilience.md](network-resilience.md#cross-run-resumable-downloads). |

See [docs/network-resilience.md](network-resilience.md) for the
behaviour and the pre-write retry invariant.

#### `network`

All `network.*` fields are documented in detail in
[docs/network-resilience.md](network-resilience.md). Highlights:

| Field | Required | Description |
| --- | --- | --- |
| `network.dns.mode` | no | `doh+udp` (default) \| `system` \| `udp` \| `doh`. |
| `network.dns.resolvers` | no | Resolver list. `udp` accepts `host[:port]`; `doh` requires `https://…/dns-query`; `doh+udp` accepts both. |
| `network.dns.timeout_ms` | no | Per-resolver budget (default 1500). |
| `network.dns.cache_ttl_s` | no | Fallback TTL when the upstream returns 0 (default 300). |
| `network.dns.cache_max_entries` | no | LRU cap on the in-memory resolver cache (default 4096). |
| `network.dns.health.enabled` | no | Rank the configured resolvers by observed latency/success so a flapping endpoint stops taxing every lookup. The system resolver stays a fixed last-resort tail. See [docs/network-resilience.md](network-resilience.md#dns-resolver-health-ranking). |
| `network.dns.health.reprobe_interval_ms` | no | Background re-probe cadence to keep ranking fresh on an idle link (default 60000). `0` disables the background probe; live traffic still updates the ranking. |
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

Integrity verification so a corrupt download (common on a bad link) is never
served — a failed install after hours is the worst outcome. A library file that
fails verification is treated as "not local": the proxy forwards upstream so the
console re-fetches correct bytes.

| Field | Required | Description |
| --- | --- | --- |
| `verify.crc` | no | Reserved flag for the `.crc` sidecar format. The CRC32/SHA-256 verifier is implemented; the exact PS5 `.crc` byte format is locked in Phase 0. |
| `verify.on_stable` | no | Run `.crc` verification when the watcher promotes a file. If a `<pkg>.crc` sidecar is present and the digest mismatches, the PKG is marked corrupt and not served. |
| `verify.require_size_match` | no | Refuse to serve a local file whose on-disk size differs from the upstream `Content-Length` observed for the same basename. The cheap guarantee that holds even without a `.crc`. |

#### `mdns`

| Field | Required | Description |
| --- | --- | --- |
| `mdns.enabled` | no | Advertise psxdh on the LAN via mDNS/DNS-SD (`_http._tcp`) so the console-setup step doesn't require hunting for the PC's IP. See [ADR 0004](decisions/0004-mdns.md). |
| `mdns.instance_name` | no | Service instance name (default `psxdh`). |

#### `downloader`

The embedded download engine (ADR 0005). A managed aria2c subprocess; required for
`psxdh node` and for the master when `cluster.master_as_node` is true.

| Field | Required | Description |
| --- | --- | --- |
| `downloader.engine` | no | `aria2` (only engine). |
| `downloader.aria2_binary` | no | Path to `aria2c`; auto-detected on `PATH` when empty. |
| `downloader.allow_http_fallback` | no | When `true`, use the built-in HTTP engine if aria2c is missing (dev/CI only; default `false`). |
| `downloader.rpc_port` | no | aria2 RPC listen port (default 6800). |
| `downloader.rpc_secret` | no | aria2 RPC secret; auto-generated when empty. |
| `downloader.connections_per_server` | no | aria2 `-x` (default 8). |
| `downloader.split` | no | aria2 `-s` (default 8). |
| `downloader.max_concurrent` | no | aria2 `-j` (default 4). |

#### `cluster`

Master/slave distributed download (ADR 0005). The **master** is the node the PS5
proxies through; **slaves** (`psxdh node`) download assigned parts and hand them
back. See the cluster quick-start in [README.md](../README.md).

| Field | Required | Description |
| --- | --- | --- |
| `cluster.enabled` | no | Turn on cluster behaviour. Default false so zero-config proxy mode keeps all bandwidth for the console. |
| `cluster.role` | when enabled | `master` or `slave`. |
| `cluster.node_name` | no | Node label in the dashboard; defaults to the hostname. |
| `cluster.master_as_node` | master | When true, the master also runs the embedded downloader and participates as a download worker, writing parts directly into `library.dir`. |
| `cluster.bind` | when enabled | `host:port` for the cluster/agent API. |
| `cluster.master_url` | slave | Base URL of the master (e.g. `http://192.168.1.10:8082`). |
| `cluster.token` | required for slaves | Shared cluster auth; generated on the master when empty (printed in the banner). Slaves must set the same value before startup. |

#### `jobs`

Portable capture workflow: import a home `capture.jsonl`, optionally enumerate
the full PKG part series from the CDN, and persist resumable job state. See
README "Capture at home, download at work" and
[docs/examples/config.home.yaml](examples/config.home.yaml) /
[docs/examples/config.work.yaml](examples/config.work.yaml).

| Field | Required | Description |
| --- | --- | --- |
| `jobs.import_on_start` | no | JSONL path imported when the master starts. Empty disables. |
| `jobs.import_enumerate` | no | Probe the CDN for the full `_0.._N` series on import. Default true. |
| `jobs.state_path` | no | JSON snapshot of job progress. Empty disables persistence. |

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
