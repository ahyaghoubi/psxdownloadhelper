# ADR 0002: Dependency budget

- Status: Accepted
- Date: 30/05/2026
- Deciders: project owner
- Supersedes: —

## Context

[plan.md §12](../../plan.md#12-dependencies-go) sets a "stdlib-first" posture and lists a short candidate set for Phase 1–2. Without a written budget, dependency creep happens by accident: a contributor adds a logger because slog is "too verbose", a YAML library because koanf seems heavy, and within a release we are maintaining a dependency tree we cannot review.

This ADR codifies what is in, what is out, and how to add anything else.

## Decision

### Allowed in Phase 1–2 without further discussion

| Package | Purpose | Status |
| --- | --- | --- |
| `github.com/spf13/cobra` | CLI commands and flags. | In. |
| `github.com/knadh/koanf` (v2) | Layered config (YAML + env + flags). | In. Preferred over viper. |
| `github.com/fsnotify/fsnotify` | Library folder watcher. | In. |
| `github.com/atotto/clipboard` | "Copy URL" / clipboard fallback for the handoff package. | In. |
| `log/slog` (stdlib) | Structured logging. | In. |
| `embed` (stdlib) | Embed rule packs and web dashboard. | In. |
| `net/http`, `net/url`, `net/http/httptest` (stdlib) | Proxy, admin server, tests. | In. |

### Phase 0 spike only (deleted after ADR 0001 lands)

| Package | Notes |
| --- | --- |
| `github.com/elazarl/goproxy` | Phase 0 comparison only. Stays only if [ADR 0001](0001-proxy-stack.md) lands on Option B. |

### Phase 3 and release

| Package | Purpose |
| --- | --- |
| `github.com/goreleaser/goreleaser` (build tool, not a library import) | Cross-platform release artefacts. |

### Phase 4 (post-v1.0, requires its own ADR)

| Candidate | Purpose |
| --- | --- |
| `github.com/charmbracelet/bubbletea` | Optional TUI (only if [plan.md §8 Phase 4](../../plan.md#phase-4--optional-enhancements) ships it). |

### Explicitly excluded for v1

- Any HTTP framework (gin, chi, echo) — stdlib `net/http` is sufficient for the admin server.
- Any frontend framework (React, Vue, Svelte) — the dashboard is vanilla HTML + JS embedded via `embed.FS`.
- Any ORM or database driver — the v1 session store is in-memory.
- Any test framework beyond stdlib `testing` + `testify/require` (the latter requires a separate ADR if proposed).
- Any logging library that wraps `slog` (zap, zerolog) — stdlib is the floor.
- Any YAML library beyond what koanf brings in transitively.

## Process for adding a new dependency

1. Open a new ADR (`docs/decisions/NNNN-<package>.md`).
2. State: what stdlib gap it fills, what alternatives were considered, licence (must be MIT/BSD/Apache-2.0 compatible — never GPL into this project per [plan.md §11](../../plan.md#11-legal-ethics--licence)), maintenance signal (last release, open issues).
3. Land the ADR before adding the import.

## Consequences

- `go.mod` size stays auditable; security review remains tractable.
- Refusing dependencies is a default behaviour, not an exception. Contributors must justify adding, not removing.
- This ADR is the canonical reference for code reviewers: a PR adding an unlisted import is rejected unless it ships with its own ADR.
