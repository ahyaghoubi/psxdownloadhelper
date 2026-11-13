# psxdownloadhelper (psxdh)

Download PlayStation games and updates at full PC speed.

`psxdh` is a small proxy you run on your PC. Point your PS5 or PS4 at it,
start a download on the console, and psxdh shows you the download links.
Fetch them with **Free Download Manager** (or any downloader you like) into
your library folder, resume on the console, and it installs straight from
your PC over your home network.

It's a clean-room MIT reimplementation in the spirit of
[KOPElan/PSX-Download-Helper](https://github.com/KOPElan/PSX-Download-Helper),
with first-class PS5 support.

```
 console ──proxy──► psxdh ──────────────► Sony CDN        (file not on PC yet)
                     │
                     └── library folder ──► console        (file is on PC: served locally)
                           ▲
         FDM / aria2 / IDM ┘  (you download the links psxdh shows you)
```

psxdh never decrypts PSN traffic: HTTPS is passed through untouched. It does
not download anything itself.

---

## Quick start

1. **Get psxdh.** Download the file for your system from
   [Releases](https://github.com/ahyaghoubi/psxdownloadhelper/releases), or
   build it: `go build ./cmd/psxdh`.
2. **Run it.** Double-click it, or run `psxdh` in a terminal. The dashboard
   opens in your browser and shows the address to use.
   *macOS: the first time, right-click › Open to get past Gatekeeper.*
3. **Point the console at it.** Set the console's proxy server to your PC's
   address and port `8080`:
   - **PS5:** Settings › Network › Settings › Set Up Internet Connection.
     Highlight your network, press **Options**, choose Advanced Settings ›
     Proxy Server › Use.
   - **PS4:** Settings › Network › Set Up Internet Connection › Custom, keep
     the defaults until Proxy Server › Use.
4. **Start a download on the console.** Its files appear in the dashboard.
5. **Pause it on the console**, press **Copy links** and paste them into
   FDM. Save into the library folder shown in the dashboard
   (`~/Downloads/psxdh` by default).
6. **Resume on the console** once the dashboard says *Ready*. The console
   installs from your PC.

The dashboard walks you through these steps and shows where each file is:

| Status | Meaning |
| --- | --- |
| Needs download | Not on your PC yet. Copy the link into your downloader. |
| From Sony (slow) | The console is downloading it from Sony right now. Pause it. |
| Downloading on PC | Your downloader is writing it into the library folder. |
| Wrong size | The file on your PC doesn't match Sony's. Delete it and download again. |
| Ready | On your PC. Resume the download on the console. |
| Installing | The console is pulling it from your PC. |
| Sent to console | Done. |

You can open the dashboard from your phone too: it's the same address and
port as the proxy, for example `http://192.168.1.10:8080`.

---

## Options

```
psxdh [--listen 0.0.0.0:8080] [--library DIR] [--config FILE] [--log-level info] [--no-browser]
psxdh version
```

Settings can also live in `~/.config/psxdh/config.yaml`. See
[docs/configuration.md](docs/configuration.md). Config files from older
releases still load; settings for removed features are ignored.

---

## Good to know

- **Keep the original file names.** psxdh matches files by name. Files can be
  in subfolders of the library folder.
- **Unfinished downloads are never served.** Files ending in `.part`,
  `.fdmdownload`, `.crdownload`, `.tmp`, `.download` or with an `.aria2`
  control file beside them are treated as in progress, and a file is only
  served once its size has stopped changing for 2 seconds.
- **A file with the wrong size is never served.** When psxdh knows the real
  size from Sony, a mismatched local copy is skipped and the console
  downloads from Sony instead.
- **Multi-part games:** as soon as the console asks for any part
  (`…_0.pkg`, `…_51.pkg`), psxdh asks Sony which other parts exist and lists
  them all, so you can download the whole game with the console switched off.
- **Updates: let the console download them.** An update usually needs only
  a few pieces of a large package, so downloading the whole package on the
  PC is slower. psxdh can't tell a game from an update by itself, so press
  **It's an update** on its card: the links are withdrawn and the console
  fetches it directly. **Download on PC instead** undoes it.
- **Links survive restarts.** Captured links are saved to `links.json` next
  to the config file. Press **Clear** to forget them.
- **Links can expire.** If your downloader gets "403 Forbidden", start the
  download on the console again for fresh links.
- **Firewall:** allow incoming connections to psxdh on your PC (Windows asks
  the first time).
- **Blocked CDN?** `network.upstream_proxy` sends psxdh's plain-HTTP
  downloads through another proxy (http, https or socks5).

---

## Development

Needs Go (the version in `go.mod`). Run `make` to list commands:

```bash
make run                           # build and start psxdh, opens the dashboard
make run ARGS="--library ~/Games"  # pass flags through
make test                          # unit tests plus end-to-end tests of the real binary
make check                         # formatting, vet and tests, as CI runs them
make build                         # ./psxdh for this machine
make dist                          # binaries for every OS/arch in ./dist
```

Without `make` (for example on Windows): `go run ./cmd/psxdh`, `go test -race ./...`.

The end-to-end tests in [cmd/psxdh/e2e_test.go](cmd/psxdh/e2e_test.go) build
the binary, run it, and play console, Sony CDN and FDM against it.

### Releasing

Either run **Actions › Release › Run workflow** on `main` and enter a version
(for example `v0.2.0`), or push a tag yourself:
`git tag v0.2.0 && git push origin v0.2.0`. The tests run on macOS, Linux and
Windows first; if they pass, GoReleaser publishes binaries for all six
OS/architecture pairs with checksums. Versions with a suffix (`v0.2.0-rc.1`)
are published as pre-releases.

Design: [docs/architecture.md](docs/architecture.md) ·
URL patterns: [docs/cdn-patterns.md](docs/cdn-patterns.md) ·
Plans: [docs/roadmap.md](docs/roadmap.md) ·
Why it got smaller: [ADR 0006](docs/decisions/0006-simplify-to-core.md)

## Legal

For content you own on PSN. psxdh only fetches the official CDN files your
console already asks for. No piracy, no licence bypass, no MITM.

MIT licence.
