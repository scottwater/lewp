# Domains Requirements

## Goal

Build a macOS-first local domain router/proxy for parallel local development.

The tool grants stable loopback ports, assigns predictable `.lewp` hostnames,
and reverse-proxies browser traffic to developer-started processes. It should
make callback URLs, SSO redirects, cookies, and multi-worktree development
predictable without becoming a process manager.

Supporting technical notes, reference-project observations, and open
implementation choices live in [technical-appendix.md](technical-appendix.md).

## V1 Product Contract

V1 is a router/proxy only.

It does:

- allocate or reuse a stable local port for the current project instance
- assign a predictable local hostname
- register that hostname with the daemon
- route HTTP traffic to `127.0.0.1:<port>` or `[::1]:<port>`
- show a useful local error page if the target process is not running
- expose enough CLI/status output to debug what it inferred and registered

It does not:

- start Rails, Vite, Next, Puma, Foreman, Overmind, Docker, or any app process
- manage project lifecycle
- proxy arbitrary remote hosts
- expose services to LAN or public internet
- write `.env` files by default
- support multiple services per project instance in V1
- provide dashboard, sharing, QR codes, or tunnels in V1

## Primary Workflow

From a project instance directory:

```sh
cd ~/projects/audit/feature-1
lewp lease
```

Default output:

```sh
PORT=42137
URL=http://feature-1.audit.lewp
HOST=feature-1.audit.lewp
```

The same command both leases the port and registers the route. The user starts
their app with the returned port using their own framework tooling.

Example:

```sh
PORT=42137 bin/dev
```

The hostname should work as a normal browser URL:

```text
http://feature-1.audit.lewp
```

Nice hostnames are the point of the tool. A design that requires users to type a
proxy port in the browser, such as `http://feature-1.audit.lewp:49200`, does not
meet the product goal.

## Hostname Contract

Default hostname format:

```text
<instance>.<root>.lewp
```

Examples:

```text
~/projects/audit/feature-1
-> feature-1.audit.lewp

~/projects/audit/scott/JIRA-123 Add SSO callback!
-> jira-123-add-sso-callback.audit.lewp
```

`.lewp` is the default suffix for this tool. It is not a real public TLD, so it
should not collide with real domains. The suffix should be configurable later,
but V1 can assume `.lewp`.

Subdomain-per-instance is the typical workflow, but V1 must also support
explicit project apex hosts:

```text
<root>.lewp
```

Example:

```sh
lewp lease --host audit.lewp
```

This registers `audit.lewp` for the current folder. Custom hosts must stay
inside `.lewp` in V1; non-`.lewp` domains are out of scope.

## Name Discovery

Root, instance, and explicit host discovery order:

1. CLI flags: `--root`, `--name`, `--host`
2. environment variables: `LEWP_ROOT`, `LEWP_NAME`, `LEWP_HOST`
3. nearest `.lewp.local.toml`
4. path inference

Path inference rule:

- parent directory name becomes `root`
- current directory basename becomes `instance`

For branch-like names containing `/`, use only the segment after the last `/`
before URL normalization.

The CLI must make inference obvious. If it inferred `root` or `name`, default
human output should say so.

Example:

```text
PORT=42137
URL=http://feature-1.audit.lewp
HOST=feature-1.audit.lewp

# inferred root=audit from parent folder
# inferred name=feature-1 from current folder
```

## Name Normalization

Hostnames must be aggressively normalized into valid DNS labels.

Rules:

- lowercase
- use the basename after the last `/`
- replace unsafe characters with `-`
- collapse repeated dashes
- trim leading and trailing dashes
- enforce DNS label length limits
- avoid empty labels
- warn when normalized output differs from input

Example:

```text
scott/feature/JIRA-123 Add SSO callback!
-> jira-123-add-sso-callback
```

If normalization would produce an unusable name, `lewp lease` must fail with
a clear error and ask for `--name`.

## CLI Overrides

Users can override inferred values:

```sh
lewp lease --root audit --name sso-callback
lewp lease --host audit.lewp
```

Overrides become the remembered identity for that folder until explicitly
changed, released with forget semantics, or renamed by a future command.

Predictability matters more than stateless command behavior. If a user provides
control via the CLI, the tool should keep using that control for the same
folder.

## Conflict Behavior

