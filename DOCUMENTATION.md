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
curl -fsSL https://raw.githubusercontent.com/scottwater/lewp/main/install.sh | bash
go install github.com/scottwater/lewp/cmd/lewp@latest
bin/build
bin/install
bin/reinstall
```

`install.sh` downloads the latest GitHub release archive for the current Mac
architecture and installs `lewp` into `~/.local/bin` by default. Set
`LEWP_INSTALL_DIR` to install somewhere else. `go install` builds from source.
`bin/install` installs a local checkout build; `bin/reinstall` runs build then
install.

## Commands

```sh
lewp setup [--suffix S] [--allow-domain-mirror] [--log-requests errors|all]
lewp system start|stop|status|restart
lewp system uninstall [--yes]
lewp lease [--root R] [--name N] [--host H] [--auto-suffix] [--reset] [--json|--shell]
lewp alias add <host> [--json]
lewp alias remove <host>
lewp alias list [--json]
lewp init [--root R] [--name N] [--host H] [--force]
lewp info [--name N] [--host H] [--json|--shell|--port]
lewp move --from <path> [--json]
lewp port [--name N] [--json|--shell]
lewp release [--path P [--recursive] [--route | --name N] | --host H | --port N]
             [--forget] [--dry-run] [--json] [--yes|-y]
lewp list [--all] [--json]
lewp suffix list
lewp suffix remove S
lewp doctor
lewp logs [--lines N] [--grep TEXT] [--follow] [--path]
lewp completion bash|zsh|fish
lewp version
lewp upgrade
lewp daemon
```

Every command accepts `--help` (alias `-h`) for command-specific usage, flags,
and examples. `lewp`, `lewp help`, and `lewp --help` print the top-level help.
An unknown command prints the top-level help to stderr and exits `2`.

## Glossary

- **Route**: A hostname-backed registration that sends HTTP and HTTPS traffic
  to one stable loopback port for a registered path. Its primary host, aliases,
  and wildcard hosts belong to the same route.
- **Bare port**: A named, stable loopback port allocation for a registered path.
  It has no hostname and no proxy route.
- **Release**: Make a route or bare port inactive and return its active port to
  Lewp's allocation pool. Release keeps registry history. It does not stop a
  process or change anything in the project directory.
- **Forget**: Delete the selected registry history and remembered identity. If
  the selected route or bare port is active, Lewp releases it in the same
  operation. A path selector can forget released history, including history for
  a path that no longer exists. Forget does not delete project files.

Top-level help:

```text
lewp — local domain router and port leaser for parallel development

Lewp leases a stable loopback port and a predictable <instance>.<root>.lewp
hostname for the current directory, then reverse-proxies browser traffic to a
process you start yourself. It routes and leases only; it never starts apps.

Usage:
  lewp <command> [flags]

Commands:
  setup      Install the .lewp DNS resolver, local CA, and launchd service
  system     Manage the daemon: start|stop|status|restart|uninstall
  lease      Lease a stable port and managed hostname for the current directory
  alias      Manage extra hostnames for the current route
  init       Generate a .lewp.local.toml identity file for the current directory
  info       Show routes and bare ports registered for the current directory
  move       Move a route from another directory to the current directory
  port       Lease a bare internal port without a hostname
  release    Release routes and bare ports by path, host, or port
  list       List active routes and their health
  suffix     List or remove custom managed suffixes
  doctor     Diagnose daemon state, DNS, and CA trust
  logs       Show or tail the daemon logs
  completion Print a shell completion script (bash|zsh|fish)
  version    Print version
  upgrade    Replace this binary with the latest GitHub release
  daemon     Run the daemon in the foreground (normally launchd-managed)

Examples:
  lewp setup && lewp system start
  cd ~/projects/atlas/feature-1 && lewp lease
  eval "$(lewp lease --shell)" && PORT=$PORT bin/dev

Configuration (highest priority first):
  flags             --root, --name, --host on lewp lease
  environment       LEWP_ROOT, LEWP_NAME, LEWP_HOST
  .lewp.local.toml  nearest file up the tree; keys: root, name, host
  inference         parent dir -> root, current dir -> name

