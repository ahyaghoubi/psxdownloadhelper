# Sony CDN URL patterns

This document is the reference for the URL hostnames and path shapes that
`psxdh` recognises on the PlayStation CDNs, plus the classification rules
that turn a raw URL into a labelled `match.Kind`.

It is the user-facing companion to `internal/match/rules/*.yaml`. Whenever
the rule packs change, this document must change with them.

Primary external references:

- [PS4 Online Connections (psdevwiki)](https://www.psdevwiki.com/ps4/Online_Connections)
- [PS5 Online Connections (psdevwiki)](https://www.psdevwiki.com/ps5/Online_Connections)

---

## PS4

| Asset | Host / path pattern | Notes |
| --- | --- | --- |
| Base game PKG | `http://gs2.ww.prod.dl.playstation.net/gs2/appkgo/.../*.pkg` | Often chunked `_0` … `_N` |
| Base manifest | `.../appkgo/.../*.json` | JSON describing the part list |
| Patch manifest | `.../ppkgo/.../*.json` | Prefer `/appkgo/` chunks for install; pure metadata-only URLs may not advance the install bar |
| Patch PKG | `.../ppkgo/.../*.pkg` | Patch chunks |
| Delta PKG | `.../*-DP.pkg` | Cumulative delta package |
| Secured manifest variant | `gs2-sec.ww.prod.dl.playstation.net/.../{appkgo,ppkgo}/.../*.json` | Same shape, different host |

**Query parameters:** `downloadId`, `du`, `q`, and similar are required by
Sony's CDN signing — they **must** be preserved end-to-end (proxy log, FDM
URL, retry upstream). The proxy never strips them.

Historical caveat: the legacy Node port (`harryi3t/psx-download-helper-nodejs`)
matched only `gs2.ww.prod.dl.playstation.net` with `?downloadId`. That regex
is too narrow for full app coverage; modern downloads also use the variants
above. See [research.md](research.md) for the historical landscape.

---

## PS5

| Asset | Host / path pattern | Notes |
| --- | --- | --- |
| Title update XML | `https://sgst.prod.dl.playstation.net/.../*-version.xml` | Entry point for update discovery (often HTTPS) |
| Update JSON | `.../app/info/.../*.json` | Links to update pieces |
| PlayGo / info PKG | `.../*_sc.pkg` | **Critical** — embeds `param.json` metadata |
| Application PKG | `http://gst.prod.dl.playstation.net/.../app/pkg/.../*.pkg` | May be split into many parts |
| Delta PKG | `.../*-DP.pkg` | Cumulative patch package |
| Chunk CRC | `.../*.crc` | May be required alongside its PKG (validate in Phase 0 hardware capture) |
| NP / title paths | `.../sgst/prod/00/np/{TITLE_ID}/...` | Title-id centric layout (`PPSA…`, `UP1234-CUSA…`) |

### Differences that shape the design

1. **Hostnames:** `sgst.prod.dl` + `gst.prod.dl` (PS5) vs `gs2.ww.prod.dl`
   (PS4).
2. **Naming:** `_sc.pkg`, `-DP.pkg`, `version.xml` vs `appkgo` / `ppkgo`
   trees.
3. **Manifest graph:** JSON/XML indirection — a single "game" can be many
   URLs.
4. **HTTPS vs HTTP:** Update metadata is often HTTPS on `sgst.prod`; game
   chunk PKGs are reported as plain HTTP on `gst.prod`. Sony can change
   this — [Phase 0](roadmap.md#phase-0--research--validation) must confirm
   on current firmware.

---

## Classification rules

Rules live in `internal/match/rules/`:

- `ps4.yaml` — default PS4 pack
- `ps5.yaml` — default PS5 pack

Both packs are embedded via `embed.FS`. The on-disk format is YAML:

```yaml
platform: ps5
rules:
  - kind: pkg-delta
    host_suffix: prod.dl.playstation.net
    path_regex: -DP\.pkg$
```

### Rule semantics

| Field | Meaning |
| --- | --- |
| `kind` | One of `pkg-base`, `pkg-patch`, `pkg-app`, `pkg-sc`, `pkg-delta`, `manifest-json`, `manifest-xml`, `crc`, `ignore`. |
| `host_suffix` | Matches either the exact host or any subdomain of it (e.g. `prod.dl.playstation.net` matches both `gst.prod.dl.playstation.net` and `a.gst.prod.dl.playstation.net`). Empty means "any host". Ports are stripped on both sides. |
| `path_regex` | Go `regexp` pattern matched against `url.URL.Path` (the path component, no query string). |

The `RuleSet` is an **ordered list**; first match wins. That ordering is why
the rule packs put `pkg-delta` (`-DP.pkg`) and `pkg-sc` (`_sc.pkg`) above the
generic `pkg-app` rule on PS5, and `pkg-delta` above `pkg-patch` on PS4.

A URL that no rule matches gets `KindUnknown`. The proxy still publishes a
capture event for unknown URLs **only when** `capture.log_ignored: true` is
set, so the dashboard isn't flooded with PSN auth / store / icon traffic in
the default config.

### Kinds reference

| Kind | Platform(s) | Pattern hint |
| --- | --- | --- |
| `pkg-base` | PS4 | `/appkgo/` + `.pkg` |
| `pkg-patch` | PS4 | `/ppkgo/` + `.pkg` (and not `-DP.pkg`) |
| `pkg-app` | PS5 | `/app/pkg/` + `.pkg` (and not `_sc.pkg` / `-DP.pkg`) |
| `pkg-sc` | PS5 | `_sc.pkg` |
| `pkg-delta` | PS4 + PS5 | `-DP.pkg` |
| `manifest-json` | PS4 + PS5 | `.json` under the appropriate manifest path |
| `manifest-xml` | PS5 | `version.xml` |
| `crc` | PS5 | `.crc` chunk checksum |
| `ignore` | both | Configurable (icons, `pronunciation.xml`, small static assets) |
| `unknown` | n/a | Internal placeholder; no rule matched |

### Hint extraction

Regardless of which rule (if any) matched, `match.Classify` also extracts a
`Hint`:

```go
type Hint struct {
    TitleHint string // PPSA01234 / CUSA12345 / UP1234-CUSA12345
    PartIndex int    // _0, _1, ...  (-1 when not parseable)
}
```

`TitleHint` powers the future session aggregator and the `per-title` library
layout (see [configuration.md](configuration.md)). `PartIndex` is parsed
from a trailing `_N.pkg` or `_N.crc`.

---

## Overriding the default rules

To change the rule packs without recompiling, point `match.rules_dir` at a
directory of `*.yaml` files:

```yaml
match:
  ps4: true        # ignored when rules_dir is set
  ps5: true        # ignored when rules_dir is set
  rules_dir: "/etc/psxdh/rules"
```

When `rules_dir` is set, the loader scans `*.yaml` files in lexical order
and **replaces** the embedded defaults entirely. Filenames therefore control
priority — name a file `00-overrides.yaml` to land at the top of the rule
chain.

A community-contributed rule pack should ship redacted URL fixtures under
`testdata/urls/` so the change is reviewable; the `match` package's golden
tests will fail-fast if a rule broadens beyond its evidence.

---

## Validating the rule packs

Run the package tests:

```bash
go test ./internal/match/...
```

And for the larger end-to-end coverage:

```bash
go test ./e2e/...
```

The `e2e` tests include `TestPhase1_CapturePublishesAllParts`, which writes a
loopback override rule and verifies that every classified URL the console
requests reaches the capture bus — a smoke test for "the rule pack matches
the URL shape we expect".
