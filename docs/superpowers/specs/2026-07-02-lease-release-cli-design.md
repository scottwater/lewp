# Lease/Release CLI Redesign

**Date:** 2026-07-02
**Status:** Approved design, not yet implemented

## Problem

The CLI's acquire/release surface has drifted into mixed grammar and split
responsibilities:

- `lewp add` acquires a route, but "lease" is the domain vocabulary everywhere
  else — docs ("lease a stable loopback port"), JSON (`lease_state`), and the
  registry. `add`/`release` is an asymmetric verb pair, and `add` is overloaded
  with `alias add`.
- Releasing is split across two commands with different grammars:
  `lewp release [--all] [--forget]` (top-level verb) and
  `lewp port release [--name N] [--forget]` (noun-scoped). Bare
  `lewp release` frees the route + aliases but silently leaves bare ports
  unless `--all` is passed.

## Decisions

Mental model: **the route is primary; bare ports are a sidecar utility.**
Acquisition is per-resource; releasing is centralized under one verb.

### Acquire

| Command | Behavior |
|---|---|
| `lewp lease` | Route for the current directory. Rename of `lewp add`; all flags carry over unchanged: `--root`, `--name`, `--host`, `--auto-suffix`, `--reset`, `--json`, `--shell`. |
| `lewp port [--name N]` | Bare port. Unchanged. |
| `lewp alias add <host>` | Extra hostname on the active route. Unchanged. |

No `add` alias is kept. The earlier removal of the `lease` alias
(commit `5037012`) showed that two names for one command is the actual
problem; the rename is clean.

### Release

| Command | Behavior |
|---|---|
| `lewp release` | Frees **everything** for the directory: route, aliases, and all bare ports. (Behavior change: this is today's `release --all`.) |
| `lewp release --port [N]` | Frees one bare port. `N` defaults to `port`, mirroring `lewp port` with no `--name`. Replaces `lewp port release`. |
| `lewp release --route` | Frees the route + aliases only, keeping bare ports. (Today's bare `release` default, now the explicit exception.) |
| `lewp release --forget` | Composes with any scope above; also drops remembered identity/history **within the selected scope** (e.g. `--port vite --forget` forgets only that port's identity, matching today's `port release --forget`). |
| `lewp alias remove <host>` | Unchanged — single-alias removal stays with the alias noun. |

Rules:

- `--route` and `--port` together are rejected as conflicting scopes.
- Release stays idempotent. No-op messages and exit `0` are preserved; bare
  `lewp release` with nothing active reports
  `no active route or port for this directory`.
- Release counts keep working; the default scope now counts route + aliases +
  bare ports freed.

### Removed

- `lewp port release` (subcommand deleted).
- `lewp release --all` (superseded by the new default; unknown-flag error is
  acceptable for the current user base).
- No back-compat alias for `add`.

## Rationale

`lease` and `release` become true inverses: `lease` gives the directory what
it needs, `release` gives it all back. Nouns (`port`, `alias`, `suffix`,
`system`) manage their own acquisition, but freeing always lives in one
place. "lease" stops being a word the docs use but the CLI hides.

Accepted asymmetry: `lewp port` acquires under a noun while its release lives
under `release --port`. This is deliberate for a sidecar resource —
acquisition is per-resource, releasing is centralized.

## Scope of change

- `internal/cli`: command table, `lease` rename, `release` scope flags,
  removal of `port release`, help text, static completions.
- Error/help messages that say "run `lewp add` first" (e.g. `alias add`)
  change to `lewp lease`.
- Control layer: no protocol changes expected — release-by-scope
  (route vs named port vs all) already exists; the CLI remaps which scope the
  default and flags select.
- Docs: `README.md`, `DOCUMENTATION.md` (`add` → `lease` section, merged
  release section, cross-references), `docs/requirements.md` command
  references.

## Testing

- CLI table tests: `lease` resolves with all carried-over flags; `add` and
  `port release` are unknown.
- Release scope tests: default frees route + aliases + ports; `--port [N]`
  frees exactly one named port; `--route` leaves ports; `--route --port`
  errors; `--forget` composes; no-op messages and exit codes for each scope.
- Docs/help snapshot updates.
