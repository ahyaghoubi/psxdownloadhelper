# Research, prior art, and dependencies

This document captures the landscape `psxdh` is built into: the prior-art
tools, the dependency budget, and the legal / licence posture. It is the
"why this exists" companion to [roadmap.md](roadmap.md).

> Project metadata below was correct at last check (30/05/2026). Verify
> before relying on specifics — any of these projects can be archived,
> renamed, or relicensed.

---

## Prior art

The original draft of this project leaned on 2014–2021 repos that no longer
represent how the PS4 / PS5 CDN behaves. Treat older repos as **historical
reference only**; validate everything against current tools and current
hardware.

### Still relevant on stock PS4 / PS5 (no exploit)

| Project | Role vs `psxdh` | Notes |
| --- | --- | --- |
| [ghost1372/PSXMaster](https://github.com/ghost1372/PSXMaster) | **Primary modern reference / competitor** | WinUI, MIT, PS4 + PS5 "Game Transfer" proxy. Default port **8080**, broad match rules (`*.pkg` / `*.pup`), recursive filename lookup in library tree, Range-aware local serve, raw socket HTTP parser (not `goproxy`). Tunnels `CONNECT` without MITM. |
| [KOPElan/PSX-Download-Helper](https://github.com/KOPElan/PSX-Download-Helper) | Historical architecture | GPL-3.0 WinForms; first implementation of intercept + replace. Do **not** copy code — clean-room only. |
| [harryi3t/psx-download-helper-nodejs](https://github.com/harryi3t/psx-download-helper-nodejs) | Pattern reference only | Single PS4 regex; weak passthrough; not a PS5 baseline. |
| [lyling/x-download-helper](https://github.com/lyling/x-download-helper) | Go spike only | No PS5 rules, no Range. Not a baseline. |

**PSXMaster behaviour worth replicating** (from source + community
reports — confirm in Phase 0):

- Game PKGs on PS5 typically observed on
  `http://gst.prod.dl.playstation.net/...` with multi-part names
  (`..._0.pkg`, `..._1.pkg`, …).
- Match rules deliberately broad: `*.pkg` / `*.pup` extension match,
  configurable.
- Logs the URL **before** the query string for display; local serve uses
  path + `Range` + `206 Partial Content`.
- Setup: LAN cable, console HTTP proxy → PC IP:port.
- `CONNECT` requests are tunnelled when no local file exists; HTTPS PSN
  traffic is **never** decrypted.
- Community guides occasionally mention a custom primary DNS
  (`165.227.83.145`). Treat as **author-specific / unverified** — do not
  hardcode in v1; [Phase 0](roadmap.md#phase-0--research--validation) to
  confirm whether it is ever actually required.

### Related tools (different problem; borrow ideas only)

| Project | Audience | Overlap |
| --- | --- | --- |
| [IRONB0SS/PSXhub](https://github.com/IRONB0SS/PSXhub) | Retail PKG LAN transfer | Not a proxy. Strong PS5 PKG diff ("only download parts 0, 22, 23"). Inspiration for an optional partial-update advisor (Phase 4). GPL-3.0 — keep clean-room. |
| [ps5-payload-dev/fetchpkg](https://github.com/ps5-payload-dev/fetchpkg) | Jailbroken PS5 | Manifest-driven official update download; supports `-p http://proxy:8080`. Useful for manifest URL examples, not the stock-console flow. GPLv3+. |
| [marcussacana/DirectPackageInstaller](https://github.com/marcussacana/DirectPackageInstaller) | Exploit / RPI / etaHEN | Proxied PKG install on exploited consoles. Different stack. |
| [Ailyth99/RewindPS4](https://github.com/Ailyth99/RewindPS4) | PS4 version downgrade | Uses `elazarl/goproxy`; author confirms "PS4 games only, not PS5 games". Good reference for Go proxy patterns. |
| ps5upload / PS5 Vault style tools | Homebrew transfer & backup | Fast LAN to exploited PS5. Out of scope for retail PSN proxy. |

### Positioning — why build this in Go anyway?

PSXMaster is the functional bar on Windows. A Go rewrite is justified if we
deliver:

1. **macOS + Linux** as first-class (PSXMaster is Windows / Microsoft Store
   focused).
2. **Headless CLI + NAS** operation without WinUI.
3. **FDM-first handoff** (clipboard, deep link, batch import) with no
   built-in downloader complexity.
4. **External, versioned URL rule packs** and open, testable `match`
   fixtures.
5. **Session model** with progress tracking and missing-part detection.
6. **No vendor-specific DNS** unless empirically required.
7. **Optional, later:** PSXhub-style chunk diff (clean-room, MIT) for
   delta-only update flows.

---

## Legal, ethics & licence

- The tool is for **content the user already owns** on PSN — same intent
  as the original PSX Download Helper.
- The README must state: not affiliated with Sony; the user is responsible
  for compliance with PSN ToS and local law.
- Do not bundle Sony copyrighted material, leaked keys, or pre-populated
  URL lists for specific titles.
- **Licence:** **MIT** with attribution to prior-art projects. If any
  GPL-3.0 code is ever copied from KOPElan or PSXhub the whole project
  would have to become GPL-3.0 — so the rule is **clean-room only**, study
  PSXMaster (MIT) when a reference is needed.

---

## Dependencies

The project is **stdlib-first**. Direct dependencies are kept minimal and
gated by [ADR 0002](decisions/0002-dependency-budget.md).

### Currently in `go.mod`

| Package | Purpose |
| --- | --- |
| `github.com/spf13/cobra` | CLI commands and flags. |
| `github.com/fsnotify/fsnotify` | Library folder watcher. |
| `gopkg.in/yaml.v3` | YAML config + rule pack loading. |

### Planned (gated by ADRs)

| Package | Phase | Purpose |
| --- | --- | --- |
| `github.com/knadh/koanf` (v2) | Phase 3 | Layered config (YAML + env + flags). Preferred over viper. |
| `github.com/atotto/clipboard` | Phase 2 | "Copy URL" / clipboard fallback for the handoff package. |
| `github.com/goreleaser/goreleaser` | Phase 3 | Cross-platform release artefacts (build tool, not a library import). |
| `github.com/charmbracelet/bubbletea` | Phase 4 | Optional TUI. Requires its own ADR per [ADR 0002](decisions/0002-dependency-budget.md). |

### Explicitly excluded for v1

- Any HTTP framework (gin, chi, echo) — stdlib `net/http` is sufficient
  for the admin server.
- Any frontend framework (React, Vue, Svelte) — the dashboard is vanilla
  HTML + JS embedded via `embed.FS`.
- Any ORM or database driver — the v1 session store is in-memory.
- Any test framework beyond stdlib `testing` (+ `testify/require` only if
  proposed via its own ADR).
- Any logging library that wraps `slog` (zap, zerolog) — stdlib is the
  floor.
- Any YAML library beyond what koanf brings in transitively.

### Process for adding a new dependency

1. Open a new ADR (`docs/decisions/NNNN-<package>.md`).
2. State: what stdlib gap it fills, what alternatives were considered,
   licence (must be MIT/BSD/Apache-2.0 compatible — never GPL into this
   project per the [licence rule](#legal-ethics--licence)), maintenance
   signal (last release, open issues).
3. Land the ADR before adding the import.

---

## External references

### Active — study these

- [ghost1372/PSXMaster](https://github.com/ghost1372/PSXMaster) — primary
  modern reference (MIT, WinUI). Read `dev/PSXMaster/Core/HttpClient.cs` for
  proxy / serve behaviour.
- [IRONB0SS/PSXhub](https://github.com/IRONB0SS/PSXhub) — PS5 PKG comparison
  / partial-chunk ideas (GPL-3.0; clean-room only).
- [ps5-payload-dev/fetchpkg](https://github.com/ps5-payload-dev/fetchpkg) —
  manifest-based official PKG fetch (exploit-scene; useful for manifest URL
  shape).
- [marcussacana/DirectPackageInstaller](https://github.com/marcussacana/DirectPackageInstaller) —
  proxied install on exploited consoles.
- [Ailyth99/RewindPS4](https://github.com/Ailyth99/RewindPS4) — Go
  `goproxy` patterns; PS4 downgrade only.

### Historical — architecture lineage only

- [KOPElan/PSX-Download-Helper](https://github.com/KOPElan/PSX-Download-Helper) —
  original .NET WinForms, GPL-3.0. Do not read source for clean-room reasons.
- [harryi3t/psx-download-helper-nodejs](https://github.com/harryi3t/psx-download-helper-nodejs) —
  minimal Node proxy (2016 era).
- [lyling/x-download-helper](https://github.com/lyling/x-download-helper) —
  Go learning sketch.

### Documentation

- [PS4 Online Connections (psdevwiki)](https://www.psdevwiki.com/ps4/Online_Connections)
- [PS5 Online Connections (psdevwiki)](https://www.psdevwiki.com/ps5/Online_Connections)
- [Free Download Manager](https://www.freedownloadmanager.org/) — confirm
  current FDM handoff capabilities in Phase 0.
- [PSXHAX PSX DH guide (legacy)](https://www.psxhax.com/threads/psx-download-helper-transfer-ps4-game-data-via-pc-guide.2178/)