The .lewp.local.toml file is meant to stay local/uncommitted. Run "lewp init"
to generate one and to print a .git/info/exclude line that keeps it out of git.

Run "lewp <command> --help" for command-specific help.
```

## `lewp setup`

Help:

```text
lewp setup — one-time macOS setup for .lewp routing and .lewp HTTPS

Setup may prompt for your password (sudo) to install /etc/resolver/lewp, and
macOS may prompt you to trust the local development CA in your keychain.

Usage:
  lewp setup [--suffix <suffix>] [--allow-domain-mirror] [--start]
             [--log-requests errors|all]

It writes /etc/resolver/lewp (via sudo when needed), creates the local CA under
~/Library/Application Support/lewp/, installs the launchd plist, and trusts the
CA with the macOS "security" tool. Run "lewp system start" afterward.

--log-requests controls how much request traffic the daemon logs (persisted in
the launchd plist). The default "errors" logs only failed requests (a proxy
error or an HTTP status >= 400), so successful HMR/SSE/websocket traffic cannot
grow daemon.out.log without bound. Use "all" to log every proxied request.

Custom public dev suffixes default to safe-subtree mode:
  lewp setup --suffix local.todoordie.com

This installs a resolver only for the subtree, so todoordie.com and
www.todoordie.com continue to use public DNS.

Domain mirror mode is explicit because it shadows public DNS locally while the
resolver exists:
  lewp setup --suffix localkickofflabs.com --allow-domain-mirror
```

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
private TLDs.

Lewp's local TLS certificate issuance covers `.lewp` hosts and configured
safe-subtree custom suffixes; domain-mirror suffixes are HTTP-only. The CA is
name-constrained to exactly that set and `lewp setup` rotates it (untrust,
regenerate, re-trust — one keychain prompt) whenever the set changes. `lewp
doctor` fails on a legacy unconstrained CA and warns when constraints drift
from the suffix config.

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
route with `lewp lease --host ...`, then attach same-app hostnames with
`lewp alias add ...`.

Safe-subtree suffixes must be below a registrable domain. `local.todoordie.com`
and `local.todoordie.co.uk` are accepted; apex domains such as `todoordie.com`
and `todoordie.co.uk`, `www.*`, and reserved suffixes are rejected unless domain
mirror mode is explicitly allowed. Setup is additive: re-running with another
`--suffix` keeps previously configured suffixes. After adding a suffix, run
`lewp system start` (or pass `--start` to setup) so launchd starts or kickstarts
the daemon and it loads the updated suffix list.

`--log-requests errors|all` sets how much request traffic the daemon logs
(persisted in the launchd plist). The default, `errors`, logs only failed
requests (a proxy error or an HTTP status ≥ 400) so a steady stream of
successful HMR/SSE/websocket traffic cannot grow `daemon.out.log` without bound.
`--log-requests=all` logs one line per proxied request; re-running `lewp setup`
without the flag returns to the errors-only default. Restart the daemon (`lewp
system restart`, or `--start`) for a change to take effect.

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

Help:

```text
lewp system — manage the launchd-managed daemon

Usage:
  lewp system <action>

Actions:
  start      Bootstrap the daemon (falls back to kickstart if already loaded)
  stop       Bootout the daemon
  restart    Kickstart the daemon
  status     Report whether the daemon is responding
  uninstall  Remove the launchd, resolver, and keychain-trust integration

Flags:
  --yes, -y  Skip the confirmation prompt for uninstall (required to uninstall
             non-interactively, e.g. in a script)

Examples:
  lewp system start
  lewp system status
  lewp system uninstall --yes
```

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

Because it is destructive, `uninstall` then asks for confirmation. In an
interactive terminal it prompts `Proceed with uninstall? [y/N]` and only removes
anything on an explicit `y`/`yes`. Run non-interactively (piped, redirected, or
from a script), it refuses unless you pass `--yes` (or `-y`), so an unattended
`lewp system uninstall` can never silently tear down the integration:

```sh
lewp system uninstall --yes
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

Help:

