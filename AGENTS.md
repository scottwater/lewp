# Lewp

Lewp is a macOS-first local domain router/proxy for parallel local development.

From any project-instance directory, it leases a stable loopback port, assigns a
predictable `<instance>.<root>.lewp` hostname, and reverse-proxies browser
traffic to a developer-started process. The goal is to make callback URLs, SSO
redirects, cookies, and multi-worktree development predictable without becoming
a process manager — it routes traffic but never starts app processes.

It is a single Go binary with two roles: a CLI client (`lewp <cmd>`) and a
launchd-managed daemon that owns the SQLite registry, HTTP/HTTPS proxy, `.lewp`
DNS responder, and a local control socket.

## Status

Pre-implementation. The repo currently holds a vision (`overview.md`) and a spec
under `docs/`:

- `docs/requirements.md` — the V1 product contract and CLI behavior
- `docs/plan.md` — the build plan and locked design decisions
- `docs/technical-appendix.md` — reference-project observations and open
  implementation questions

Reference implementations (read-only guides, not dependencies) live in
`reference/`.

## Working in this repo

When implementing or modifying Lewp, read `docs/` first and follow the design
decisions recorded in `docs/plan.md` (resolver file shape, loopback-only binds,
two-level wildcard routing, stdlib local CA, launchd socket activation for
privileged ports, etc.). Keep the V1 boundary in mind: route and lease only —
never start app processes, expose services to the LAN, or proxy non-loopback
targets.
