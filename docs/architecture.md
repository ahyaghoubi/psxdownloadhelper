# Architecture

psxdh is one Go binary with one dependency (`gopkg.in/yaml.v3`). It listens
on a single port that serves both the console's proxy traffic and the web
dashboard.

## Request flow

```
                    ┌──────────── psxdh :8080 ────────────┐
console ──CONNECT──►│ tunnel bytes untouched (HTTPS)      │──► PSN
console ──GET abs──►│ classify URL (match)                │
                    │ record in tracker                   │
                    │ ready in library? ── yes ─► serve   │──► console (200/206)
                    │        └─ no ─► forward to Sony     │──► Sony CDN
browser ──GET /────►│ dashboard + /api/*                  │
                    └─────────────────────────────────────┘
```

A request is a proxy request when it carries an absolute URL
(`GET http://host/path`). Anything else, such as a browser opening
`http://<pc>:8080/`, goes to the dashboard. An absolute URL that points at
psxdh itself also goes to the dashboard, so there's no loop.

## Packages

| Package | Job |
| --- | --- |
| `cmd/psxdh` | Flags, config, wiring, banner, opening the browser, shutdown |
| `internal/config` | Defaults and the optional YAML file (keys compatible with older releases) |
| `internal/match` | Classifies CDN URLs with the embedded PS4 and PS5 rule packs |
| `internal/proxy` | Forwarding, CONNECT tunnelling, serving library hits, byte counting |
| `internal/library` | Rescans the library folder every second and decides which files are ready |
| `internal/serve` | Streams a file with full `Range` support via `http.ServeContent` |
| `internal/tracker` | Captured files, sizes and bytes sent; saved to `links.json` so links survive restarts |
| `internal/parts` | From one numbered part (`…_N.pkg`), asks Sony which other parts exist |
| `internal/dashboard` | Works out each file's status, serves the embedded page and JSON API |
| `internal/netinfo` | LAN IPv4 addresses for the setup instructions |

## Rules that keep the console safe

- **Never serve a partial file.** A library file is ready only when it has a
  non-temporary name, is non-empty, has no `.aria2` control file, and its
  size and modification time have been stable for 2 seconds.
- **Never serve a wrong-sized file.** When a forward told us the real size
  (from `Content-Length` on a 200, or the total in `Content-Range` on a 206),
  a local file of a different size is skipped.
- **Only CDN files are served from disk.** A URL is served from the library
  only if the rules classify it or it ends in `.pkg`, so an unrelated request
  that shares a file name is never hijacked.
- **Duplicates resolve deterministically.** If several ready files share a
  name, the largest wins, then the shortest path.
- **Stream, never buffer.** Forwarding and serving copy through fixed
  buffers, so memory stays flat for 100 GB games.
- **HTTPS is opaque.** CONNECT is a byte pipe; psxdh never terminates TLS.

## Finding every part

The first time the console has to fetch a numbered package from Sony,
psxdh starts a background search. It requests byte 0 of each other part
number (same URL and query string, the console's User-Agent), checking every
number below the known part in parallel and counting upwards until the first
404. Found parts are listed with their real sizes. No search runs when the
requested part is already on the PC.

## Games vs updates

Every file belongs to a package (its name without the part number, `_sc`
or `-DP`). Packages start as games: all parts are found and offered for
download. The user can mark a package as an update (`POST /api/mode`), which
withdraws its links and stops part searches; the console then fetches only
the pieces it needs. The choice is saved with the links.

psxdh doesn't guess. An earlier build marked a package as an update when the
console jumped far into a file, but a PS5 starts a game by reading the tail
of its last part, so real games were misdetected.

## Clear and running searches

The tracker has a generation number that Clear increments. A part search
records the generation when it starts, and its results are dropped if the
list was cleared meanwhile, so Clear can't be undone by a slow search.

## Why polling instead of fsnotify

The library holds tens of files at most. A rescan every second is cheap,
behaves the same on macOS, Windows, Linux and network drives, needs no
recursive watch bookkeeping, and removed a dependency. See
[ADR 0006](decisions/0006-simplify-to-core.md).

## Dashboard API

| Endpoint | Purpose |
| --- | --- |
| `GET /` | The single-page dashboard (embedded, no external assets) |
| `GET /api/state` | Everything the page shows; polled every second |
| `GET /api/urls.txt` | Captured PKG URLs, one per line. `?need=1` only lists files not on the PC yet; `&download=1` saves as a file |
| `POST /api/clear` | Forget captured URLs (the library folder is not touched) |

The dashboard has no login. It's meant for your home network, and it can't do
anything beyond listing links and clearing that list.

## Testing

- Unit tests per package.
- [cmd/psxdh/e2e_test.go](../cmd/psxdh/e2e_test.go) builds the binary, runs it
  as a separate process, and plays the console (proxy client), Sony
  (a fake CDN reached through `upstream_proxy`, so real host names are used)
  and FDM (writing temp files into the library, then renaming them).
- Not covered automatically: a real PS5 or PS4. See the hardware checklist in
  [roadmap.md](roadmap.md).
