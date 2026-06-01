# ADR 0001: HTTP proxy implementation stack

- Status: Accepted (default; revisit if Track A hardware capture reveals issues)
- Date: 30/05/2026
- Deciders: project owner
- Supersedes: —

## Outcome

**Selected: Option A — stdlib `net/http` + `Hijacker`.**

Implementation lives in [internal/proxy/](../../internal/proxy/). Covered by an httptest-based integration suite that validates: absolute-URI forwarding, query-string preservation, Range pass-through (`bytes=N-M` → 206 with correct `Content-Range`), library-hit short-circuit, `auto`/`cache`/`strict` forward modes, capture event publication, `CONNECT` tunnel against `httptest.NewTLSServer` (no MITM), 405 for unsupported methods, and hop-by-hop header stripping.

Phase 0 hardware capture against real PS5/PS4 traffic may surface edge cases that warrant revisiting (e.g. unusual `Range` encodings or `CONNECT` quirks). The `Deps` constructor pattern in `proxy.New` keeps the swap surface small: an alternative implementation would expose the same `Handler()` method and Deps shape so `cmd/psxdh` could swap by changing one constructor call.

If revisited, candidates remain Option B (`elazarl/goproxy`) and Option C (raw socket parser).

## Context

[roadmap.md](../roadmap.md#phase-0--research--validation) lists three candidate stacks for the proxy core (originally captured in the Phase 0 validation gate):

1. Stdlib `net/http` with a custom `Director` plus manual `Hijacker`-based `CONNECT` tunnel.
2. `elazarl/goproxy` (used by [RewindPS4](https://github.com/Ailyth99/RewindPS4)).
3. A custom raw-socket HTTP parser ([PSXMaster](https://github.com/ghost1372/PSXMaster) / KOPElan style).

All three must correctly handle:

- Absolute-URI `GET`/`HEAD` (typical for HTTP proxies).
- `CONNECT` tunnel that bridges raw TCP without decrypting.
- `Range` header preservation end-to-end (`bytes=N-M`, `bytes=-N`, `bytes=N-`).

The decision drives `internal/proxy/server.go` and ripples through every other package via interfaces.

## Options

### Option A — stdlib `net/http` + `Hijacker`

**Pros:** zero dependencies; full control over `Range` header passthrough; `CONNECT` hijack is a well-trodden pattern; debuggable with stdlib tooling.
**Cons:** more boilerplate than a framework; we hand-roll absolute-URI handling.

### Option B — `elazarl/goproxy`

**Pros:** fewer lines of glue for the common cases; battle-tested by RewindPS4.
**Cons:** dependency footprint; opinionated request-rewrite model that may fight us when we want to forward bytes unmodified; licence is MIT (compatible), but its plugin pattern needs auditing.

### Option C — Raw socket HTTP parser

**Pros:** maximum control; matches PSXMaster's approach which Phase 0 traces against.
**Cons:** highest code volume and bug surface; we re-implement HTTP/1.1 details Go's stdlib already gets right.

## Decision

To be filled after the Phase 0 spike. Default expectation: **Option A** (stdlib + `Hijacker`) — "stdlib + hijack will suffice; goproxy only if it removes meaningful boilerplate."

### Spike methodology

Three throwaway implementations under `spike/proxy_stdlib`, `spike/proxy_goproxy`, `spike/proxy_socket`. Each must:

- Accept absolute-URI `GET`/`HEAD` and forward upstream byte-for-byte.
- Hijack `CONNECT` and bridge raw TCP against `httptest.NewTLSServer`.
- Preserve `Range` headers in both directions against a 1 GB synthetic file.
- Compile cleanly with `go vet ./...` and `go test ./...`.

### Cross-check

Side-by-side capture against [PSXMaster](https://github.com/ghost1372/PSXMaster) on a Windows VM, same titles, same console session. Diff classified URL list and request sequence. Note that PSXMaster comparison is informational only and does not block this decision if a Windows machine is unavailable; in that case mark the cross-check as "not performed" in the Accepted section.

### Decision criteria (rank-order)

1. Range correctness on real PS5 traffic.
2. `CONNECT` correctness against HTTPS endpoints (no MITM).
3. Lines of code (lower is better, ties broken by Option A).
4. Dependency footprint per [ADR 0002](0002-dependency-budget.md).

## Consequences

- `internal/proxy` will expose a `ProxyServer` interface so the chosen implementation can be swapped without touching consumers.
- Whichever option loses, its spike directory is deleted after this ADR is `Accepted`; no spike code reaches `main`.
- This ADR also produces the redacted URL fixtures under `testdata/urls/` that gate the `match` package's tests.

## Clean-room note

This project is clean-room MIT. PSXMaster (MIT) is the only readable reference per [research.md — Legal, ethics & licence](../research.md#legal-ethics--licence). KOPElan and PSXhub source must not be read while this ADR or any related code is open. Where PSXMaster's *behaviour* informs a decision, cite the file/commit in this ADR.
