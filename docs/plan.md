# Plan: `lewp` — local domain router/proxy (V1)

## Context

This plan is now implemented: the single Go binary ships the CLI and daemon roles, the SQLite
registry, the `.lewp` DNS responder, the HTTP/HTTPS proxy, and the launchd integration. It is
retained as the design rationale and history behind those decisions; the user-facing contract
lives in `README.md` and `DOCUMENTATION.md`. The goal is a macOS-first Go binary that, from
any project-instance directory, leases a stable loopback port, assigns a predictable
`.lewp` hostname, and reverse-proxies browser traffic to a developer-started process. It must
make callback URLs / SSO redirects / cookies / multi-worktree dev predictable
**without becoming a process manager** (the explicit boundary that keeps it debuggable, unlike
puma-dev's auto-booting apps).

This plan reflects design decisions made after grounding the spec against the four reference
implementations in `reference/` (`dot-test`, `puma-dev`, `outport`, `caddy`). The references are
read-only guides, not dependencies — this is a fresh binary.

### Decisions locked with the user (these refine the spec)

1. **Privileged ports (80/443): launchd socket activation.** A *user* LaunchAgent where launchd
   binds the ports and hands FDs to a non-root daemon (puma-dev's model). Needs a small cgo shim
   around `launch_activate_socket()`. No setuid, no root daemon.
2. **Port model: registry, no sweeper.** Keep the full queryable SQLite registry (the
   differentiator for orchestrating LLM-driven local services). Assign ports by free-port scan and
   **pin per identity until explicit `lewp release`**. No 14-day auto-expiry sweeper and no
   per-request `last_seen` writes. Keep a *throttled, informational* `last_seen` for display only.
3. **Internal ports: bare-port lease.** Add `lewp port [--name vite]` that leases a unique,
   registered port number with **no hostname/route**, to wire into env like `VITE_RUBY_PORT` and
   kill worktree collisions on internal servers (e.g. Vite's hardcoded 3036).
4. **HTTPS: V1 = stdlib CA + macOS system keychain.** Covers Safari + every Chromium browser
   (Chrome/Brave/Arc/Edge/Helium). Firefox/NSS deferred to vNext (purely additive `certutil` step).
5. **Worktrees: first-class git detection.** Derive `root` from the main repo, `instance` from the
   worktree branch/dir, regardless of physical layout.

## Architecture

One Go binary, two roles: `lewp <cmd>` (CLI client) and `lewp daemon` (launchd-managed).
CLI talks to daemon over a Unix control socket. Daemon owns: SQLite registry, HTTP(80)/HTTPS(443)
proxy, DNS server for `.lewp`, control API.

```
cmd/lewp/main.go            # cobra-style dispatch: cli vs daemon mode
internal/daemon/               # daemon supervisor, control API over unix socket
internal/proxy/                # httputil.ReverseProxy host->port routing, error page
internal/dns/                  # UDP DNS responder for *.lewp -> loopback
internal/registry/             # SQLite (modernc.org/sqlite, pure-Go, no cgo for DB)
internal/identity/             # root/name inference, normalization, worktree/branch detection
internal/tls/                  # local CA, SNI leaf minting + cache
internal/launchd/              # plist generation, socket-activation FD handoff (cgo, darwin)
internal/cli/                  # lease/port/release/list/doctor/setup/system commands
```

## Key technical specifics (corrections found vs the references)

These are the things the references get subtly wrong or that the spec under-specifies — bake them
in from the start:

- **DNS does not need a privileged port.** `/etc/resolver/lewp` accepts a `port` line, so the DNS
  responder listens on a high port (e.g. 15353) via plain `net.ListenPacket`. Only 80/443 go through
  launchd socket activation. Decouples DNS entirely from the privileged-binding problem.
  Reference: `reference/dot-test/server.go` (resolver file shape), `reference/puma-dev/dev/resolver.go`.
- **Bind the proxy to `127.0.0.1` and `::1`, NOT `0.0.0.0`.** puma-dev's plist uses `0.0.0.0`,
  exposing every app to the LAN — which violates our explicit non-goal. Set `SockNodeName=127.0.0.1`.
- **Full-host wildcard routing.** Resolve *anything* under `.lewp` to loopback at the DNS layer;
  route by **full host** in the registry. Do NOT copy dot-test's `TrimSuffix(host, ".lewp")` +
  single-label lookup (it breaks `feature-1.atlas.lewp`).
- **Streaming: `FlushInterval = -1`** on the ReverseProxy (immediate flush). puma-dev's `1s` adds up
  to a second of latency to SSE/streamed responses. WebSocket upgrades work by default in modern Go's
  `httputil.ReverseProxy`.
- **Host preservation:** use `ReverseProxy.Rewrite` (Go 1.20+) — set `r.SetXForwarded()` then
  `r.Out.Host = r.In.Host` so the upstream sees `feature-1.atlas.lewp`, not `127.0.0.1:port`.
  Pass `X-Forwarded-Host/Proto/For`.
- **`last_seen` is throttled, best-effort, in-memory-coalesced** (flush at most every ~N seconds per
  lease). Never one DB write per request. The appendix's "route hit" event is informational only — do
  NOT persist a row per request (unbounded write firehose).
- **Local CA = stdlib `crypto/x509`** (~150 lines; puma-dev `ssl.go` / outport `certmanager` pattern),
  NOT certmagic/smallstep (Caddy's heavy path, overkill here). RSA-2048 CA, ECDSA-P256 leaves minted in
  the SNI `GetCertificate` callback, LRU-cached. Trust via `security add-trusted-cert -k <login keychain>`
  (one-time auth prompt — document it). Uninstall removes by CN.
- **Socket activation requires cgo on darwin** (`launch_activate_socket`). Reference:
  `reference/puma-dev/dev/launch/launch_darwin.go`. Keep DB driver pure-Go (`modernc.org/sqlite`) so cgo
  is confined to this one darwin file.

## Host shapes and customization

Subdomain-per-instance is the default workflow, but V1 must not be subdomain-only. DNS should
resolve any `.lewp` name to loopback, and the proxy should route by exact registered host. That
means both of these are first-class route shapes:

- **Instance host:** `<instance>.<root>.lewp` (default), e.g. `feature-1.atlas.lewp`.
- **Project apex host:** `<root>.lewp` (explicit), e.g. `atlas.lewp`.

Use "project apex" for `<root>.lewp` in docs/UI to avoid confusing it with the `.lewp` suffix
itself. Apex routes are useful for known, stable local apps where the project name should be the
whole local domain. The typical multi-worktree case still uses instance hosts.

V1 should also leave room for explicit custom `.lewp` hosts, as long as they remain loopback-only
and inside the owned suffix:

```sh
lewp add --host atlas.lewp
lewp add --host sso.atlas.lewp
```

`--host` is an override for the final registered hostname. It must be normalized/validated as a
full `.lewp` hostname, persisted for the current folder like other CLI overrides, included in
`--json`, and subject to the same deterministic conflict behavior as inferred hosts. It must not
allow non-`.lewp` domains in V1.

## Identity, normalization, worktrees

`internal/identity` discovery order (per current CLI/daemon request shape, extended for host
overrides): CLI flags (`--root`/`--name`/`--host`) → nearest `.lewp.local.toml` (`root`, `name`,
optional `host`) → inference. Inference must be *announced* in human output.

- **Default inference:** parent dir → `root`, basename → `instance`.
- **Default host:** `<normalized_instance>.<normalized_root>.lewp`.
- **Explicit host:** `--host` wins over host inference while preserving root/name metadata for
  list/debug output.
- **Worktree detection (new):** if `git rev-parse --git-common-dir` differs from `--git-dir`, it's a
  worktree → `root` = main-repo working-dir basename, `instance` = current branch (segment after last
  `/`) or worktree dir basename. Works regardless of where the worktree physically lives.
- **Normalization:** lowercase, basename-after-last-`/`, unsafe→`-`, collapse/trim dashes, DNS label
  limits, warn when output differs, fail with a clear `--name` ask if unusable.
- **Conflict:** clean name first; on real conflict allocate a *deterministic* short suffix derived from
  the folder identity (so repeat leases are stable), warn, show conflicting path.

## Registry schema (SQLite, adjusted)

Keep `identities` and `leases` (per appendix sketch), with release state explicit and no active
expiry model. `events` table kept for debugging breadcrumbs (lease created, conflict renamed,
released, forgotten, normalized-name-changed) — **but no per-request rows**.

- `identities(id, root, name, normalized_root, normalized_name, host, host_kind['instance'|'apex'|'custom'],
  host_source['inferred'|'cli'|'env'|'config'], path, kind['route'|'port'], created_at, updated_at)` —
  `kind='port'` rows are bare-port leases with no host.
- `leases(id, identity_id, port, state, last_seen_at, released_at, created_at, updated_at)` — no
  expiry field or sweeper; `last_seen_at` is throttled/informational.
- Path: `~/Library/Application Support/lewp/registry.sqlite`.

## Port allocation

Default routed-app port range: `41000-49999`. Allocate by scanning for a free loopback port in that
range, then persist the assignment so the same remembered identity reuses it. Never silently steal a
live assignment. Requested routed-app ports (`lewp add --port`) are **out of V1**; add later only if
the default range proves insufficient. `lewp port --name ...` uses the same registry-backed allocator
for bare internal ports.

## CLI contract

```
lewp setup                                   # resolver file, CA+trust, launchd install, port-bind check
lewp system start|stop|status|restart|uninstall
lewp add [--root R] [--name N] [--host H] [--json|--shell] # routed port + hostname
lewp port [--name vite] [--json|--shell]            # NEW: bare internal port, no hostname
lewp release [--forget]
lewp list [--all]                            # registry-backed; TCP up/down; no HTTP app probes
lewp doctor
```

`add` default output: `PORT=`, `URL=http://...`, `HOST=...` env lines + inferred-from notes.
`--shell` → `export`; `--json` → full machine fields incl. inference metadata + warnings.

If the daemon is not running, CLI commands fail with a direct fix, not an implicit background start:

```text
lewp daemon is not running
Run: lewp system start
```

Optional local config file:

```toml
# .lewp.local.toml
root = "atlas"
name = "feature-1"
host = "atlas.lewp" # optional full-host override
```

The file is local/uncommitted by convention. `lewp init` is out of V1; `add` must work without it
through flags, env vars, or inference.

## Status, doctor, and error output

`lewp list` uses TCP checks only, never HTTP probes. States:

- `up`: TCP connection to target port succeeds.
- `down`: TCP connection fails or is refused.
- `stale`: path is missing or identity can no longer be resolved.

Default `list` shows active and recently inactive entries. `list --all` includes released/stale
history.

The proxy error page for a registered-but-closed target must include: requested host, loopback target,
project path, root/name, last seen time, release state, suggested start command, and hints for
`lewp list` / `lewp doctor`.

`lewp doctor` checks: daemon running, control socket reachable, resolver file exists, `.lewp` lookup
resolves to loopback, proxy can bind or is bound on port 80, HTTPS CA state when enabled, registry
readable, current folder identity/host inference, current lease target port state, and hostname
conflicts. Output should be concrete and command-oriented.

## Build order (incremental, each step independently verifiable)

1. **Skeleton + registry:** binary dispatch, SQLite registry, `identity` inference + normalization +
   worktree detection. Unit-test inference/normalization against spec examples.
2. **DNS + resolver:** UDP `.lewp`→loopback responder on high port; `setup` writes `/etc/resolver/lewp`.
   Verify with `dig feature-1.atlas.lewp @127.0.0.1 -p 15353`, `dig atlas.lewp @127.0.0.1 -p 15353`,
   and (after setup) `ping`/`dscacheutil`.
3. **Proxy (HTTP) + error page:** host→port routing, Host preservation, X-Forwarded-*, WebSocket,
   `FlushInterval=-1`, debug-first HTML error page when target port is closed.
4. **launchd + privileged bind:** plist generation (127.0.0.1 sockets), cgo socket-activation handoff,
   `system start/stop/status`, port-80 bind check in `doctor`.
5. **`add` / `port` / `release` end-to-end** over the control socket; conflict suffixes; `list`/`doctor`.
6. **HTTPS:** stdlib CA, SNI leaf minting + cache, keychain trust in `setup`, clean uninstall. Output must
   state plainly whether HTTPS is enabled.

## Verification (end-to-end)

- From `~/projects/atlas/feature-1`: `lewp add` returns stable `PORT`/`URL`/`HOST`; re-running is
  idempotent.
- `feature-1.atlas.lewp` resolves to loopback after `lewp setup`.
- `lewp add --host atlas.lewp` returns a stable project apex host; `https://atlas.lewp` resolves,
  routes, preserves Host, and conflicts deterministically.
- Start a throwaway server on the leased port → `http://feature-1.atlas.lewp` proxies to it; closed port
  shows the debug page, not a blank 502.
- WebSocket + SSE pass through (test with a tiny echo WS + an SSE endpoint).
- Real-stack check: run `thocstock_v2` via `PORT=<leased> bin/dev`; confirm the app loads through the
  `.lewp` host **and Vite HMR works** (rides the Rails origin via vite_ruby's dev-server proxy). Confirm
  `lewp port --name vite` hands out a unique port to set `VITE_RUBY_PORT` so two worktrees don't collide
  on 3036.
- Worktree check: create a git worktree in a non-standard location; confirm `root`/`instance` infer
  correctly without a config file.
- `lewp list` shows up/down/stale via TCP only; `--all` includes released/stale history; conflicting
  names get deterministic suffixes + warnings.
- HTTPS: `https://feature-1.atlas.lewp` is trusted in Safari + a Chromium browser; `system uninstall`
  removes launchd + resolver + keychain cert cleanly.

## Out of scope (V1) / vNext

- Firefox/NSS trust (`certutil`), multi-service-per-instance subdomain routing
  (`vite.feature-1.atlas.lewp`), `.env` mutation, dashboard/sharing/tunnels, auto-expiry sweeper,
  Linux support, proxying non-loopback targets, starting any app process.
