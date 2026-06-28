# Lewp

Lewp is a macOS-first local domain router/proxy for parallel local development.
It leases stable loopback ports, assigns predictable `.lewp` hostnames, and
reverse-proxies browser traffic to developer-started processes.

Lewp is not a process manager. It never starts Rails, Vite, Next, Docker, or any
other app process.

## Install

From source:

```sh
git clone <repo-url> lewp
cd lewp
bin/reinstall
```

This installs to `~/.local/bin/lewp` by default. Make sure `~/.local/bin` is in
your `PATH`. Override with `LEWP_INSTALL_DIR=/some/bin bin/install`.

Requirements:

- macOS
- Go 1.25+
- permission to install a LaunchAgent and trust a local development CA

## Setup

Run one-time setup:

```sh
lewp setup
lewp system start
```

`setup` writes `/etc/resolver/lewp`, creates Lewp's local CA files under
`~/Library/Application Support/lewp/`, writes the LaunchAgent plist, and trusts
the CA with macOS `security`.

Expect two prompts during `setup`:

- **sudo**: installing `/etc/resolver/lewp` needs root, so `setup` runs `sudo`
  and macOS may prompt for your password.
- **keychain trust**: trusting the local CA may pop a macOS dialog asking you to
  allow the change to your keychain.

If a step fails, `setup` prints the exact failing command and a `Next:` line with
how to recover (for example, the precise `security add-trusted-cert` command to
run by hand). `system start` runs the launchd bootstrap command; if the
LaunchAgent is already loaded, it falls back to `launchctl kickstart -k`. Daemon
logs are written under `~/Library/Logs/lewp/`.

Check the installed build with:

```sh
lewp version
```

## Quick Start

From a project instance directory:

```sh
cd ~/projects/audit/feature-1
lewp add
```

Example output:

```sh
PORT=42137
URL=http://feature-1.audit.lewp
HOST=feature-1.audit.lewp
```

(`lewp lease` is a backward-compatible alias for `lewp add`.)

Start your app yourself on the leased port:

```sh
PORT=42137 bin/dev
```

Then open:

```text
http://feature-1.audit.lewp
https://feature-1.audit.lewp
```

Check or relocate a route later:

```sh
lewp info                                  # show this directory's route
lewp move --from ~/projects/audit/feature-1   # bring its route here, same port
```

## CLI Reference

Full command documentation lives in [DOCUMENTATION.md](DOCUMENTATION.md).

## Development

```sh
go test ./...
bin/build
```

The implementation follows the V1 plan in [docs/plan.md](docs/plan.md).
