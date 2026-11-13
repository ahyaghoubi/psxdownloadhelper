# Configuration

psxdh works with no configuration. Change things with flags, or with a YAML
file at `~/.config/psxdh/config.yaml` (or wherever `--config` points).
Flags win over the file.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `0.0.0.0:8080` | Address the console uses as its proxy. The dashboard is on the same address. |
| `--library` | `~/Downloads/psxdh` | Folder your download manager saves into. Created if missing. |
| `--config` | `~/.config/psxdh/config.yaml` | Settings file. A missing default file is fine; a missing `--config` file is an error. |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error`. |
| `--no-browser` | off | Don't open the dashboard when started by double-click. |

`psxdh version` prints the version. `psxdh proxy …` still works for scripts
written for older releases.

## Config file

Every key is optional.

```yaml
proxy:
  listen: 0.0.0.0:8080

library:
  dir: ~/Downloads/psxdh
  # Files with these endings are downloads still in progress and are never
  # served. Replaces the default list when set.
  ignore_suffixes: [".part", ".fdmdownload", ".tmp", ".crdownload", ".aria2", ".download"]

network:
  # Send psxdh's plain-HTTP downloads from Sony through another proxy.
  # HTTPS (CONNECT) traffic from the console still goes direct.
  upstream_proxy:
    enabled: false
    url: socks5://127.0.0.1:1080   # http://, https:// or socks5://

log:
  level: info
```

## Upgrading from older releases

Older config files load unchanged. These sections are ignored because the
features were removed in [ADR 0006](decisions/0006-simplify-to-core.md):
`admin`, `match`, `capture`, `handoff`, `forward`, `verify`, `mdns`,
`downloader`, `cluster`, `jobs`, `network.dns`, `network.circuit`,
`network.bandwidth`, `network.prefer_ipv4`, `network.dial_timeout_ms`,
`network.upstream_proxy.only_for_hosts`, `library.layout`, `library.watch`,
`library.stable_settle_ms`, `log.json`.

The dashboard is now on the proxy port (`8080`), not `8081`.
