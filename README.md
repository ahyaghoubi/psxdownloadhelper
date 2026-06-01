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

> **Status: pre-implementation, Phase 1 complete.** The proxy, library
> watcher, classifier, and CLI are wired up and tested. Hardware validation
> against current PS5/PS4 firmware (Phase 0) and the dashboard / session
> tracker (Phase 2) are still ahead. See [docs/roadmap.md](docs/roadmap.md)
> for the full phase breakdown.

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
# Defaults: proxy on 0.0.0.0:8080, library at ~/Downloads/psxdh
./psxdh proxy

# Or with overrides:
./psxdh proxy --listen 0.0.0.0:8080 --library ~/Downloads/psxdh

# Or with a config file:
./psxdh proxy --config config.yaml
```

The startup banner prints your LAN IP and the address the console should
target:

```
psxdh dev
  LAN IP:        192.168.1.42
  proxy listen:  0.0.0.0:8080
  admin listen:  http://127.0.0.1:8081/
  library dir:   /home/me/Downloads/psxdh
  library layout: basename
  match rules:   8

Point your console's HTTP proxy at: 192.168.1.42:8080
Press Ctrl-C to shut down.
```

### Point your console at the proxy

- **PS5:** Settings → Network → Settings → Set Up Internet Connection →
  Advanced Settings → Proxy Server → Use → enter the PC's LAN IP and the
  proxy port (default 8080).
- **PS4:** Settings → Network → Set Up Internet Connection → Custom →
  Proxy Server → Use → enter the PC's LAN IP and the proxy port.

Use a wired LAN connection on both ends if possible — wireless will work but
caps your local-serve throughput.

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
[docs/configuration.md](docs/configuration.md) for the full reference).
Minimal example:

```yaml
proxy:
  listen: "0.0.0.0:8080"
library:
  dir: "~/Downloads/psxdh"
  layout: "basename"
  watch: true
forward:
  mode: "auto"
log:
  level: "info"
```

CLI flags (`--listen`, `--library`, `--log-level`, `--config`) override the
YAML file.

---

## CLI

| Command | Purpose |
| --- | --- |
| `psxdh proxy` | Run the HTTP proxy + library watcher. |
| `psxdh version` | Print the build version. |
| `psxdh --help` | List subcommands and flags. |

Future Phase 2 commands (planned, see [docs/roadmap.md](docs/roadmap.md)):
`psxdh sessions`, `psxdh export-urls`, `psxdh watch`.

---

## What `psxdh` does **not** do

- It does **not** download anything itself. Use FDM / aria2 / IDM / curl —
  whatever fits your workflow.
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
  `strict` modes, hop-by-hop header stripping, capture publication.
- `internal/serve` — RFC 7233 Range cases.
- `internal/export`, `internal/capture`, `internal/config` — unit tests.
- `e2e/` — full forward → watcher → serve cycle against a fake Sony-CDN-
  shaped upstream, including the multi-part FDM scenario.

See the testing strategy in
[docs/architecture.md](docs/architecture.md#testing-strategy).

---

## Documentation map

| Document | What's in it |
| --- | --- |
| [docs/architecture.md](docs/architecture.md) | Repository layout, package responsibilities, request pipeline, session model, testing strategy. |
| [docs/cdn-patterns.md](docs/cdn-patterns.md) | PS4 / PS5 CDN URL shapes and the rule packs that classify them. |
| [docs/configuration.md](docs/configuration.md) | `config.yaml` reference + CLI flags + library layouts. |
| [docs/roadmap.md](docs/roadmap.md) | Vision, user flows, phases, risks, open questions, v1.0 definition of done. |
| [docs/research.md](docs/research.md) | Prior-art tools, dependency budget, legal / licence notes, external references. |
| [docs/decisions/](docs/decisions/) | Architecture Decision Records. |

---

## Licence

MIT. See [LICENSE](LICENSE).

Not affiliated with Sony Interactive Entertainment. You are responsible for
compliance with PSN Terms of Service and any local laws when using this
tool.
