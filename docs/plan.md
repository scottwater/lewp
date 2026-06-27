# Plan: `domains` — local domain router/proxy (V1)

## Context

The repo currently holds only a vision (`overview.md`) and a spec (`docs/requirements.md`,
`docs/technical-appendix.md`) — no code yet. The goal is a macOS-first Go binary that, from
any project-instance directory, leases a stable loopback port, assigns a predictable
`<instance>.<root>.test` hostname, and reverse-proxies browser traffic to a developer-started
process. It must make callback URLs / SSO redirects / cookies / multi-worktree dev predictable
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
   **pin per identity until explicit `domains release`**. Drop the spec's 14-day auto-expiry sweeper
   and per-request `last_seen` writes. Keep a *throttled, informational* `last_seen` for display only.
3. **Internal ports: bare-port lease.** Add `domains port [--name vite]` that leases a unique,
   registered port number with **no hostname/route**, to wire into env like `VITE_RUBY_PORT` and
   kill worktree collisions on internal servers (e.g. Vite's hardcoded 3036).
4. **HTTPS: V1 = stdlib CA + macOS system keychain.** Covers Safari + every Chromium browser
   (Chrome/Brave/Arc/Edge/Helium). Firefox/NSS deferred to vNext (purely additive `certutil` step).
5. **Worktrees: first-class git detection.** Derive `root` from the main repo, `instance` from the
   worktree branch/dir, regardless of physical layout.

## Architecture

One Go binary, two roles: `domains <cmd>` (CLI client) and `domains daemon` (launchd-managed).
CLI talks to daemon over a Unix control socket. Daemon owns: SQLite registry, HTTP(80)/HTTPS(443)
proxy, DNS server for `.test`, control API.

```
cmd/domains/main.go            # cobra-style dispatch: cli vs daemon mode
internal/daemon/               # daemon supervisor, control API over unix socket
internal/proxy/                # httputil.ReverseProxy host->port routing, error page
internal/dns/                  # UDP DNS responder for *.test -> loopback
internal/registry/             # SQLite (modernc.org/sqlite, pure-Go, no cgo for DB)
internal/identity/             # root/name inference, normalization, worktree/branch detection
internal/tls/                  # local CA, SNI leaf minting + cache
internal/launchd/              # plist generation, socket-activation FD handoff (cgo, darwin)
internal/cli/                  # lease/port/release/list/doctor/setup/system commands
```

## Key technical specifics (corrections found vs the references)

These are the things the references get subtly wrong or that the spec under-specifies — bake them
in from the start:

- **DNS does not need a privileged port.** `/etc/resolver/test` accepts a `port` line, so the DNS
  responder listens on a high port (e.g. 15353) via plain `net.ListenPacket`. Only 80/443 go through
  launchd socket activation. Decouples DNS entirely from the privileged-binding problem.
  Reference: `reference/dot-test/server.go` (resolver file shape), `reference/puma-dev/dev/resolver.go`.
- **Bind the proxy to `127.0.0.1` and `::1`, NOT `0.0.0.0`.** puma-dev's plist uses `0.0.0.0`,
  exposing every app to the LAN — which violates our explicit non-goal. Set `SockNodeName=127.0.0.1`.
- **Two-level wildcard routing.** Resolve *anything* under `.test` to loopback at the DNS layer;
  route by **full host** in the registry. Do NOT copy dot-test's `TrimSuffix(host, ".test")` +
  single-label lookup (it breaks `feature-1.audit.test`).
- **Streaming: `FlushInterval = -1`** on the ReverseProxy (immediate flush). puma-dev's `1s` adds up
  to a second of latency to SSE/streamed responses. WebSocket upgrades work by default in modern Go's
  `httputil.ReverseProxy`.
- **Host preservation:** use `ReverseProxy.Rewrite` (Go 1.20+) — set `r.SetXForwarded()` then
  `r.Out.Host = r.In.Host` so the upstream sees `feature-1.audit.test`, not `127.0.0.1:port`.
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

## Identity, normalization, worktrees

`internal/identity` discovery order (per spec): CLI flags → env (`DOMAINS_ROOT`/`DOMAINS_NAME`) →
nearest `.domains.local.toml` → inference. Inference must be *announced* in human output.

- **Default inference:** parent dir → `root`, basename → `instance`.
- **Worktree detection (new):** if `git rev-parse --git-common-dir` differs from `--git-dir`, it's a
  worktree → `root` = main-repo working-dir basename, `instance` = current branch (segment after last
  `/`) or worktree dir basename. Works regardless of where the worktree physically lives.
- **Normalization:** lowercase, basename-after-last-`/`, unsafe→`-`, collapse/trim dashes, DNS label
  limits, warn when output differs, fail with a clear `--name` ask if unusable.
- **Conflict:** clean name first; on real conflict allocate a *deterministic* short suffix derived from
  the folder identity (so repeat leases are stable), warn, show conflicting path.

## Registry schema (SQLite, adjusted)

Keep `identities` and `leases` (per appendix sketch) but treat expiry fields as informational, not
load-bearing. `events` table kept for debugging breadcrumbs (lease created, conflict renamed, released,
forgotten, normalized-name-changed) — **but no per-request rows**.

- `identities(id, root, name, normalized_root, normalized_name, host, path, kind['route'|'port'],
  created_at, updated_at)` — `kind='port'` rows are bare-port leases with no host.
- `leases(id, identity_id, port, state, last_seen_at, released_at, created_at, updated_at)` — no
  active `expires_at` sweeper; `last_seen_at` is throttled/informational.
- Path: `~/Library/Application Support/domains/registry.sqlite`.

## CLI contract

```
domains setup                                   # resolver file, CA+trust, launchd install, port-bind check
domains system start|stop|status|restart|uninstall
domains lease [--root R] [--name N] [--json|--shell]   # routed port + hostname (default env-style output)
domains port [--name vite] [--json|--shell]            # NEW: bare internal port, no hostname
domains release [--forget]
domains list [--all]                            # registry-backed; TCP up/down; no HTTP app probes
domains doctor
```

`lease` default output: `PORT=`, `URL=http://...`, `HOST=...` env lines + inferred-from notes.
`--shell` → `export`; `--json` → full machine fields incl. inference metadata + warnings.

## Build order (incremental, each step independently verifiable)

1. **Skeleton + registry:** binary dispatch, SQLite registry, `identity` inference + normalization +
   worktree detection. Unit-test inference/normalization against spec examples.
2. **DNS + resolver:** UDP `.test`→loopback responder on high port; `setup` writes `/etc/resolver/test`.
   Verify with `dig feature-1.audit.test @127.0.0.1 -p 15353` and (after setup) `ping`/`dscacheutil`.
3. **Proxy (HTTP) + error page:** host→port routing, Host preservation, X-Forwarded-*, WebSocket,
   `FlushInterval=-1`, debug-first HTML error page when target port is closed.
4. **launchd + privileged bind:** plist generation (127.0.0.1 sockets), cgo socket-activation handoff,
   `system start/stop/status`, port-80 bind check in `doctor`.
5. **`lease` / `port` / `release` end-to-end** over the control socket; conflict suffixes; `list`/`doctor`.
6. **HTTPS:** stdlib CA, SNI leaf minting + cache, keychain trust in `setup`, clean uninstall. Output must
   state plainly whether HTTPS is enabled.

## Verification (end-to-end)

- From `~/projects/audit/feature-1`: `domains lease` returns stable `PORT`/`URL`/`HOST`; re-running is
  idempotent.
- `feature-1.audit.test` resolves to loopback after `domains setup`.
- Start a throwaway server on the leased port → `http://feature-1.audit.test` proxies to it; closed port
  shows the debug page, not a blank 502.
- WebSocket + SSE pass through (test with a tiny echo WS + an SSE endpoint).
- Real-stack check: run `thocstock_v2` via `PORT=<leased> bin/dev`; confirm the app loads through the
  `.test` host **and Vite HMR works** (rides the Rails origin via vite_ruby's dev-server proxy). Confirm
  `domains port --name vite` hands out a unique port to set `VITE_RUBY_PORT` so two worktrees don't collide
  on 3036.
- Worktree check: create a git worktree in a non-standard location; confirm `root`/`instance` infer
  correctly without a config file.
- `domains list` shows up/down via TCP only; conflicting names get deterministic suffixes + warnings.
- HTTPS: `https://feature-1.audit.test` is trusted in Safari + a Chromium browser; `system uninstall`
  removes launchd + resolver + keychain cert cleanly.

## Out of scope (V1) / vNext

- Firefox/NSS trust (`certutil`), multi-service-per-instance subdomain routing
  (`vite.feature-1.audit.test`), `.env` mutation, dashboard/sharing/tunnels, auto-expiry sweeper,
  Linux support, proxying non-loopback targets, starting any app process.
