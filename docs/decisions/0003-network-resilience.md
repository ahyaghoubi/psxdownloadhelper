# ADR 0003: Network resilience stack

- Status: Accepted
- Date: 01/06/2026
- Deciders: project owner
- Supersedes: —

## Context

`psxdh` is used by people whose internet connection between the PC and Sony's
CDN is **not** the well-behaved fibre link the original design assumed. Two
concrete failure modes motivate this ADR:

1. **Intermittent connectivity.** Forward upstream requests fail with
   transient DNS, TCP, TLS, or 5xx errors. Today the console sees an
   immediate 502 and has to retry the entire request itself, which is slow
   and ugly on a PlayStation UI.
2. **ISP-level DNS / route disruption** (e.g. ISPs in Iran). Stock OS
   resolution to Sony's CDN can be poisoned, slow, or blocked outright. The
   user's existing workaround is a third-party resolver (Shecan, Electro,
   403.online, Begzar, Cloudflare DoH) and / or a VPN they want only the
   CDN traffic to use, not their whole console.

Both can be solved at the proxy layer without compromising the
[clean-room MIT posture](../research.md#legal-ethics--licence) or the
[design rules in the proxy pipeline](../architecture.md#design-rules).

## Decision

Add a **network resilience layer** that the proxy's upstream forward path
sits on top of. The layer is composed of small, independently-testable
packages under `internal/`:

| Package | Responsibility |
| --- | --- |
| `internal/netresolve` | Custom DNS: DoH (RFC 8484), plain UDP/53, system fallback, TTL cache, multi-resolver fallback. |
| `internal/retry` | Backoff policy with jitter, transient-only retry classifier, pre-write-only invariant. |
| `internal/circuit` | Per-host circuit breaker (closed / open / half-open). |
| `internal/bandwidth` | Token-bucket rate limiter for `io.Reader` (forward-path throttle). |
| `internal/upstream` | Builds a fully-configured `*http.Client`: custom dialer with `netresolve` + IPv4 preference + dial timeout, optional HTTP/SOCKS5 upstream proxy, optional bandwidth limit, optional circuit breaker. |
| `internal/persist` | Append-only JSONL sink for `capture.Event` (survives restarts). |
| `internal/verify` | Framework for `.crc` sidecar verification. Parser stub until Phase 0 nails the format. |
| `internal/doctor` | Diagnostic checks for the `psxdh doctor` and `psxdh probe` commands. |

The proxy package gains two new behaviours, both opt-in:

- **Forward retry**: when `forward.retry.max_attempts > 1`, transient errors
  on `client.Do` are retried with backoff. Retries only happen **before any
  response bytes have been written to the client** — once we start
  streaming, a mid-stream failure bubbles up so the console can issue a
  fresh `Range` request and we don't corrupt the partial body.
- **Partial cache**: when `forward.partial_cache.enabled`, a non-Range GET
  forward stream is tee'd to disk in the library directory, then atomically
  renamed to the final basename on a successful complete response. This
  promotes a successful forward into a future library hit "for free", with
  zero extra latency for the initial request.

`CONNECT` tunnels remain raw TCP. None of the new layers touch HTTPS
payload; the
[no-MITM rule from docs/architecture.md](../architecture.md#design-rules)
stands.

## New dependency: `golang.org/x/net`

Two pieces of the resilience stack need types from `golang.org/x/net`:

1. `golang.org/x/net/dns/dnsmessage` — DNS wire-format encoding/decoding for
   the DoH and UDP resolvers. Hand-rolling this is feasible but invites
   correctness bugs in compression, EDNS, and TC handling.
2. `golang.org/x/net/proxy` — SOCKS5 dialer for the optional upstream proxy
   chain (HTTP proxy is handled by the stdlib `http.Transport.Proxy` field).

`golang.org/x/net` is the official Go-team-maintained extension to the
stdlib `net` package. Licence: BSD-3-Clause (MIT-compatible). Maintenance
signal: continuous releases. Used transitively by most non-trivial Go HTTP
projects already.

Per [ADR 0002](0002-dependency-budget.md), this ADR is its addition record.

Rate limiting for the bandwidth-cap feature is implemented in-tree
(`internal/bandwidth`) rather than via `golang.org/x/time/rate`, to keep the
new dep surface to one package.

## Configuration surface

Each new capability is **off by default**. The proxy behaves exactly as it
does today unless the user opts in.

```yaml
network:
  dns:
    mode: "system"                # system | udp | doh | doh+udp
    resolvers: []                 # plain "1.1.1.1" or "https://… /dns-query"
    timeout_ms: 1500
    cache_ttl_s: 300
    cache_max_entries: 4096
  prefer_ipv4: false
  dial_timeout_ms: 10000
  upstream_proxy:
    enabled: false
    url: ""                       # http://, https://, socks5://; or "env"
    only_for_hosts: []
  circuit:
    enabled: false
    failure_threshold: 5
    cooldown_ms: 30000
  bandwidth:
    forward_bps: 0                # 0 = unlimited

forward:
  retry:
    max_attempts: 1               # 1 = current behaviour (no retries)
    initial_backoff_ms: 200
    max_backoff_ms: 5000
    multiplier: 2.0
    jitter: 0.2
  partial_cache:
    enabled: false
    min_size_bytes: 1048576

capture:
  persist:
    enabled: false
    path: ""                      # JSONL file, ~ expanded
    fsync: false

verify:
  crc: false                      # framework only until Phase 0 locks format
```

Full reference in [docs/configuration.md](../configuration.md) and
[docs/network-resilience.md](../network-resilience.md).

## Diagnostic CLI surface

- `psxdh doctor` — resolve each configured DNS resolver, probe the PSN CDN
  hosts (`gst.prod.dl.playstation.net`, `sgst.prod.dl.playstation.net`,
  `gs2.ww.prod.dl.playstation.net`, `gs2-sec.ww.prod.dl.playstation.net`),
  attempt a TLS handshake, summarise.
- `psxdh probe <url>` — classify the URL against the rule pack, resolve,
  issue a `HEAD`, print headers / redirect chain. Useful when the FDM-side
  download succeeds but the console-side replay misbehaves.

## Consequences

- Every new feature is gated behind a config flag whose default preserves
  today's behaviour. Phase 1 tests continue to pass unchanged.
- The proxy package depends on `internal/upstream` but not on the inner
  resilience packages. Swapping any implementation (e.g. a future GeoDNS
  resolver) only touches `internal/netresolve`.
- The diagnostic commands are non-destructive and never touch the library;
  they can be run while the proxy is serving.
- The `.crc` parser remains a stub — verification fires only when both PKG
  and CRC are observed in the library; if parsing fails, the file is left
  alone (warning logged) so a malformed sidecar never blocks installs.