```text
lewp suffix — list or remove custom managed suffixes

Usage:
  lewp suffix list
  lewp suffix remove <suffix>

Commands:
  list             List built-in and custom managed suffixes
  remove <suffix>  Remove a custom managed suffix and its resolver file

.lewp is built in and cannot be removed.
```

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

## `lewp lease`

Help:

```text
lewp lease — lease a stable port and host for this directory

Re-running from the same directory returns the same port and host. By default,
the host is under .lewp. Root and name are inferred from the directory layout
unless overridden.

Identity is discovered in this order: CLI flags, then LEWP_ROOT / LEWP_NAME /
LEWP_HOST environment variables, then the nearest .lewp.local.toml, then path
inference. Inferred values and warnings are printed to stderr so --shell and
$(...) capture only the clean env lines.

Usage:
  lewp lease [--root <root>] [--name <name>] [--host <host>] [--auto-suffix] [--reset] [--json] [--shell]

Flags:
  --root <root>   Override the inferred root segment of the hostname
  --name <name>   Override the inferred instance segment of the hostname
  --host <host>   Register an explicit .lewp or managed custom-suffix host
  --auto-suffix   On an explicit --host conflict, append a deterministic suffix
                  instead of failing
  --reset         Discard the remembered host/root/name override for this
                  directory and re-resolve from flags and inference, keeping the
                  same port and history
  --json          Emit the route as a JSON object
  --shell         Emit shell "export" lines for use with eval

An explicit --host that is already assigned to another directory fails by
default with the conflicting path and cleanup guidance. You can release and
forget the old route without changing directories. For an owner path with
spaces, Lewp prints a quoted command such as:

```sh
lewp release --path "/work/old app" --route --forget
```

An inferred host that conflicts receives a stable deterministic suffix.

Once you pass --root, --name, or --host, that value is remembered and reused by
later plain "lewp lease" calls. Use --reset to clear a bad override without
throwing away the port or history the way "lewp release --forget" would. Combine
it with a flag to keep one value while clearing the rest, e.g.
"lewp lease --reset --root atlas" re-infers the name and host but keeps root.

Examples:
  lewp lease
  lewp lease --root atlas --name feature-1
  lewp lease --reset
  eval "$(lewp lease --shell)"
```

Lease a routed app port and a Lewp-managed host for the current directory.
This is the command for creating a route.

```sh
lewp lease
lewp lease --root atlas --name feature-1
lewp lease --host atlas.lewp
lewp lease --json
lewp lease --shell
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
are emitted whenever the lease has a routable host. The `HTTPS_URL` line is
usable for `.lewp` hosts and configured safe-subtree custom suffix hosts once
`lewp setup` has trusted the local CA for that suffix. Domain-mirror hosts stay
HTTP-only; see [Browser URLs](#browser-urls).
`STATE` is `new`, `reused`, or `conflict-renamed`, and `HOST_KIND` is
`instance`, `apex`, or `custom`. Because `.lewp` names resolve only to loopback,
the human output also prints a `# <host> is local-only (resolves to 127.0.0.1)`
note on stderr.

`--shell` prefixes the assignable values with `export` and omits the descriptive
`STATE`/`HOST_KIND` lines and the stderr note, so `eval "$(lewp lease --shell)"`
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
- project apex override: `lewp lease --host atlas.lewp`
- custom `.lewp` override: `lewp lease --host sso.atlas.lewp`
- configured public suffix override:
  `lewp lease --host feature-1.local.todoordie.com`
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

### Clearing a remembered override with `--reset`

Once you pass `--root`, `--name`, or `--host`, Lewp remembers that value for the
directory and reuses it on later plain `lewp lease` calls. To undo a bad override
without losing the folder's port or history, pass `--reset`:

```sh
lewp lease --reset
```

