# Lewp CLI Documentation

Lewp has one binary with two roles:

```sh
lewp <command>   # CLI client
lewp daemon      # launchd-managed daemon
```

Most commands talk to the daemon over
`~/Library/Application Support/lewp/control.sock`.

## Build and Install

```sh
bin/build
bin/install
bin/reinstall
```

`bin/install` installs to `~/.local/bin/lewp` by default. Set
`LEWP_INSTALL_DIR` to install somewhere else. `bin/reinstall` runs build then
install.

## Commands

```sh
lewp setup [--suffix S] [--allow-domain-mirror]
lewp system start|stop|status|restart|uninstall
lewp add [--root R] [--name N] [--host H] [--auto-suffix] [--json|--shell]
lewp alias add <host> [--json]
lewp alias remove <host>
lewp alias list [--json]
lewp init [--root R] [--name N] [--host H] [--force]
lewp info [--json]
lewp move --from <path> [--json]
lewp port [--name N] [--json|--shell]
lewp port release [--name N] [--forget]
lewp release [--all] [--forget]
lewp list [--all] [--json]
lewp suffix list
lewp suffix remove S
lewp doctor
lewp logs [--lines N] [--grep TEXT] [--follow] [--path]
lewp completion bash|zsh|fish
lewp version
lewp daemon
```

Every command accepts `--help` (alias `-h`) for command-specific usage, flags,
and examples. `lewp`, `lewp help`, and `lewp --help` print the top-level help.
An unknown command prints the top-level help to stderr and exits `2`.

## `lewp setup`

Creates Lewp local TLS CA material and trusts it in the macOS login keychain.
Installing `/etc/resolver/lewp` uses `sudo` when the command is not already
running as root, so macOS may prompt for your password. Trusting the local CA
may also pop a macOS keychain dialog. `setup` prints both possibilities as
leading `#` notes before doing the work.

When a step fails, `setup` prints the exact failing command (from the wrapped
error) plus a `Next:` line describing how to recover — for example the precise
`security add-trusted-cert ...` command to run by hand if keychain trust fails.

`.lewp` is built in and is always installed. `--suffix S` additively installs a
resolver for an owned public dev suffix, useful when an OAuth provider rejects
private TLDs. Custom public suffix and domain-mirror routing is HTTP-only in V1;
Lewp's local TLS certificate issuance is limited to `.lewp` hosts.

By default, custom suffixes use safe-subtree mode:

```sh
lewp setup --suffix local.todoordie.com
```

This installs a resolver only for the configured subtree. With
`local.todoordie.com`, public DNS for `todoordie.com` and `www.todoordie.com`
continues normally.

Domain mirror mode must be opted into:

```sh
lewp setup --suffix localkickofflabs.com --allow-domain-mirror
```

It allows an owned registrable domain as the suffix and shadows public DNS for
that suffix locally until you run `lewp suffix remove localkickofflabs.com` or
`lewp system uninstall`. Proxy routing remains host-based: register a primary
route with `lewp add --host ...`, then attach same-app hostnames with
`lewp alias add ...`.

Safe-subtree suffixes must be below a registrable domain. `local.todoordie.com`
and `local.todoordie.co.uk` are accepted; apex domains such as `todoordie.com`
and `todoordie.co.uk`, `www.*`, and reserved suffixes are rejected unless domain
mirror mode is explicitly allowed. Setup is additive: re-running with another
`--suffix` keeps previously configured suffixes. After adding a suffix, run
`lewp system start` (or pass `--start` to setup) so launchd starts or kickstarts
the daemon and it loads the updated suffix list.

Output:

```sh
DNS=resolver-file
HTTPS=enabled
LAUNCHD=/Users/scott/Library/LaunchAgents/dev.lewp.daemon.plist
RESOLVER=/etc/resolver/lewp
SUFFIX=local.todoordie.com RESOLVER=/etc/resolver/local.todoordie.com
CA=/Users/scott/Library/Application Support/lewp/ca.pem
LOGS=/Users/scott/Library/Logs/lewp
security add-trusted-cert -r trustRoot -p ssl -k login.keychain ...
```

Current setup state:

- CA cert: `~/Library/Application Support/lewp/ca.pem`
- CA key: `~/Library/Application Support/lewp/ca-key.pem`
- LaunchAgent plist: `~/Library/LaunchAgents/dev.lewp.daemon.plist`
- Daemon logs: `~/Library/Logs/lewp/daemon.out.log` and
  `~/Library/Logs/lewp/daemon.err.log`
