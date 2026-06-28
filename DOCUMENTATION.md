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
lewp setup
lewp system start|stop|status|restart|uninstall
lewp add [--root R] [--name N] [--host H] [--json|--shell]
lewp info [--json]
lewp move --from <path> [--json]
lewp port [--name N] [--json|--shell]
lewp release [--forget]
lewp list [--all]
lewp doctor
lewp logs [--lines N] [--follow] [--path]
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

Output:

```sh
DNS=resolver-file
HTTPS=enabled
LAUNCHD=/Users/scott/Library/LaunchAgents/dev.lewp.daemon.plist
RESOLVER=/etc/resolver/lewp
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
resolver file.

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

## `lewp add`

Register a routed app port and a `.lewp` hostname for the current directory.
This is the command for creating a route.

```sh
lewp add
lewp add --root audit --name feature-1
lewp add --host audit.lewp
lewp add --json
lewp add --shell
```

Default output:

```sh
PORT=42137
URL=http://feature-1.audit.lewp
HOST=feature-1.audit.lewp
```

`--shell` prefixes values with `export`:

```sh
export PORT=42137
export URL=http://feature-1.audit.lewp
export HOST=feature-1.audit.lewp
```

`--json` prints machine-readable fields including root/name/host metadata,
path, kind, source fields, warnings, and release state.

Host rules:

- default host: `<instance>.<root>.lewp`
- project apex override: `lewp add --host audit.lewp`
- custom `.lewp` override: `lewp add --host sso.audit.lewp`
- non-`.lewp` hosts are rejected

Discovery order:

1. flags: `--root`, `--name`, `--host`
2. nearest `.lewp.local.toml`
3. path/git worktree inference

Example `.lewp.local.toml`:

```toml
root = "audit"
name = "feature-1"
host = "audit.lewp"
```

If another folder already owns a host, Lewp keeps the original owner and assigns
a deterministic suffix to the new folder.

## `lewp info`

Show the route registered for the current directory. `info` reads existing
registry data only — it never infers, allocates, or mutates a route.

```sh
lewp info
lewp info --json
```

Default output for a registered directory:

```sh
PORT=42137
URL=http://feature-1.audit.lewp
HOST=feature-1.audit.lewp
PATH=/Users/scott/projects/audit/feature-1
```

If no route is registered for the current directory, `info` exits non-zero and
points you at the commands that create or relocate one:

```text
no Lewp route is registered for this directory
Run: lewp add
Or move an existing route here: lewp move --from <path>
```

## `lewp move`

Move an existing route from another directory to the current directory, keeping
the same host and port. Use this after relocating or renaming a project folder
so its stable URL follows it.

```sh
lewp move --from ~/projects/audit/old-feature
lewp move --from ~/projects/audit/old-feature --json
```

`--from` is required and names the directory that currently owns the route. The
move is atomic: the source directory is left with no route (`lewp info` there
reports none) and the current directory becomes the owner with the same port and
host. The port is never reallocated.

If the source directory has no active route, or the current directory already
owns an active route, `move` fails with a clear error and changes nothing.

## `lewp port`

Lease a stable bare port with no hostname or proxy route.

```sh
lewp port
lewp port --name vite
lewp port --name vite --shell
```

Use this for internal services that need unique local ports across worktrees,
such as Vite:

```sh
export VITE_RUBY_PORT="$(lewp port --name vite --json | jq -r .port)"
```

## `lewp release`

Release the current folder route.

```sh
lewp release
lewp release --forget
```

Without `--forget`, history stays in the registry. With `--forget`, Lewp removes
the remembered identity/history for the current folder.

## `lewp list`

List registry entries.

```sh
lewp list
lewp list --all
```

Columns:

```text
HOST    PORT    STATE    PATH
```

States:

- `up`: TCP connect to the leased port succeeds
- `down`: TCP connect fails
- `stale`: registered path no longer exists
- `released`: route was explicitly released

Default list hides released routes. `--all` includes registry history.

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
lewp logs              # last 50 lines of each log, with a header per file
lewp logs --lines 200  # last 200 lines of each log
lewp logs --follow     # stream new output until Ctrl-C (alias: -f)
lewp logs --path       # print the two log file paths and exit
```

`lewp logs` only reads files; it never starts the daemon. When no logs exist
yet, it prints where they will appear after `lewp setup && lewp system start`.

## `lewp version`

Print version and build metadata.

```sh
lewp version
```

Example:

```text
lewp version 0.1.0
commit:  0a778ce
built:   2026-06-27T23:08:00Z
go:      go1.25.1
```

`version`, `--version`, and `-v` are equivalent. The version, commit, and build
time are injected at link time by `bin/build`; an un-stamped `go build` reports
`dev`/`unknown` values.

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
common failure modes:

- **Registered but not responding** — when a host is leased but its target port
  is closed, the proxy returns `502` with the host, loopback target, project
  path, root/name, last-seen time, release state, a suggested `PORT=<n> bin/dev`
  start command, and `lewp list` / `lewp doctor` hints.
- **Unregistered `.lewp` host** — when a `.lewp` name has no route, the proxy
  returns `404` with the parsed instance/root labels and next steps
  (`lewp add`, `lewp list`, `lewp doctor`). Non-`.lewp` hosts get a plain
  `404`. The host is HTML-escaped so a crafted hostname cannot inject markup.

## Browser URLs

For routed leases, open:

```text
http://<host>
https://<host>
```

DNS resolves all `.lewp` names to loopback. The proxy routes by full registered
host, so `feature-1.audit.lewp` and `audit.lewp` can point to different local
ports.

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
- conflicting hosts receive deterministic suffixes
- use `lewp release --forget` from old folders to remove stale ownership