If the desired hostname is already assigned to another active or remembered
identity, the tool should not silently steal it.

V1 behavior:

- allocate a deterministic short suffix
- warn clearly
- show the conflicting existing path
- include cleanup guidance

Example:

```text
warning: feature-1.audit.lewp is already assigned to:
  /Users/scott/projects/audit/feature-1

using: feature-1-a8f3.audit.lewp
```

Suffixes should be deterministic for the folder/identity so repeated leases do
not keep changing the hostname.

## Port Leasing

Default app port range:

```text
41000-49999
```

Reasons:

- avoids common framework defaults
- avoids common database/cache ports such as Postgres and Redis
- keeps app targets in a high unprivileged range

Port assignment requirements:

- stable for the same folder/identity when possible
- persisted in SQLite
- never silently steals a live assignment
- reuses the same port for a remembered identity when still free
- supports a configurable global port range later

Requested routed-app port/range flags are out of V1. The default range should be
reliable enough for first implementation. Bare internal port leases are covered
by `lewp port --name ...`.

## Lease Lifetime

Leases are stable until explicit release. Proxy traffic updates activity metadata
for display/debugging, but it does not drive automatic expiry or cleanup.

Requirements:

- HTTP requests through the proxy update `last_seen_at` on a throttled,
  best-effort basis
- V1 has no automatic lease expiry sweeper
- a later `lewp lease` from the same folder reuses the same host and port while
  the identity remains remembered
- `lewp release` releases the current folder route
- `lewp release --forget` removes remembered identity/history for that
  folder

The user should not need to keep running the CLI during normal development. They
run `lewp lease` when creating or returning to a branch/worktree, then use
the URL while building.

## Daemon Architecture

One Go binary with two roles:

```sh
lewp ...         # CLI client
lewp daemon      # launchd-managed daemon
```

The daemon owns:

- SQLite registry
- HTTP proxy on port 80
- HTTPS proxy on port 443 when enabled
- DNS server for `.lewp`
- local control API over a Unix socket

The CLI talks to the daemon over the local socket.

If the daemon is not running, CLI commands should fail with a direct fix instead
of silently starting background services.

Example:

```text
lewp daemon is not running
Run: lewp system start
```

## macOS Setup

V1 target platform: macOS.

Required setup command:

```sh
lewp setup
```

Setup responsibilities:

- configure macOS DNS for `.lewp`, likely via `/etc/resolver/lewp`
- install or prepare the launchd service
- verify that port 80 can be bound by the daemon setup
- prepare local HTTPS trust when HTTPS support is enabled
- provide clear cleanup/uninstall instructions

Service commands are `lewp system start|stop|status|restart|uninstall`.

`system uninstall` must remove launchd/resolver integration cleanly.

## DNS

V1 should run its own tiny DNS server for `.lewp` and integrate with macOS using
`/etc/resolver/lewp`.

Reasons:

- `/etc/hosts` cannot express wildcard local lewp cleanly
- Caddy alone does not solve DNS
- puma-dev and dot-test validate this integration shape

All matching `.lewp` names owned by the tool should resolve to loopback.

## Proxy Requirements

Proxy targets are loopback-only:

- `127.0.0.1:<port>`
- `[::1]:<port>`

V1 must not proxy to arbitrary LAN, WAN, or public hosts.

Proxy behavior:

- preserve original `Host` header by default
- pass `X-Forwarded-Host`
- pass `X-Forwarded-Proto`
- pass `X-Forwarded-For`
- support HTTP/1.1
- support WebSocket upgrades
- support streaming responses and SSE

Preserving the friendly host is important for Rails hosts config, SSO callbacks,
CORS, cookies, OAuth redirects, and similar local-development behavior.

## Error Page

If a hostname is registered but the target port is closed, the proxy should show
a debug-first HTML error page rather than a generic blank 502.

Include the requested host, loopback target, project path, root/name, last seen
time, release state, suggested start command, and hints for `lewp list` /
`lewp doctor`.

Example content:

```text
feature-1.audit.lewp is registered but not responding

Target: 127.0.0.1:42137
Project: /Users/scott/projects/audit/feature-1
Last seen: 2026-06-26 21:44
Try: PORT=42137 bin/dev
```

Including local filesystem paths is acceptable. Debuggability is more important
than hiding local-only details.

## Health and Status

