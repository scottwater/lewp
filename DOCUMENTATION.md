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
lewp lease [--root R] [--name N] [--host H] [--json|--shell]
lewp port [--name N] [--json|--shell]
lewp release [--forget]
lewp list [--all]
lewp doctor
lewp daemon
```

## `lewp setup`

Creates Lewp local TLS CA material and trusts it in the macOS login keychain.
Installing `/etc/resolver/lewp` uses `sudo` when the command is not already
running as root.

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

`status` checks the daemon control socket and prints:

```text
lewp daemon is running
```

## `lewp lease`

Lease a routed app port and register a `.lewp` hostname for the current
directory.

```sh
lewp lease
lewp lease --root audit --name feature-1
lewp lease --host audit.lewp
lewp lease --json
lewp lease --shell
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
- project apex override: `lewp lease --host audit.lewp`
- custom `.lewp` override: `lewp lease --host sso.audit.lewp`
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
```

The current implementation checks daemon reachability, control socket
reachability, local CA material, and keychain trust. The V1 doctor contract also
tracks resolver file state, `.lewp` lookup, proxy port binding, registry
readability, current-folder identity inference, current target port state, and
hostname conflicts.

If setup has not created local CA material:

```text
https: not configured (run lewp setup)
keychain: not trusted (run lewp setup)
```

## `lewp daemon`

Daemon mode. Normally launchd starts this command.

The daemon owns:

- SQLite registry
- Unix control socket
- HTTP proxy listeners from launchd
- HTTPS proxy listeners from launchd
- SNI certificate minting from the persisted local CA

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
