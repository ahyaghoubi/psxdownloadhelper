# ADR 0006: Simplify to the core workflow

- Status: Accepted
- Date: 13/11/2026
- Deciders: project owner
- Supersedes: [0003](0003-network-resilience.md), [0004](0004-mdns.md), [0005](0005-distributed-downloader.md); amends [0002](0002-dependency-budget.md)

## Context

psxdh grew to 25 internal packages and about 11,000 lines of Go: an embedded
aria2 downloader, a master/slave download cluster, job persistence, a partial
download cache, a custom DNS resolver with health ranking, retry, a circuit
breaker, bandwidth shaping, mDNS, a JSONL capture log, integrity
verification, a session tracker, a live config editor and a separate
token-protected dashboard port. Several config keys were never read by any
code. None of it had been validated on a real console, and the owner reported
that many features didn't work in practice.

The product's job is narrow: show the console's download links, let an
external downloader fetch them, and serve the files back. External
downloaders (FDM in particular) already handle speed, resume and scheduling.

## Decision

Keep only:

- the proxy (forward, CONNECT tunnel, serve library hits with Range);
- URL classification (PS4 and PS5 rules, both always on);
- a library folder that is rescanned every second (replaces fsnotify);
- an in-memory tracker of captured files and bytes sent;
- a dashboard on the same port as the proxy, with a status for each file, copy and `.txt` export;
- a minimal config with old keys still accepted;
- one optional `network.upstream_proxy` URL as an escape hatch for blocked networks.

Remove everything else listed above, plus the `doctor`, `probe`, `export`,
`import` and `node` commands and the cobra, fsnotify and zeroconf
dependencies.

## Consequences

- About 1,700 lines of Go instead of about 11,000; one third-party dependency.
- One address to remember (`IP:8080`) for both the console and the dashboard.
- The dashboard has no token. It's a LAN tool that can only list links and
  clear the list.
- Users who relied on the cluster, partial cache or custom DNS lose them.
  Old config files still load; the removed keys are ignored.
- Real-hardware validation is now the top priority (see [roadmap](../roadmap.md)).
