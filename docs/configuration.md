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

The single binary is `psxdh`. The relevant Phase 1 commands:

| Command | Purpose |
| --- | --- |
| `psxdh proxy` | Run the HTTP proxy + library watcher until interrupted. |
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

handoff:                          # Phase 2 — "Send to FDM" handoff settings
  fdm:
    enabled: true
    fdm_binary: ""                # auto-detect on PATH when empty
    fallback_to_clipboard: true

forward:
  mode: "auto"                    # auto | cache | strict
  passthrough_https: true         # CONNECT tunnel without MITM (do not change)

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