Health should use TCP checks, not arbitrary HTTP requests.

Reasons:

- avoids changing app state
- avoids app-specific route assumptions
- fast enough for `lewp list`

States:

- `up`: TCP connection to target port succeeds
- `down`: TCP connection fails or is refused
- `stale`: path is missing or identity can no longer be resolved

Default `lewp list` should show active and recently inactive entries.
`lewp list --all` should include released/stale history.

Example:

```text
HOST                         PORT   STATE   PATH
feature-1.audit.lewp         42137  down    ~/projects/audit/feature-1
main.blog.lewp               42138  up      ~/projects/blog
```

## CLI Contract

Core commands:

```sh
lewp setup
lewp system start|stop|status|restart|uninstall
lewp lease [--root audit] [--name feature-1] [--host audit.lewp] [--json] [--shell]
lewp port [--name vite] [--json] [--shell]
lewp release [--forget]
lewp list [--all]
lewp doctor
```

`lewp lease` defaults to simple env-style lines:

```sh
PORT=42137
URL=http://feature-1.audit.lewp
HOST=feature-1.audit.lewp
```

`--shell` outputs eval-safe `export` lines. `--json` outputs machine-readable
fields for port, URL, host, root, name, normalized values, path, inference
metadata, warnings, and release state.

## Environment File Policy

V1 should not mutate `.env` by default.

The core contract is: print values; user scripts decide what to do. This keeps
the tool framework-agnostic and avoids committing project-specific env decisions
too early.

Future adapters may write `mise.local.toml`, `.env.local`, `.envrc`,
framework-specific files, or custom paths. Mise is a useful integration target,
not the core registry.

## Config File

Optional local config file:

```text
.lewp.local.toml
```

Example:

```toml
root = "audit"
name = "feature-1"
host = "audit.lewp" # optional full-host override
```

This file is intended to be local/uncommitted and easy to ignore. `lewp init`
may be added later to create it, but `lease` must work without it through flags,
env vars, or inference.

## Registry Storage

Use SQLite at:

```text
~/Library/Application Support/lewp/registry.sqlite
```

The registry must persist identities, leases, ports, paths, last-seen timestamps,
release state, and enough events to debug routing and conflict behavior. The
first schema can be small, but it should keep identity history separate from the
currently active lease so released projects can reclaim stable names and ports
later when remembered history remains.

## HTTPS Requirement

HTTP must work first, but HTTPS is close to core.

The project should fail hard trying to make local HTTPS work before pushing it
out of scope. Local HTTPS matters for auth callbacks, secure cookies, browser
API behavior, and realistic development.

Target behavior:

```text
https://feature-1.audit.lewp
```

Requirements:

- local CA generated or managed by the tool
- setup installs/trusts the CA on macOS, or gives exact manual steps
- certificates issued automatically for local `.lewp` hosts
- no per-project TLS config
- user-facing output clearly states whether HTTPS is enabled

If HTTPS misses the first release, command output and docs must be explicit. The
tool must not pretend HTTPS works.

## Doctor Command

`lewp doctor` should inspect common failure points: daemon state, control
socket, resolver file, `.lewp` lookup, proxy port binding, registry readability,
current-folder inference, target port state, conflicts, and HTTPS CA state when
enabled.

Doctor output should be concrete and command-oriented.

## Acceptance Criteria

V1 is acceptable when:

- `lewp lease` from `~/projects/audit/feature-1` returns stable `PORT`,
  `URL`, and `HOST`
- `feature-1.audit.lewp` resolves locally after setup
- `lewp lease --host audit.lewp` registers a stable project apex host
- `lewp port --name vite` returns a stable bare internal port without a hostname
- proxy routes to `127.0.0.1:<PORT>`
- closed ports show a useful debug page, not a blank 502
- WebSockets/HMR work through the proxy
- streaming/SSE responses work through the proxy
- original `Host` is preserved upstream
- `X-Forwarded-*` headers are passed
- `lewp list` shows `up`/`down`/`stale` state without HTTP app probes
- conflicting names get deterministic suffixes and warnings
- normalized names are valid DNS labels
- remembered leases are stable and reclaimed by the same folder
- `lewp release` frees current folder route
- `lewp release --forget` removes remembered identity
- `lewp system uninstall` removes launchd/resolver integration cleanly
- V1 cannot proxy remote targets
