# ADR 0005: Embedded downloader + master/slave distributed download

- Status: Accepted
- Date: 05/06/2026
- Deciders: project owner
- Supersedes: partially reverses the "no built-in downloader" non-goal in [roadmap.md](../roadmap.md)

## Context

On a slow or throttled link, one machine's WAN bandwidth is the bottleneck. The
owner wants to spread a single game's download across several machines on the
same LAN so their links add up, and to remove the manual copy-paste/handoff
step entirely. That requires two changes psxdh previously ruled out:

1. **An embedded downloader.** Until now psxdh only captured URLs and handed
   them to an external tool (FDM, or an external aria2 over RPC). To assign and
   track downloads itself, psxdh must drive a downloader directly.
2. **A distributed role split.** A **master** (the node the PS5 proxies through)
   enumerates a game's parts and assigns them to **slave** nodes; finished parts
   come back to the master, which serves them to the console as before.

## Decision

### Embedded downloader = managed aria2c

psxdh manages a local **aria2c** subprocess (`--enable-rpc`), driven through the
existing JSON-RPC client. The engine sits behind a `downloader.Downloader`
interface (`internal/downloader`) so the cluster and the test suite never depend
on a real subprocess:

- `aria2Downloader` — supervises `aria2c` via stdlib `os/exec` (locate on PATH or
  `downloader.aria2_binary`, restart on crash, kill on shutdown).
- `httpDownloader` — a built-in stdlib HTTP engine used when
  `downloader.allow_http_fallback` is set (dev/CI) and as the deterministic
  engine the test suite drives.

**Why aria2c, not a from-scratch Go downloader:** aria2 already does segmented,
multi-connection, resumable downloads — exactly what throttled links need —
maturely and for free.

**Tradeoff (accepted):** aria2c is an external **runtime** requirement on nodes
that use the embedded downloader (`psxdh node`, or master with `master_as_node`).
This does *not* change the Go build: psxdh remains a single static binary with
**no new Go dependency** (managed via `os/exec`). Startup fails if aria2c is
missing unless `downloader.allow_http_fallback: true` (dev/CI only).

This **reverses the original "no built-in downloader" non-goal**; that line in
the roadmap now points here.

### Distributed architecture

- **Master** (`psxdh proxy` with `cluster.role: master`): captures the first PKG
  URL, **enumerates** the part series (`_0.._N`, probing until a gap — query
  string and `f_<hash>` path segment preserved), and submits the parts to a
  `cluster.Manager`. The manager assigns parts to least-loaded online nodes,
  polls progress, and **pulls finished parts** into the library.
- **Slave** (`psxdh node`): runs the embedded downloader plus a small
  token-guarded agent API (`/node/info|assign|status|part|cancel`) that the
  master drives; serves finished parts back for the master to pull.
- **Serving model:** slaves push to the master (the master pulls the completed
  file over the LAN and writes it into `library.dir`). The master also picks up
  parts **dropped in manually** (USB/SSD) via the existing watcher. The single
  authority is: a part is "have it" iff it is present in the master's library
  index — unifying slave-push and manual-move.
- **Discovery:** both mDNS auto-discovery (`_psxdh-node._tcp`, via the existing
  `grandcat/zeroconf`) and manual add-by-IP in the dashboard.
- **Auth:** a shared `cluster.token` (generated on the master when empty) guards
  every master↔slave call, mirroring the dashboard token.

### Dependency budget (per ADR 0002)

**No new Go dependency.** `os/exec`, `net/http`, `crypto/rand` are stdlib; mDNS
browse reuses `grandcat/zeroconf` (ADR 0004); config serialisation reuses
`gopkg.in/yaml.v3`. Cluster nodes require the aria2c **binary** at runtime
(documented, not imported; see `psxdh doctor` and README install table).

## Consequences

- psxdh gains a real download engine and a LAN cluster; capability matches the
  "combine several links" goal.
- The downloader interface keeps the engine swappable and the suite hermetic
  (no aria2c, no network in normal `go test`; a `PSXDH_ARIA2=1` opt-in test
  covers the real subprocess).
- Cluster code is confined to `internal/cluster` (+ `internal/downloader`); the
  proxy path is unchanged when `cluster.enabled` is false.
- The dashboard becomes the management surface: per-node + aggregate progress
  and speed, add/remove/discover nodes, and a full config editor.
