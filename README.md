# psxdownloadhelper

A cross-platform **Go** HTTP proxy for PlayStation owners. `psxdh` sits
between a PS5 / PS4 and Sony's CDN, captures the official download URLs as
the console requests them, hands those URLs to an external downloader
(Free Download Manager, aria2, IDM, …) on a PC, watches a library folder
for the downloaded files, and serves them back to the console over LAN with
full HTTP `Range` support.

It is a clean-room MIT reimplementation in the spirit of the original
[KOPElan/PSX-Download-Helper](https://github.com/KOPElan/PSX-Download-Helper),
with **first-class PS5 support** and a deliberate **no built-in downloader**
design — the PC user keeps full control via FDM.

> **Status: Phase 1 + 2.5 complete; Phase 2 + 3 largely landed.** The proxy,
> library watcher, classifier, CLI, and the full network-resilience stack
> (custom DNS, retry, upstream proxy chain, partial cache, breaker,
> bandwidth, JSONL persist, diagnostics) are wired up and tested. Now added:
> **cross-run resumable downloads**, **integrity verification** (don't serve
> a corrupt file), **DNS resolver health ranking**, an embedded **web
> dashboard** + session tracker, **aria2** export + JSON-RPC push, **mDNS**
> LAN announce, and release tooling (CI matrix, GoReleaser, golangci-lint).
> Hardware validation against current PS5/PS4 firmware (Phase 0) is still
> ahead. See [docs/roadmap.md](docs/roadmap.md) for the phase breakdown and
> [docs/network-resilience.md](docs/network-resilience.md) for the resilience
> knobs.
>
> **Also now:** an **embedded aria2 downloader** and a **master/slave
> distributed download cluster** that combines several machines' links for one
> game, managed from the dashboard. See
> [ADR 0005](docs/decisions/0005-distributed-downloader.md).

---

## How it works

```
PS5 / PS4   ───HTTP proxy───►   psxdh  ───forward (no local file)───►   Sony CDN
                                  │                                       │
                                  ▼                                       │
                            captured URL                                  │
                                  │                                       │
                          (you copy → FDM)                                │
                                  ▼                                       │
                              FDM ───download───────────────────────────►─┘
                                  │
                                  ▼
                          library folder
                                  │
                       fsnotify "file stable"
                                  ▼
PS5 / PS4   ───retry with Range───►   psxdh ───serve from disk (206)───► console
```

For the full design see [docs/architecture.md](docs/architecture.md). For
the URL patterns the proxy classifies, see
[docs/cdn-patterns.md](docs/cdn-patterns.md).

---

## Quick start

### Build

```bash
go build ./cmd/psxdh
```

### Run

```bash
# Zero-config: PS5 rules, dashboard, cluster master (downloads on this PC),
# Iran-friendly DNS/retry/partial-cache. Proxy on 0.0.0.0:8080.
./psxdh proxy

# Or with overrides:
./psxdh proxy --listen 0.0.0.0:8080 --library ~/Downloads/psxdh

# Or with a config file (to disable cluster, change DNS, etc.):
./psxdh proxy --config config.yaml
```

**Prerequisite (cluster / `psxdh node`):** install `aria2c` on your PATH before
starting. The embedded cluster downloader requires it by default.

| OS | Install |
| --- | --- |
| macOS | `brew install aria2` |
| Debian/Ubuntu | `sudo apt install aria2` |
| Windows | `winget install aria2.aria2` |

Run `psxdh doctor` to verify DNS reachability and that `aria2c` is found. For
local dev/CI only, set `downloader.allow_http_fallback: true` to use the
built-in HTTP engine when aria2c is missing.

The startup banner prints your LAN IP(s), dashboard URL, and cluster token.
When your PC has more than one network interface (e.g. Wi‑Fi plus a direct
Ethernet cable to the PS5), every address is listed — use the IP on the
interface your console is plugged into:

```
psxdh dev
  LAN IPs:
    192.168.2.1    (en8)
    192.168.1.42   (en0)
  proxy listen:  0.0.0.0:8080
  dashboard:     http://192.168.1.42:8081/?token=4f3c…
  cluster:       master (token abc123…)
  library dir:   /home/me/Downloads/psxdh
  library layout: basename
  match rules:   6

Point your PS5 proxy at the IP on the interface it is connected to
  (e.g. 192.168.2.1:8080 for a direct cable; 192.168.1.42:8080 on a shared router).
Open the dashboard from your phone using any listed IP.
Press Ctrl-C to shut down (graceful shutdown, up to 15s for in-flight work).
```

### Point your console at the proxy

- **PS5:** Settings → Network → Settings → Set Up Internet Connection →
  Advanced Settings → Proxy Server → Use → enter the PC's LAN IP and the
  proxy port (default 8080).
- **PS4:** Settings → Network → Set Up Internet Connection → Custom →
  Proxy Server → Use → enter the PC's LAN IP and the proxy port.

Use a wired LAN connection on both ends if possible — wireless will work but
caps your local-serve throughput.

### PS5 direct Ethernet (cable to PC)

Use this when the PS5 is plugged straight into your PC with an Ethernet cable
and the PC uses Wi‑Fi (or another adapter) for internet:

```
Internet ←── Wi‑Fi ←── PC ←── Ethernet cable ←── PS5
```

psxdh needs no special config (`proxy.listen: 0.0.0.0:8080` already listens on
the cable). The OS must assign addresses and route traffic:

1. **macOS:** System Settings → General → Sharing → **Internet Sharing** —
   share **from Wi‑Fi** **to Ethernet**. The Mac is usually `192.168.2.1` on
   that link; the PS5 gets an address via DHCP.
2. **Windows:** Settings → Network → **Mobile hotspot** or Internet Connection
   Sharing — share Wi‑Fi to the Ethernet adapter.
3. **Run** `./psxdh proxy` and read the **LAN IPs** line in the banner or
   dashboard header.
4. **PS5 proxy:** Settings → Network → … → Proxy Server → Use → **Ethernet IP**
   (e.g. `192.168.2.1:8080`), not the Wi‑Fi IP.
5. **Firewall:** allow inbound TCP **8080** (proxy) and **8081** (dashboard).
6. **Phone dashboard:** open the URL using the **Wi‑Fi** IP from the list.

See also the dashboard header, which lists every interface IP and the proxy
port.

### Capture at home, download at work

Capture URLs on your home network without downloading the full game, then
resume on a work machine (optionally using the cluster to combine several PCs'
bandwidth):

**At home (capture only):**

```yaml
capture:
  persist:
    enabled: true
    path: "~/psxdh/capture.jsonl"
    fsync: true
cluster:
  enabled: false
forward:
  partial_cache:
    enabled: false   # don't tee large PKGs to disk at home
```

1. Run `./psxdh proxy --config docs/examples/config.home.yaml` (or use the
   dashboard Settings panel).
2. Point the PS5 proxy at your home PC and start the download — psxdh records
   CDN URLs to `capture.jsonl` without saving multi-gigabyte files locally.
3. Copy `capture.jsonl` to your work machine (USB, cloud sync, etc.).

**At work (cluster download):**

```yaml
jobs:
  import_on_start: "~/psxdh/capture.jsonl"
  import_enumerate: true
  state_path: "~/psxdh/jobs/state.json"
cluster:
  enabled: true
  role: master
  master_as_node: true
```

1. Start the master: `./psxdh proxy --config docs/examples/config.work.yaml`
   — it imports the capture file on startup and queues parts for download.
2. Attach slaves: `./psxdh node --config config.work.yaml` on other work PCs.
3. Or import later via the dashboard **Import capture** button, or:

```bash
psxdh import --from capture.jsonl --url http://192.168.1.10:8081 --token … --enumerate
```

**Offline export** (no running proxy):

```bash
psxdh export --from capture.jsonl --format aria2 --out game.aria2.txt --library-dir ~/Downloads/psxdh
```

Job state in `jobs.state_path` survives restarts — the Sessions panel shows
progress even before the PS5 reconnects.

Example profiles: [docs/examples/config.home.yaml](docs/examples/config.home.yaml),
[docs/examples/config.work.yaml](docs/examples/config.work.yaml).

### Use a downloader (FDM, aria2, …)

1. Start a download on the console. The URL appears in the `psxdh` log.
2. Copy the URL into FDM (or your downloader of choice).
3. Save the file into your `library.dir` with its original filename. FDM's
   default save layout works as-is.
4. The library watcher will detect the file, wait for it to settle, and add
   it to the index.
5. Resume the download on the console. The proxy will now serve the file
   locally with `206 Partial Content`.

---

## Configuration

`psxdh` is configured by `config.yaml` (see
[docs/configuration.md](docs/configuration.md) for the full reference). With no
config file, built-in defaults are tuned for **PS5 on Iranian networks**
(dashboard, cluster master + `master_as_node`, Shecan-style DNS, retry, partial
cache, verify). Override only what you need:

```yaml
# Headless / FDM-only: turn off dashboard and embedded downloader.
admin:
  enabled: false
cluster:
  enabled: false

# PS4 titles alongside PS5:
match:
  ps4: true
  ps5: true
```

CLI flags (`--listen`, `--library`, `--log-level`, `--config`) override the
YAML file.

---

## Surviving unstable networks

The built-in defaults already include the Iran-friendly DNS, retry, partial
cache, DNS health ranking, and integrity verification stack. Verify resolvers
after first start:

```bash
psxdh doctor                                    # all configured resolvers
psxdh probe https://gst.prod.dl.playstation.net/.../x.pkg
```

If you use a local VPN, route only PSN CDN traffic through it (not enabled by
default — your SOCKS5 port varies by app):

```yaml
network:
  upstream_proxy:
    enabled: true
    url: "socks5://127.0.0.1:1080"
    only_for_hosts:
      - "prod.dl.playstation.net"
      - "ww.prod.dl.playstation.net"
```

See [docs/network-resilience.md](docs/network-resilience.md) for the full knob
reference and additional recipes.

---

## Web dashboard (GUI)

An embedded dashboard shows the live capture log, per-title session progress,
library state, a connectivity panel (DNS health + CDN reachability), and a
**Settings** panel for editing config without touching raw YAML. Settings
are grouped by section (proxy, library, network, cluster, jobs, etc.) with a typed
control and helper text for every field. Use **Save** on a section or **Save all**
to write changes; restart `psxdh` to apply them. Without `--config`, saves go to
`~/.config/psxdh/config.yaml` (created on first save; loaded automatically once
it exists).

The **Sessions** panel includes per-title **Export aria2** / **Export txt**
buttons, header **Export all** actions, and **Import capture** to upload a
home `capture.jsonl` into a running master.

```yaml
admin:
  enabled: true
  listen: "0.0.0.0:8081"   # LAN-reachable
  # token: ""              # auto-generated and printed in the banner when empty
```

On startup the banner prints a ready-to-open URL with the token:

```
  dashboard:     http://192.168.1.42:8081/?token=4f3c…
```

Any non-loopback bind requires the token (sent via the `?token=` query or the
`X-Psxdh-Token` header), so a casual device on the LAN can't drive your proxy.

## aria2 handoff (best for throttled links)

aria2's segmented, resumable downloads are ideal where bandwidth is throttled.
Start `aria2c --enable-rpc`, then let psxdh push captured URLs straight in — no
copy-paste:

```yaml
handoff:
  aria2:
    enabled: true
    rpc_url: "http://127.0.0.1:6800/jsonrpc"
    # rpc_secret: "…"      # matches aria2c --rpc-secret
    auto_push: true        # queue every captured PKG automatically
```

The dashboard also has a per-part "→ aria2" button. Files land in `library.dir`
with their original basename, so the watcher promotes them automatically. You
can still export a plain list (`psxdh` emits aria2 input-file format) and run
`aria2c -i list.txt` yourself.

---

## CLI

| Command | Purpose |
| --- | --- |
| `psxdh proxy` | Run the HTTP proxy + library watcher (+ dashboard / cluster master if enabled). |
| `psxdh node` | Run as a cluster slave: embedded downloader + agent the master drives. |
| `psxdh doctor` | Probe configured DNS resolvers and PSN CDN reachability. |
| `psxdh probe <url>` | Classify + resolve + HEAD/GET a single URL. |
| `psxdh export` | Export pushable URLs from a capture JSONL (aria2 or txt). |
| `psxdh import` | POST a capture JSONL to a running master's import API. |
| `psxdh version` | Print the build version. |
| `psxdh --help` | List subcommands and flags. |

The `doctor` and `probe` commands are the diagnostic surface for the
network-resilience stack — see
[docs/network-resilience.md](docs/network-resilience.md). Both are
read-only and safe to run while the proxy is serving. Session tracking,
URL export/import, and job persistence are available through the web dashboard
and the `export` / `import` CLI commands.

---

## Distributed download cluster (combine several machines' links)

Where one link is slow, split a game's parts across several machines on the LAN
so their bandwidth adds up. One **master** (the node the PS5 proxies through)
enumerates the parts and farms them out to **slave** nodes; finished parts come
back to the master, which serves them to the console. See
[ADR 0005](docs/decisions/0005-distributed-downloader.md).

This is **on by default** when you run `./psxdh proxy` with no config file
(dashboard + cluster master + `master_as_node`). To add remote slaves, keep
the master running and start nodes on other machines. Optional overrides:

```yaml
cluster:
  bind: "0.0.0.0:8082"     # cluster/agent API (default)
downloader:
  engine: "aria2"            # requires aria2c on PATH (see prerequisite table)
  # allow_http_fallback: true  # dev/CI only — skip aria2 requirement
```

On each slave machine:

```bash
psxdh node --master http://<master-ip>:8082 --bind 0.0.0.0:8082
# (use the master's cluster token: set cluster.token to the same value on both)
```

Slaves announce themselves via mDNS, so the master's dashboard can **discover**
them, or you can **add one by IP**. The dashboard shows per-node progress and
speed, the game's overall progress, and the **combined speed across all nodes**.
You can also move a finished part to the master manually (USB/SSD) — drop it in
the library folder and the master treats that part as done.

---

## What `psxdh` does **not** do

- It does **not** require an external downloader for the cluster: it embeds a
  managed **aria2c** (ADR 0005). For non-cluster use you can still hand URLs to
  FDM / aria2 / IDM / curl, or push to a running aria2 over RPC.
- It does **not** MITM HTTPS. `CONNECT` is tunnelled as raw TCP. PSN
  authentication, store browsing, and login traffic stay encrypted.
- It is **not** a tool for piracy, licence bypass, or downloading content
  you don't own. It is for **content you already own** on PSN — same intent
  as the original PSX Download Helper.

See [docs/research.md](docs/research.md) for the legal / licence posture
and prior-art landscape.

---

## Testing

```bash
go test ./...
```

The test suite covers:

- `internal/match` — URL classification against PS4 + PS5 rule packs.
- `internal/library` — index, watcher (partial-write debounce), resolver.
- `internal/proxy` — absolute-URI forwarding, `Range` pass-through,
  `CONNECT` tunnel against an `httptest.NewTLSServer`, `auto` / `cache` /
  `strict` modes, hop-by-hop header stripping, capture publication,
  forward retry, partial cache promotion.
- `internal/serve` — RFC 7233 Range cases.
- `internal/netresolve` — DoH + UDP + cache + multi-resolver fallback.
- `internal/retry`, `internal/circuit`, `internal/bandwidth`,
  `internal/upstream` — the resilience layer.
- `internal/persist`, `internal/verify`, `internal/doctor` — JSONL log,
  CRC framework + watcher integrity gate, diagnostic checks.
- `internal/proxy` (partial cache) — cross-run resume, validator mismatch
  fallback, and the corrupt/wrong-size serve gate.
- `internal/netresolve` — DoH + UDP + cache + multi-resolver fallback +
  health ranking.
- `internal/admin`, `internal/session`, `internal/handoff` — dashboard API
  + token auth + SSE, session aggregation, aria2 JSON-RPC client.
- `internal/export`, `internal/capture`, `internal/config`, `internal/mdns`
  — unit tests.
- `e2e/` — full forward → watcher → serve cycle against a fake Sony-CDN-
  shaped upstream, including the multi-part FDM scenario.

### Windows + Defender Application Control note

On some Windows installs, Microsoft Defender Application Control will
briefly block freshly-built test binaries that `go test` writes into
obscured temp paths (`%LocalAppData%\Temp\go-buildXXXXX\...`). When that
happens — typically for one or two specific packages, non-deterministic
— rerun with the bundled wrapper:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/run-tests.ps1
```

It builds each test binary to `./.testbin/<pkg>.test.exe` and retries
the launch up to 5 times with a short delay if Defender refuses the
first attempt. The wrapper accepts the same package patterns as
`go test`: `scripts/run-tests.ps1 ./internal/bandwidth/...`.

See the testing strategy in
[docs/architecture.md](docs/architecture.md#testing-strategy).

---

## Documentation map

| Document | What's in it |
| --- | --- |
| [docs/architecture.md](docs/architecture.md) | Repository layout, package responsibilities, request pipeline, session model, testing strategy. |
| [docs/cdn-patterns.md](docs/cdn-patterns.md) | PS4 / PS5 CDN URL shapes and the rule packs that classify them. |
| [docs/configuration.md](docs/configuration.md) | `config.yaml` reference + CLI flags + library layouts. |
| [docs/network-resilience.md](docs/network-resilience.md) | DNS (+ health ranking) + retry + proxy-chain + partial cache (+ cross-run resume) + diagnostics. |
| [docs/roadmap.md](docs/roadmap.md) | Vision, user flows, phases, risks, open questions, v1.0 definition of done. |
| [docs/research.md](docs/research.md) | Prior-art tools, dependency budget, legal / licence notes, external references. |
| [docs/decisions/](docs/decisions/) | Architecture Decision Records (0001 proxy stack, 0002 dep budget, 0003 network resilience, 0004 mDNS dependency, 0005 embedded downloader + cluster). |

---

## Licence

MIT. See [LICENSE](LICENSE).

Not affiliated with Sony Interactive Entertainment. You are responsible for
compliance with PSN Terms of Service and any local laws when using this
tool.