- resolver file: `/etc/resolver/lewp`
- custom suffix config: `~/Library/Application Support/lewp/suffixes.toml`
- Registry: `~/Library/Application Support/lewp/registry.sqlite`
- Control socket: `~/Library/Application Support/lewp/control.sock`
- DNS responder default port: `15353`

## `lewp system`

Manage the macOS LaunchAgent commands.

```sh
lewp system start
lewp system stop
lewp system restart
lewp system status
lewp system uninstall
```

`start`, `stop`, `restart`, and `uninstall` run the matching launchctl command
and print it. If `start` sees launchd bootstrap status 5 because the job is
already loaded, it falls back to `launchctl kickstart -k`. `uninstall` also
removes the Lewp CA trust from the login keychain, LaunchAgent plist, and
resolver files, including custom suffix resolver files configured with
`lewp setup --suffix`.

Because `uninstall` is destructive, it first prints an affected-file summary —
the launchd job to boot out, the keychain trust to remove, the resolver file to
remove (which may prompt for sudo), and the CA material it keeps — before doing
any of the work, so the full scope is visible up front:

```text
uninstall will affect:
  launchd: bootout dev.lewp.daemon and remove ~/Library/LaunchAgents/dev.lewp.daemon.plist
  keychain: remove trust for "Lewp Local Development CA"
  resolver: remove /etc/resolver/lewp (may prompt for sudo)
  resolver: remove /etc/resolver/local.todoordie.com (custom suffix local.todoordie.com; may prompt for sudo)
  suffix config: remove ~/Library/Application Support/lewp/suffixes.toml
  kept: ~/Library/Application Support/lewp/ca.pem (CA material; a later lewp setup reuses it)
```

When `start` (or the kickstart fallback) fails for any other reason, it prints
the exact launchctl command that failed, the underlying error, where to find the
daemon's captured startup errors (`~/Library/Logs/lewp/daemon.err.log`, also via
`lewp logs --lines 50`), and — when `launchctl print` is available — the
service's current launchd state. The daemon's plist always declares the four
socket-activation entries `HTTP`, `HTTP6`, `HTTPS`, and `HTTPS6` (loopback ports
80/443 over IPv4 and IPv6), which the daemon claims back on startup.

`status` checks the daemon control socket and prints:

```text
lewp daemon is running
```

## `lewp suffix`

List and remove custom managed suffixes.

```sh
lewp suffix list
lewp suffix remove local.todoordie.com
```

`.lewp` is built in and cannot be removed. Custom suffixes are added with
`lewp setup --suffix S`; `suffix remove S` removes the suffix from Lewp config
and deletes its resolver file after confirming the resolver is Lewp-owned.
`suffix list` includes each suffix mode:

```text
SUFFIX                 MODE
lewp                   built-in
local.todoordie.com    safe-subtree
localkickofflabs.com   domain-mirror
```

Example:

```sh
lewp setup --suffix local.todoordie.com
lewp suffix list
lewp suffix remove local.todoordie.com
```

## `lewp add`

Register a routed app port and a Lewp-managed host for the current directory.
This is the command for creating a route.

```sh
lewp add
lewp add --root atlas --name feature-1
lewp add --host atlas.lewp
lewp add --json
lewp add --shell
```

Default (human) output:

```sh
PORT=42137
URL=http://feature-1.atlas.lewp
HTTPS_URL=https://feature-1.atlas.lewp
HOST=feature-1.atlas.lewp
STATE=new
HOST_KIND=instance
```