`--reset` ignores the remembered identity and re-resolves from flags, env,
config, and inference, then persists the fresh result in place. The active port
and event history are kept — unlike [`lewp release --forget`](#lewp-release),
which deletes the route and every bare port for the directory entirely. When a reset actually changes a remembered
value, a `# reset remembered identity: host old -> new` note is printed on
stderr. Combine `--reset` with a flag to keep one value while clearing the rest:

```sh
lewp lease --reset --root atlas   # re-infer name and host, but keep root=atlas
```

Only the current directory's primary host and root/name are reset; its aliases
and wildcard hosts are left untouched.

## `lewp alias`

Help:

```text
lewp alias — manage extra hostnames for the current route

Aliases attach to the current directory's active route and reuse its port. Use
them when one app process serves multiple hosts. Wildcards match one label only:
*.app.lewp matches tags.app.lewp, not api.tags.app.lewp.

Usage:
  lewp alias add <host> [--json]
  lewp alias remove <host>
  lewp alias list [--json]

Examples:
  lewp alias add tags.app.lewp
  lewp alias add '*.app.lewp'
  lewp alias list
```

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

`alias add` requires an active route in the current directory. Run `lewp lease`
first. The host must be under `.lewp` or a suffix configured by
[`lewp setup --suffix`](#lewp-setup); public apex/domain-mirror hosts still
require the same setup opt-in rules as `lewp lease --host`.

Wildcards are supported as host patterns:

```sh
lewp alias add '*.app.lewp'
```

A wildcard matches exactly one label. `*.app.lewp` matches `tags.app.lewp`, but
not `app.lewp` and not `api.tags.app.lewp`. Lewp rejects aliases and wildcards
that conflict with an existing active host and reports the conflicting path.
Adding an exact alias already covered by a wildcard on the same route succeeds
with a warning.

Default `alias add` output mirrors `lewp lease` for the attached host:

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

Help:

```text
lewp init — generate a .lewp.local.toml for this directory

Writes a local config file with root, name, and (optionally) host so this
directory's identity is explicit and stable regardless of how it is laid out.
Values come from flags first, then path inference. It only writes the file; it
does not contact the daemon or lease anything.

Usage:
  lewp init [--root <root>] [--name <name>] [--host <host>] [--force]

Flags:
  --root <root>   Root segment to record (defaults to the inferred root)
  --name <name>   Instance segment to record (defaults to the inferred name)
  --host <host>   Record an explicit full host inside .lewp (e.g. atlas.lewp)
  --force         Overwrite an existing .lewp.local.toml

The file is meant to stay uncommitted. init prints the .git/info/exclude line to
keep it out of version control.

Examples:
  lewp init
  lewp init --root atlas --name feature-1
  lewp init --host atlas.lewp
```

Generate a `.lewp.local.toml` for the current directory so its identity is
explicit and stable regardless of how the folder is laid out. `init` only writes
the file — it never contacts the daemon, allocates a port, or leases a route.

```sh
lewp init
lewp init --root atlas --name feature-1
lewp init --host atlas.lewp
lewp init --force
```

Values come from flags first, then path inference (the same inference `lease`
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

Help:

```text
lewp info — show routes and bare ports for the current directory

Reads existing registry data only; it never creates, allocates, or changes a
route or port. Exits non-zero if no route or port is registered for this
directory. Default output is the same table shape as lewp list, scoped to this
directory.

Usage:
  lewp info [--name <name>] [--host <host>] [--json|--shell|--port]

Flags:
  --name <name>  Filter entries by logical name
  --host <host>  Filter entries by exact host
  --json         Emit matching entries as a JSON array
  --shell        Emit shell "export" lines; requires exactly one match
  --port         Emit only the port digits; requires exactly one match

Examples:
  lewp info
  lewp info --host app.atlas.lewp --port
  eval "$(lewp info --host app.atlas.lewp --shell)"
```

Show routes and bare ports registered for the current directory. `info` reads
existing registry data only — it never infers, allocates, or mutates a route or
port.

```sh
lewp info
lewp info --json
lewp info --host feature-1.atlas.lewp --port
lewp info --name vite --port
```

Default output for a directory with one route, one alias, and one bare port:

```sh
HOST                       NAME       KIND   PORT   STATE  PATH
feature-1.atlas.lewp       feature-1  route  42137  up     /Users/scott/projects/atlas/feature-1
tags.feature-1.atlas.lewp  feature-1  alias  42137  up     /Users/scott/projects/atlas/feature-1
-                          vite       port   42138  down   /Users/scott/projects/atlas/feature-1
```

Use `--json` for machine-readable entries. `--name` filters by logical name;
aliases share their route's name. `--host` filters by exact host, which is the
clearest way to select a route or alias.

`--port` emits only the port digits with no trailing newline. It requires
exactly one matching entry:

```sh
PORT="$(lewp info --host feature-1.atlas.lewp --port)"
VITE_RUBY_PORT="$(lewp info --name vite --port)"
```

`--shell` emits eval-safe exports for exactly one matching entry:

```sh
eval "$(lewp info --host feature-1.atlas.lewp --shell)"
```

If no route or port is registered for the current directory, `info` exits
non-zero and points you at the commands that create or relocate one:

```text
no Lewp route or port is registered for this directory
Run: lewp lease
Or lease a bare port: lewp port --name <name>
Or move an existing route here: lewp move --from <path>
```

## `lewp move`

Help:

```text
lewp move — move a route from another directory to this one

Reassigns the active route(s) owned by --from to the current directory, keeping
the same host, aliases, wildcards, and port. Use this after moving or renaming a
project folder so its stable URLs follow it. The source directory is left with
no route.

Usage:
  lewp move --from <path> [--json]

Flags:
  --from <path>   Directory that currently owns the route to move (required)
  --json          Emit the moved route(s) as a JSON array

Examples:
  lewp move --from ~/projects/atlas/old-feature
```

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

Help:

```text
lewp port — lease a bare internal port without a hostname

Useful for sidecar processes (asset bundlers, internal APIs) that need a stable
port but no .lewp host.

Usage:
  lewp port [--name <name>] [--json] [--shell]

Flags:
  --name <name>   Logical name for the port within this directory (default "port")
  --json          Emit the lease as a JSON object
  --shell         Emit shell "export" lines for use with eval

With no --name a bare port is leased under the default name "port", so repeated
"lewp port" calls from the same directory return the same number.

--shell emits one "export" line per value, so evaluate it rather than capturing
it into a single variable. For just the number, prefer --json with jq.

To free a named bare port in this directory, use "lewp release --name <name>".
Plain "lewp release" frees the route and every bare port for this directory.

Examples:
  lewp port --name vite
  eval "$(lewp port --name vite --shell)"
  VITE_RUBY_PORT="$(lewp port --name vite --json | jq -r .port)"
  lewp release --name vite
```

Lease a stable bare port with no hostname or proxy route.

```sh
lewp port
lewp port --name vite
lewp port --name vite --shell
```

`port` prints the same env-style output as `lease` minus the host fields: the
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

To free the named port in the current directory, use
[`lewp release --name <name>`](#lewp-release). To target the current owner of a
known number from any directory, use `lewp release --port <number>`. Plain
`lewp release` frees the route and every bare port for the current directory.

## `lewp release`

Help:

```text
lewp release — release routes and bare ports by path, host, or port

Plain release targets the current directory. It releases the route, including
aliases and wildcard hosts, and every bare port. Exact selectors do not prompt.
A no-op reports no match and exits successfully.

Usage:
  lewp release [--path <path> [--recursive] [--route | --name <name>] |
                --host <host> | --port <number>]
               [--forget] [--dry-run] [--json] [--yes|-y]

Targets and scopes:
  --path <path>    Target an exact registered path, including an external or
                   deleted path. Lewp cleans the path lexically; it does not
                   resolve symlinks or require the path to exist.
  --recursive      Include paths below --path at path-component boundaries
  --host <host>    Target the current owner of an exact registered host
  --port <number>  Target the current owner of a numeric port
  --name <name>    Release one named bare port under a path
  --route          Release only the route under a path, keeping bare ports

Output and safety:
  --forget         Delete matching registry history. Release otherwise keeps
                   history. With a path, forget also deletes released history.
  --dry-run        Show the plan without prompting or changing the registry
  --json           Emit the result as one JSON object
  --yes, -y        Approve a recursive mutation without a prompt. Scripts and
                   recursive JSON mutations require this flag.

A recursive mutation prints its full plan before asking for confirmation. Lewp
applies that exact plan atomically and refuses it if registry state changes;
rerun the command to review a new plan. Recursive selection from / is allowed
for dry runs but remains protected by the same confirmation rules for changes.

Examples:
  lewp release
  lewp release --path /work/deleted-worktree
  lewp release --path /work/worktrees --recursive --dry-run
  lewp release --path /work/worktrees --recursive --yes
  lewp release --path /work/app --route
  lewp release --path /work/app --name vite
  lewp release --host app.work.lewp
  lewp release --host '*.app.work.lewp'
  lewp release --port 42137
  lewp release --path /work/deleted-worktree --forget
  lewp release --path /work/worktrees --recursive --yes --json
```

### Targets and path matching

Plain `lewp release` uses the current directory as an implicit exact path. It
releases that path's route, aliases, wildcard hosts, and bare ports. Use
`--route` to select its route or `--name vite` to select one bare port:

```sh
lewp release
lewp release --path /work/app --route
lewp release --path /work/app --name vite
```

`--path` accepts an absolute path or resolves a relative path against the
invoking directory. Lewp makes the result absolute and cleans `.` and `..`
lexically. It does not call `realpath`, follow symlinks, or require the target to
exist. The cleaned string must match the path that Lewp registered. This lets
you clean up a moved or deleted worktree:

```sh
lewp release --path /work/deleted-worktree
lewp release --path /work/deleted-worktree --forget
```

An exact path selects only that registration. `--recursive` also selects
registered descendants at path-component boundaries. For example,
`/work/app` includes `/work/app/admin` but not `/work/application`. Lewp does
not walk the filesystem.

```sh
lewp release --path /work/worktrees --recursive --dry-run
lewp release --path /work/worktrees --recursive --yes
```

A host selector matches an exact registered primary host, alias, or wildcard.
Quote wildcard arguments so the shell cannot expand them. Host and numeric-port
selectors target current owners only; they do not search released history.
A numeric port can belong to a route or bare port.

```sh
lewp release --host app.work.lewp
lewp release --host '*.app.work.lewp'
lewp release --port 42137
```

You may combine `--recursive` only with an explicit `--path`. The `--route` and
`--name` scopes also require a path selector and cannot be combined with each
other. Without a scope, a path selects its route and all bare ports.

### Release, forget, and counts

Release frees active allocations and keeps their registry history. `--forget`
deletes the matching history. A path with `--forget` can select active and
released history, so it works after you released an item or deleted its path.
Host and port selectors still require a current owner.

Lewp reports one item per logical allocation. Database row count does not affect
the totals. One route counts as one item even when it has aliases, wildcard
hosts, or several historical port numbers. One named bare port at one path also
counts as one item.

- `matched` counts logical route and bare-port items in the plan.
- `released` counts matched active items with a `release` action.
- `forgotten` counts matched items with a `forget` action.

An active item selected with `--forget` has both actions, so it increments both
`released` and `forgotten`. Released-only history has only the `forget` action
and increments `forgotten`. Dry runs report the counts that an apply would
produce. Human output labels them `Planned releases` and `Planned forgets` for a
dry run, or `Released` and `Forgotten` after an apply.

A selector with no matches prints a specific no-match message and exits `0`.
Repeated release and forget commands therefore succeed as idempotent no-ops.

### Preview, confirmation, and atomic apply

`--dry-run` prints the complete plan and never prompts or changes the registry.
It works without `--yes`, including recursive JSON dry runs.

An interactive recursive mutation prints the complete plan and defaults its
`Proceed? [y/N]` prompt to no. Pass `--yes` or `-y` to approve it without a
prompt. Noninteractive recursive mutations and recursive mutations with
`--json` require `--yes` or `-y`. Exact path, host, and port mutations do not
prompt.

The recursive path `/` can match every registered path. Lewp safeguards a `/`
mutation with the same full preview and confirmation rules; run a dry run first.

Lewp builds a plan from one registry snapshot and applies that exact plan in one
transaction. If registry state changes between preview and apply, Lewp refuses
the stale plan, changes nothing, and prints `release plan changed; rerun the
release command`. Rerun the original command to inspect a fresh plan.

### JSON output

`--json` writes one object and no human table or prompt. Its top-level fields
are:

- `operation`: `"release"`
- `dry_run`: boolean
- `selector`: the normalized public selector
- `matched`, `released`, `forgotten`: logical item/action counts described above
- `items`: a non-null array of logical items

`selector.type` is `"path"`, `"host"`, or `"port"`. A path selector includes
`path`, `implicit`, `recursive`, and `scope`; `scope` is `"all"`, `"route"`, or
`"name"`, and name scope also includes `name`. A host selector includes `host`.
A port selector includes numeric `port`.

Each item includes `kind` (`"route"` or `"port"`), `path`, `state`
(`"up"`, `"down"`, `"stale"`, or `"released"`), nullable current `port`, a
sorted `ports` array of distinct current and historical numbers, and `actions`.
Actions contain `"release"`, `"forget"`, or both in that order. Route items
also include primary `host` and a `hosts` array whose entries contain `host` and
`type` (`"primary"`, `"alias"`, or `"wildcard"`). Bare-port items include
`name` instead. Lewp keeps internal route, lease, host, and port row IDs private
and never includes them in JSON output.

For a recursive JSON mutation, pass `--yes` or `-y`:

```sh
lewp release --path /work/worktrees --recursive --yes --json
```

## `lewp list`

Help:

```text
lewp list — list routes, aliases, and bare ports with their health

States are derived from TCP checks: up, down, or stale. The human table has
HOST, NAME, KIND, PORT, STATE, and PATH columns; KIND is route, alias, or port.
Bare ports show "-" for HOST.

Usage:
  lewp list [--all] [--json]

Flags:
  --all    Include released and stale history, not just active entries
  --json   Emit the entries as a JSON array

Examples:
  lewp list
  lewp list --json | jq '.[] | select(.kind == "alias")'
```

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

Help:

```text
lewp doctor — diagnose daemon, DNS, and CA trust

Usage:
  lewp doctor

Reports daemon state, control-socket reachability, resolver configuration, and
whether the local CA is trusted in the macOS keychain. It also compares the
binary launchd is configured to run against the CLI you are running now, so you
can tell whether bin/install / bin/reinstall updated what launchd launches.
```

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

When the daemon is up, `doctor` additionally resolves `probe.lewp` through the
macOS system resolver (`dscacheutil`) rather than the responder's own port. The
direct `.lewp` lookup can pass while `mDNSResponder` has not picked up
`/etc/resolver/lewp` (a stale cache or resolver-file quirk), which leaves every
other check green while browsers still cannot resolve `.lewp`. This probe
exercises the same path browsers use: it warns when the name does not resolve
(with `sudo killall -HUP mDNSResponder` as the recovery hint) and fails when it
resolves to a non-loopback address.

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

Help:

```text
lewp logs — show or tail the launchd-managed daemon logs

Reads the daemon's stdout/stderr logs under ~/Library/Logs/lewp (request
routing details, startup errors). It only reads files; it never starts the
daemon.

Usage:
  lewp logs [--lines N] [--grep TEXT] [--follow] [--path]

Flags:
  --lines N    Number of trailing lines to show per log (default 200)
  --grep TEXT  Show only lines containing TEXT (case-insensitive); --lines then
               bounds the matching lines, like "grep TEXT | tail -n N"
  --follow     Print new log output as it is appended (Ctrl-C to stop)
  -f           Alias for --follow
  --path       Print the log file paths only and exit

Examples:
  lewp logs
  lewp logs --lines 500
  lewp logs --grep error
  lewp logs --grep 502 --lines 50
  lewp logs --follow
  lewp logs --path
```

Show or tail the launchd-managed daemon's logs. launchd writes the daemon's
stdout and stderr to `daemon.out.log` and `daemon.err.log` under
`~/Library/Logs/lewp` (set as `StandardOutPath` / `StandardErrorPath` in the
plist). By default the daemon's stdout carries a request line only for failed
requests (a proxy error or an HTTP status ≥ 400); startup failures land on
stderr. This errors-only default keeps the log from growing without bound under
a steady stream of successful HMR/SSE/websocket traffic. To log every proxied
request (host, method, scheme, upstream target, status, and any proxy error),
run `lewp setup --log-requests=all` (see [`lewp setup`](#lewp-setup)).

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

Help:

```text
lewp completion — print a shell completion script

Usage:
  lewp completion bash|zsh|fish

Prints a static completion script for the given shell to stdout. The script is
self-contained and never contacts the daemon. Redirect it into the location your
shell loads completions from:

  bash:  lewp completion bash > /usr/local/etc/bash_completion.d/lewp
  zsh:   lewp completion zsh  > "${fpath[1]}/_lewp"   # then restart zsh
  fish:  lewp completion fish > ~/.config/fish/completions/lewp.fish

Examples:
  lewp completion bash
  lewp completion zsh
```

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

Help:

```text
lewp version — print version

Usage:
  lewp version [--detailed]

Prints only the public version by default.

Flags:
  --detailed   Include git commit, UTC build time, and Go toolchain version
```

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

## `lewp upgrade`

Help:

```text
lewp upgrade — replace this binary with the latest GitHub release

Usage:
  lewp upgrade

Downloads the latest release archive for this Mac, extracts the lewp binary, and
atomically replaces the executable currently running this command. If launchd is
already using that binary, restart the daemon after upgrade:

  lewp system restart
```

Download the latest GitHub release archive for this Mac, extract the `lewp`
binary, and replace the executable currently running the command.

```sh
lewp upgrade
lewp system restart
```

If the current binary already matches the latest release, `upgrade` reports that
it is up to date and exits `0`. After a successful replacement, restart the
daemon so launchd runs the new binary.

## `lewp daemon`

Help:

```text
lewp daemon — run the daemon in the foreground

Usage:
  lewp daemon [--log-requests errors|all]

Normally launchd starts this command. It owns the registry, .lewp DNS responder,
HTTP/HTTPS proxy, and local control socket.

--log-requests controls request-log volume. The default "errors" logs only
failed requests (a proxy error or an HTTP status >= 400); "all" logs every
proxied request. Prefer "lewp setup --log-requests=all" so the choice persists
in the launchd plist.
```

Daemon mode. Normally launchd starts this command.

The daemon owns:

- SQLite registry
- Unix control socket
- HTTP proxy listeners from launchd
- HTTPS proxy listeners from launchd
- SNI certificate minting from the persisted local CA

The daemon logs failed requests (proxy errors and statuses ≥ 400) to stdout
(captured in `daemon.out.log`); inspect it with `lewp logs`. Pass
`--log-requests=all` (normally persisted by `lewp setup --log-requests=all`) to
log every proxied request instead. Startup errors are written to stderr
(`daemon.err.log`).

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
  labels when available and next steps (`lewp lease`, `lewp list`, `lewp doctor`).
  Unmanaged hosts get a plain `404`. The host is HTML-escaped so a crafted
  hostname cannot inject markup.

## Browser URLs

For routed leases, open:

```text
http://<host>
https://<host>   # .lewp and safe-subtree custom suffix hosts
```

DNS resolves all `.lewp` names and configured custom suffixes to loopback. The
proxy routes by full registered host. Different primary routes can point to
different local ports, while route aliases let multiple hostnames point to one
route and port. Wildcard aliases match one label only.

HTTPS uses Lewp's local CA, created and trusted by `lewp setup`. Lewp mints
certificates for `.lewp` SNI names and configured safe-subtree custom suffixes;
the CA carries critical X.509 name constraints limited to exactly that set, and
`lewp setup` rotates the CA whenever the configured suffix set changes.
Domain-mirror suffixes always route over HTTP — Lewp's CA is cryptographically
unable to sign a real registrable domain.

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

Port and host conflicts:

- Lewp does not take another current allocation.
- An inferred primary host conflict receives a deterministic suffix. An
  explicit primary host conflict reports its current owner.
- Conflicting aliases and wildcards report the registered owner path.
- Release a stale route owner from any directory with
  `lewp release --path "/work/old app" --route --forget`.
