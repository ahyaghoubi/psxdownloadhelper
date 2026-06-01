# Network resilience

`psxdh` is often deployed on networks where the connection between the
PC and Sony's CDN is the unreliable link in the chain. The resilience
stack — introduced in
[ADR 0003](decisions/0003-network-resilience.md) — adds opt-in DNS,
retry, proxy-chain, partial-cache, breaker, bandwidth, and persistence
features so the proxy degrades gracefully when the network does.

Everything in this document is **off by default**. The defaults match
the pre-ADR-0003 behaviour bit-for-bit; enable only what you need.

- [Quick recipes](#quick-recipes)
- [DNS resolution](#dns-resolution)
- [Forward retry policy](#forward-retry-policy)
- [Upstream proxy chain](#upstream-proxy-chain)
- [Circuit breaker](#circuit-breaker)
- [Partial cache on the forward path](#partial-cache-on-the-forward-path)
- [Bandwidth throttle](#bandwidth-throttle)
- [Persistent capture log](#persistent-capture-log)
- [Diagnostic CLI](#diagnostic-cli)
- [Phase-0 sanity ritual](#phase-0-sanity-ritual)

## Quick recipes

### Iran / ISP-poisoned DNS
```yaml
network:
  dns:
    mode: "doh+udp"
    resolvers:
      - "https://free.shecan.ir/dns-query"
      - "https://dns.electrotm.org/dns-query"
      - "178.22.122.100"   # Shecan plain UDP
      - "185.51.200.2"
      - "https://1.1.1.1/dns-query"
forward:
  retry:
    max_attempts: 4
    initial_backoff_ms: 250
    max_backoff_ms: 4000
```

### "Route only CDN traffic through my VPN"
```yaml
network:
  upstream_proxy:
    enabled: true
    url: "socks5://127.0.0.1:1080"
    only_for_hosts:
      - "prod.dl.playstation.net"
      - "ww.prod.dl.playstation.net"
```

### Unstable hotel / cellular tether
```yaml
forward:
  retry:
    max_attempts: 6
    initial_backoff_ms: 500
    max_backoff_ms: 8000
    jitter: 0.3
  partial_cache:
    enabled: true
    min_size_bytes: 1048576
network:
  circuit:
    enabled: true
    failure_threshold: 5
    cooldown_ms: 30000
  prefer_ipv4: true
  dial_timeout_ms: 7000
```

## DNS resolution

The `network.dns.mode` switch selects the resolution transport:

| Mode      | Behaviour                                                                                                   |
| --------- | ----------------------------------------------------------------------------------------------------------- |
| `system`  | Use the OS resolver (default).                                                                              |
| `udp`     | Plain DNS over UDP/53 against `resolvers`. System resolver remains as a final fallback.                     |
| `doh`     | DNS-over-HTTPS (RFC 8484, POST flavour) against `resolvers`. Entries must start with `https://`.            |
| `doh+udp` | Mix: `https://`-prefixed entries become DoH, the rest become UDP. System resolver remains as fallback.      |

All resolvers are wrapped in an in-memory TTL cache. Responses with a
zero TTL (notably anything the system resolver returns) fall back to
`cache_ttl_s` (default 300s).

Resolvers are tried in **list order** until one returns a non-error
result. An authoritative `NXDOMAIN` from any resolver stops the chain
immediately — falling through after that just hides the truth.

Known good DoH endpoints inside Iran (community-sourced; verify with
`psxdh doctor` before trusting them):

- `https://free.shecan.ir/dns-query` (Shecan)
- `https://dns.electrotm.org/dns-query` (Electro)
- `https://dns.403.online/dns-query` (403.online)
- `https://dns.begzar.ir/dns-query` (Begzar)
- `https://1.1.1.1/dns-query` (Cloudflare)
- `https://dns.google/dns-query` (Google)

## Forward retry policy

`forward.retry.max_attempts` controls how many times the proxy will
re-issue an upstream request when it fails transiently. The default of
`1` disables retries (current behaviour).

The retry classifier treats these as transient:

- Network errors (DNS failure, connection reset/refused, TLS handshake,
  i/o timeout, broken pipe, unreachable network)
- HTTP `500`, `502`, `503`, `504`

The classifier explicitly **does not** retry:

- `context.Canceled` / `context.DeadlineExceeded` (the client gave up)
- 4xx responses (the request itself is wrong)

### The pre-write invariant

The proxy only retries **before any response bytes have been written
to the client**. Once we've started streaming, a mid-stream upstream
failure has to bubble up: the console will issue a fresh `Range`
request and continue from where it left off. Retrying mid-stream would
either truncate the response, corrupt it, or stall the connection.

If you set `max_attempts` high and `initial_backoff_ms` low, the proxy
will gladly burn the budget waiting on Sony's CDN to come back. The
exponential backoff with jitter prevents thundering-herd retry storms
across multiple paths.

## Upstream proxy chain

`network.upstream_proxy` routes outbound forward traffic through an
HTTP, HTTPS, or SOCKS5 proxy. The most common use case is "send only
my console's PSN traffic through my personal VPN; leave the rest of my
PC's traffic alone":

```yaml
network:
  upstream_proxy:
    enabled: true
    url: "socks5://127.0.0.1:1080"   # local VPN's SOCKS5 entry point
    only_for_hosts:                  # empty = route everything
      - "prod.dl.playstation.net"
      - "ww.prod.dl.playstation.net"
```

The `only_for_hosts` filter matches **either** an exact host **or** any
subdomain of it. Hosts not on the list dial directly.

HTTP proxies via `http://` are passed through `http.Transport.Proxy`;
SOCKS5 proxies via `socks5://` use `golang.org/x/net/proxy`.

## Circuit breaker

Per-host failure breaker. After `failure_threshold` consecutive
failures, the breaker opens and short-circuits subsequent dials with
a fail-fast error. After `cooldown_ms` the breaker half-opens, lets one
probe through, and either closes again on success or re-opens on
failure.

The breaker is a cooperative back-pressure tool, not a replacement for
retry. Pair it with retry to avoid burning attempts on a host that has
been down for the last 30 seconds:

```yaml
network:
  circuit:
    enabled: true
    failure_threshold: 5
    cooldown_ms: 30000
```

## Partial cache on the forward path

When `forward.partial_cache.enabled` is `true`, the proxy tees every
successful non-Range upstream forward to disk under
`library.dir/.psxdh-partial/`. If the response completes cleanly with
the byte count matching `Content-Length`, the file is atomically
renamed into `library.dir/<basename>` — a future request for the same
URL becomes a library hit without you having to re-download anything.

Scope (intentionally narrow for v1):

- GET only.
- No `Range` header on the request.
- 200 OK with a positive `Content-Length`.
- Response size ≥ `min_size_bytes` (default 1 MiB — skips tiny manifests).
- Same basename is not already in the library, and not currently being
  cached.

A failed download leaves the `.partial` file behind as a breadcrumb
(you can inspect or delete it manually). The next eligible forward
overwrites it. Resuming from a partial across runs is on the Phase 2.5+
roadmap.

## Bandwidth throttle

`network.bandwidth.forward_bps` caps the forward path's throughput in
bytes per second. `burst_bytes` controls the token-bucket burst size
(default: equal to `forward_bps`).

```yaml
network:
  bandwidth:
    forward_bps: 5242880    # 5 MiB/s
    burst_bytes: 1048576    # 1 MiB
```

Use this to keep psxdh's downloads from saturating a shared link
during a console patch. CONNECT tunnels are NOT throttled (we don't
inspect their bytes), and library-served files are unaffected.

## Persistent capture log

`capture.persist` writes one JSON object per observed event to an
append-only file. The proxy stays running even if writes fail (disk
full, permission denied) — a warning is logged.

```yaml
capture:
  persist:
    enabled: true
    path: "~/.psxdh/capture.jsonl"
    fsync: false
```

Use `fsync: true` if you need every event durable across a power loss,
at the cost of one fsync per write.

## Diagnostic CLI

Two new subcommands ship with the resilience stack:

### `psxdh doctor`

Walks every configured DNS resolver, resolves each Sony CDN host, then
tries a direct TLS handshake on port 443. The output makes it
immediately obvious whether DNS or transport is the bottleneck.

```text
psxdh doctor
────────────────────────────────────────────────────────────

DNS resolvers
  • https://free.shecan.ir/dns-query
      gst.prod.dl.playstation.net               ok   (124ms) 23.40.197.86
      sgst.prod.dl.playstation.net              ok   (118ms) 23.40.197.119
      gs2.ww.prod.dl.playstation.net            FAIL (1.5s) i/o timeout
      gs2-sec.ww.prod.dl.playstation.net        FAIL (1.5s) i/o timeout
  • system
      gst.prod.dl.playstation.net               FAIL (5s)  no such host

Direct TLS handshakes (port 443)
  gst.prod.dl.playstation.net                   ok   (286ms)
  ...
```

Flags: `--host` (repeatable, override the default list), `--skip-tls`,
`--timeout` (per-host TLS budget in seconds), `--config`.

### `psxdh probe <url>`

Single-URL classify + resolve + HEAD/GET diagnostic. Useful when an
FDM download succeeds but the console-side replay misbehaves.

```text
URL:        https://gst.prod.dl.playstation.net/cdn/path/CUSA12345/CUSA12345.pkg
Classified: pkg-app
DNS:        ok   (28ms) 23.40.197.86
HTTP:       HEAD 200 (190ms)
  Server:        nginx
  Accept-Ranges: bytes
  Content-Length: 6432198144
```

If the upstream answers `405 Method Not Allowed` to HEAD, `probe`
silently falls back to `GET` with `Range: bytes=0-0` so it works on
every PSN endpoint we've seen.

## Phase-0 sanity ritual

Before relying on these features in production, run the following:

1. `psxdh doctor --config your-config.yaml` — verify every resolver and
   every CDN host is reachable.
2. `psxdh probe <a real PKG URL>` — verify classification + range
   support against your DNS choice.
3. Drive a single PKG download end-to-end with `forward.retry.max_attempts: 4`
   and `partial_cache.enabled: true`. Confirm the file lands in
   `library.dir` after a successful completion.
4. Drop the WAN mid-download (e.g. disconnect Wi-Fi for 5 s, reconnect).
   The proxy should serve a 502 to the console on the in-flight Range,
   the console will re-issue, and the next Range should complete
   normally. The on-disk byte stream must not be corrupted.