`URL` is always `http://<host>` and `HTTPS_URL` is always `https://<host>`; both
are emitted whenever the lease has a routable host. For `.lewp` hosts, the
`HTTPS_URL` line is usable after `lewp setup` trusts the local CA. For configured
custom public suffixes and domain mirrors, routing is HTTP-only in V1 unless TLS
support is expanded; see [Browser URLs](#browser-urls).
`STATE` is `new`, `reused`, or `conflict-renamed`, and `HOST_KIND` is
`instance`, `apex`, or `custom`. Because `.lewp` names resolve only to loopback,
the human output also prints a `# <host> is local-only (resolves to 127.0.0.1)`
note on stderr.

`--shell` prefixes the assignable values with `export` and omits the descriptive
`STATE`/`HOST_KIND` lines and the stderr note, so `eval "$(lewp add --shell)"`
sets only the variables you want and nothing else:

```sh
export PORT=42137
export URL=http://feature-1.atlas.lewp
export HTTPS_URL=https://feature-1.atlas.lewp
export HOST=feature-1.atlas.lewp
```

`--json` prints machine-readable fields including `url`, `https_url`,
root/name/host metadata, path, kind, host_kind, source fields, warnings, lease
state, and release state.

Host rules:

- default host: `<instance>.<root>.lewp`
- project apex override: `lewp add --host atlas.lewp`
- custom `.lewp` override: `lewp add --host sso.atlas.lewp`
- configured public suffix override:
  `lewp add --host feature-1.local.todoordie.com`
- hosts outside `.lewp` or the configured suffix list are rejected

Discovery order (each of `root`, `name`, and `host` is resolved from the first
source that provides it):

1. flags: `--root`, `--name`, `--host`
2. environment: `LEWP_ROOT`, `LEWP_NAME`, `LEWP_HOST`
3. nearest `.lewp.local.toml` up the directory tree
4. path/git worktree inference (parent dir → root, current dir → name)

The `LEWP_*` variables are read from the CLI process and forwarded to the daemon
over the control socket; the daemon never honors its own environment. Inferred
values are reported as `# inferred root=...` / `# inferred name=...` notes on
stderr so `--shell` and `$(...)` capture stay clean.

Example `.lewp.local.toml` (write one with [`lewp init`](#lewp-init)):

```toml
root = "atlas"
name = "feature-1"
host = "atlas.lewp"
```

By default an inferred host that collides with another folder's is given a
deterministic suffix (the original owner is kept). An explicit `--host` that
collides fails with the conflicting path and cleanup guidance; pass
`--auto-suffix` to take a deterministic suffix instead of failing.

## `lewp alias`

Attach extra hostnames to the current directory's active route. Aliases reuse
the route's existing port and process; they do not allocate another port, create
another primary route, or start anything.

```sh
lewp alias add tags.feature-1.atlas.lewp
lewp alias add '*.feature-1.atlas.lewp'
lewp alias list
lewp alias remove tags.feature-1.atlas.lewp
```

Use aliases when one app process serves multiple hostnames, such as Rails
subdomain routing (`tags.app.lewp`, `leads.app.lewp`) or a local domain mirror
where several public-looking URLs should point to the same dev server.

`alias add` requires an active route in the current directory. Run `lewp add`
first. The host must be under `.lewp` or a suffix configured by
[`lewp setup --suffix`](#lewp-setup); public apex/domain-mirror hosts still
require the same setup opt-in rules as `lewp add --host`.

Wildcards are supported as host patterns:

```sh
lewp alias add '*.app.lewp'
```

A wildcard matches exactly one label. `*.app.lewp` matches `tags.app.lewp`, but
not `app.lewp` and not `api.tags.app.lewp`. Lewp rejects aliases and wildcards
that conflict with an existing active host and reports the conflicting path.
Adding an exact alias already covered by a wildcard on the same route succeeds
with a warning.

Default `alias add` output mirrors `lewp add` for the attached host:

```sh
PORT=42137
URL=http://tags.feature-1.atlas.lewp
HTTPS_URL=https://tags.feature-1.atlas.lewp
HOST=tags.feature-1.atlas.lewp
STATE=new
HOST_KIND=alias
```

`alias list` prints env-style blocks for aliases on the current route. `--json`
is available for `add` and `list`; list JSON is always an array.

## `lewp init`

Generate a `.lewp.local.toml` for the current directory so its identity is
explicit and stable regardless of how the folder is laid out. `init` only writes
the file — it never contacts the daemon, allocates a port, or leases a route.

```sh
lewp init
lewp init --root atlas --name feature-1
lewp init --host atlas.lewp
lewp init --force
```

Values come from flags first, then path inference (the same inference `add`
uses). Flags:

- `--root R` — root segment to record (defaults to the inferred root)
- `--name N` — instance segment to record (defaults to the inferred name)
- `--host H` — record an explicit full `.lewp` host (e.g. `atlas.lewp`)
- `--force` — overwrite an existing file; the replacement is rebuilt from flags
  and inference only, never from the file being replaced

The file is meant to stay uncommitted. `init` prints the `.git/info/exclude`
line that keeps it out of version control. Without `--force`, `init` refuses to
clobber an existing `.lewp.local.toml` (even a malformed one) with an
`already exists` message.

## `lewp info`

Show routes and bare ports registered for the current directory. `info` reads
existing registry data only — it never infers, allocates, or mutates a route or
port.

```sh
lewp info
lewp info --json
```

Default output for a directory with one route, one alias, and one bare port:

```sh
ROUTE
PORT=42137
URL=http://feature-1.atlas.lewp
HTTPS_URL=https://feature-1.atlas.lewp
HOST=feature-1.atlas.lewp
STATE=up
DIR=/Users/scott/projects/atlas/feature-1

ALIASES
PORT=42137
URL=http://tags.feature-1.atlas.lewp
HTTPS_URL=https://tags.feature-1.atlas.lewp
HOST=tags.feature-1.atlas.lewp
STATE=up
DIR=/Users/scott/projects/atlas/feature-1

PORTS
NAME=vite
PORT=42138
STATE=down
DIR=/Users/scott/projects/atlas/feature-1
```

The project directory is reported as `DIR=` (not `PATH=`) so the env-style
output never shadows the shell's `$PATH`. A routed host also prints a
`# <host> is local-only (resolves to 127.0.0.1)` note on stderr.

If no route or port is registered for the current directory, `info` exits
non-zero and points you at the commands that create or relocate one:

```text
no Lewp route or port is registered for this directory
Run: lewp add
Or lease a bare port: lewp port --name <name>
Or move an existing route here: lewp move --from <path>
```

## `lewp move`

Move an existing route from another directory to the current directory, keeping
the same primary host, aliases, wildcards, and port. Use this after relocating
or renaming a project folder so its stable URLs follow it.

```sh
lewp move --from ~/projects/atlas/old-feature
lewp move --from ~/projects/atlas/old-feature --json
```

`--from` is required and names the directory that currently owns the route. The
move is atomic: the source directory is left with no route (`lewp info` there
reports none) and the current directory becomes the owner with the same port,
primary host, aliases, and wildcards. The port is never reallocated. The moved
route hosts are printed in env-style blocks (`PORT`, `URL`, `HTTPS_URL`, `HOST`,
`DIR`).

If the source directory has no active route, or the current directory already
owns an active route, `move` fails with a clear error and changes nothing.

## `lewp port`

Lease a stable bare port with no hostname or proxy route.

```sh
lewp port
lewp port --name vite
lewp port --name vite --shell
```

`port` prints the same env-style output as `add` minus the host fields: the
default (human) form adds a `STATE=` line, and `--shell` emits a single
`export PORT=<n>` line. Because `--shell` emits one `export` line per value,
evaluate it rather than capturing it into a variable:

```sh
eval "$(lewp port --name vite --shell)"
```

For just the number, prefer `--json` with `jq` — for internal services that need
unique local ports across worktrees, such as Vite:

```sh
export VITE_RUBY_PORT="$(lewp port --name vite --json | jq -r .port)"
```

With no `--name`, `port` leases under the default name `port`, so repeated
`lewp port` calls from the same directory return the same number.

### `lewp port release`

Release a single bare port lease for the current directory.

```sh
lewp port release             # release the default-named ("port") lease
lewp port release --name vite # release the bare port named "vite"
lewp port release --name vite --forget
```

`--name` selects which bare port to release (default `port`); `--forget` also
drops its remembered identity. Releasing a name with no active port is reported
(`no active port named "vite" for this directory`) and exits `0` — release is
idempotent. This frees one named port; to drop the route and every bare port at
once use [`lewp release --all`](#lewp-release).

## `lewp release`

Release the current folder route, and optionally its bare ports.

```sh
lewp release
lewp release --all
lewp release --forget
```

By default `release` frees only the route, including its aliases and wildcard
hosts. `--all` additionally releases every bare port leased for this directory
(equivalent to running `lewp port release` for each one). Without `--forget`,
history stays in the registry; with
`--forget`, Lewp removes the remembered identity/history for the current folder.

Release is idempotent: when nothing is active it reports `no active route for
this directory` (or, with `--all`, `no active route or port for this directory`)
and exits `0`. To release a single named bare port instead of all of them, use
[`lewp port release --name <name>`](#lewp-port-release).

## `lewp list`

List registry entries (routes, aliases, and bare ports).

```sh
lewp list
lewp list --all
lewp list --json
```

Human columns:

```text
HOST              NAME       KIND   PORT   STATE  PATH
feature-1.atlas.lewp  feature-1  route  42137  up     /Users/scott/projects/atlas/feature-1
tags.feature-1.atlas.lewp  feature-1  alias  42137  up     /Users/scott/projects/atlas/feature-1
-                 vite       port   42138  down   /Users/scott/projects/atlas/feature-1
```

`KIND` is `route`, `alias`, or `port`; a bare port has no host, so `HOST` shows
`-`. Alias rows share the route's port and path.

States:

- `up`: TCP connect to the leased port succeeds
- `down`: TCP connect fails
- `stale`: registered path no longer exists
- `released`: route was explicitly released

Default list hides released routes and their aliases. `--all` includes registry
history.

`--json` emits a JSON array of entries (always an array, never `null`), each
with `host`, `port`, `state`, `path`, `kind`, `root`, and `name`:

```sh
lewp list --json | jq '.[] | select(.kind == "alias")'
```

## `lewp doctor`

Print daemon/control/HTTPS checks.

Example:

```text
daemon: ok
control socket: ok
https: configured (local CA present)
keychain: trusted
cli binary: /usr/local/bin/lewp
launchd plist: /Users/scott/Library/LaunchAgents/dev.lewp.daemon.plist
installed program: /usr/local/bin/lewp
installed program matches this CLI
current version: 0.1.0
installed version: 0.1.0
daemon log: /Users/scott/Library/Logs/lewp/daemon.err.log
```

The current implementation checks daemon reachability, control socket
reachability, local CA material, and keychain trust. It then compares the binary
and config launchd is set up to run against the CLI you are invoking now, so you
can tell whether `bin/install` / `bin/reinstall` updated what launchd launches.
It reads the program path out of the installed launchd plist, confirms that
program exists on disk, and runs `<installed> version` to compare the installed
daemon's build against the current CLI. When they differ it prints a `mismatch:`
or `version mismatch:` line pointing at `bin/reinstall` and `lewp system
restart`. The installed-binary checks print even when the daemon is not
responding (the common symptom of running the wrong binary), and `doctor` exits
non-zero in that case.

The V1 doctor contract also tracks resolver file state, `.lewp` lookup, proxy
port binding, registry readability, current-folder identity inference, current
target port state, and hostname conflicts.

`doctor` also prints an informational `browser trust` line restating the V1
HTTPS browser boundary: Safari and Chromium browsers trust the macOS keychain,
while Firefox uses its own NSS store and is not supported in V1 (use `http://`
in Firefox). It is informational only and never fails. See
[Browser URLs](#browser-urls).

If setup has not created local CA material:

```text
https: not configured (run lewp setup)
keychain: not trusted (run lewp setup)
```

If launchd runs a different binary than the current CLI:

```text
mismatch: launchd runs /old/bin/lewp but this CLI is /usr/local/bin/lewp — run bin/reinstall and lewp system restart
```

## `lewp logs`

Show or tail the launchd-managed daemon's logs. launchd writes the daemon's
stdout and stderr to `daemon.out.log` and `daemon.err.log` under
`~/Library/Logs/lewp` (set as `StandardOutPath` / `StandardErrorPath` in the
plist). The daemon's stdout carries one request line per proxied request
(host, method, scheme, upstream target, status, and any proxy error); startup
failures land on stderr.

```sh
lewp logs                  # last 200 lines of each log, with a header per file
lewp logs --lines 500      # last 500 lines of each log
lewp logs --grep error     # only lines containing "error" (case-insensitive)
lewp logs --grep 502 --lines 50  # last 50 matching lines
lewp logs --follow         # stream new output until Ctrl-C (alias: -f)
lewp logs --path           # print the two log file paths and exit
```

`--grep TEXT` keeps only lines containing `TEXT` (case-insensitive); `--lines`
then bounds the matching lines, so `--grep TEXT --lines N` behaves like
`grep TEXT | tail -n N`. When a log has no matching lines, `logs` prints
`(no lines matching "TEXT")` for that file.

`lewp logs` only reads files; it never starts the daemon. When no logs exist
yet, it prints where they will appear after `lewp setup && lewp system start`.

## `lewp completion`

Print a static shell completion script to stdout for `bash`, `zsh`, or `fish`.
The script is self-contained and never contacts the daemon; it completes the
top-level commands plus the `system` actions and `completion`/`port` shells and
subcommands.

```sh
lewp completion bash > /usr/local/etc/bash_completion.d/lewp
lewp completion zsh  > "${fpath[1]}/_lewp"   # then restart zsh
lewp completion fish > ~/.config/fish/completions/lewp.fish
```

A missing or unsupported shell argument exits `2` with guidance.

## `lewp version`

Print the public version. Add `--detailed` for development/build metadata.

```sh
lewp version
lewp version --detailed
```

Example:

```text
lewp version 0.1.0
```

Detailed example:

```text
lewp version 0.1.0
commit:  0a778ce
built:   2026-06-27T23:08:00Z
go:      go1.25.1
```

`version`, `--version`, and `-v` are equivalent. The version is injected at link
time by `bin/build`; an un-stamped `go build` reports `dev`. The detailed
commit/build-time fields are also injected at link time for development builds.

## `lewp daemon`

Daemon mode. Normally launchd starts this command.

The daemon owns:

- SQLite registry
- Unix control socket
- HTTP proxy listeners from launchd
- HTTPS proxy listeners from launchd
- SNI certificate minting from the persisted local CA

The daemon logs one line per proxied request to stdout (captured in
`daemon.out.log`); inspect it with `lewp logs`. Startup errors are written to
stderr (`daemon.err.log`).

## `.lewp` error pages

The proxy serves debuggable HTML rather than blank gateway errors for the two
common failure modes. Both pages share a small, framework-neutral inline
stylesheet (no external assets, no web fonts, no JavaScript) that renders in
light and dark mode, and present the suggested command in a copyable block:

- **Registered but not responding** — when a host is leased but its target port
  is closed, the proxy returns `502` with the host, loopback target, project
  path, root/name, last-seen time (or `never`), release state, a copyable
  framework-neutral `PORT=<n> <your dev command>` start command, and
  `lewp list` / `lewp doctor` hints.
- **Unregistered managed Lewp host/suffix** — when a host under `.lewp` or a
  configured custom suffix has no route, the proxy returns `404` with parsed
  labels when available and next steps (`lewp add`, `lewp list`, `lewp doctor`).
  Unmanaged hosts get a plain `404`. The host is HTML-escaped so a crafted
  hostname cannot inject markup.

## Browser URLs

For routed leases, open:

```text
http://<host>
https://<host>   # .lewp hosts only
```

DNS resolves all `.lewp` names and configured custom suffixes to loopback. The
proxy routes by full registered host. Different primary routes can point to
different local ports, while route aliases let multiple hostnames point to one
route and port. Wildcard aliases match one label only.

HTTPS uses Lewp's local CA, created and trusted by `lewp setup`. Browser support
in V1 is `.lewp`-only: Lewp mints certificates only for `.lewp` SNI names.
Configured custom public suffixes and domain mirrors route over HTTP unless TLS
support is expanded.

- **Safari and Chromium browsers** (Chrome, Brave, Arc, Edge, Helium) trust the
  macOS system keychain, so `https://<host>` works as soon as `lewp setup` has
  run.
- **Firefox is not supported in V1.** It uses its own NSS trust store rather than
  the macOS keychain, so it will warn on `.lewp` HTTPS. Use `http://<host>` in
  Firefox, or switch to Safari/Chromium for HTTPS. NSS (`certutil`) trust is a
  planned vNext addition.

## Troubleshooting

Daemon not running:

```text
lewp daemon is not running
Run: lewp system start
```

Target app not running:

- `lewp list` shows the route as `down`
- browser shows a Lewp debug page with host, target, path, and hints
- start your app manually with the leased `PORT`

HTTPS warnings:

- run `lewp setup`
- restart the browser if it cached trust state
- run `lewp system uninstall` to stop launchd and remove Lewp CA trust,
  LaunchAgent plist, and resolver file

Port collisions:

- Lewp never steals another remembered live assignment
- conflicting primary hosts receive deterministic suffixes
- conflicting aliases and wildcards are rejected with the conflicting path
- use `lewp release --forget` from old folders to remove stale ownership
