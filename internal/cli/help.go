package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/scottwater/lewp/internal/buildinfo"
)

// mainHelp is the top-level help shown for `lewp`, `lewp help`, `lewp --help`,
// no arguments, and unknown commands. Kept concise and terminal-friendly.
const mainHelp = `lewp — local domain router and port leaser for parallel development

Lewp leases a stable loopback port and a predictable <instance>.<root>.lewp
hostname for the current directory, then reverse-proxies browser traffic to a
process you start yourself. It routes and leases only; it never starts apps.

Usage:
  lewp <command> [flags]

Commands:
  setup      Install the .lewp DNS resolver, local CA, and launchd service
  system     Manage the daemon: start|stop|status|restart|uninstall
  add        Register a stable port and .lewp hostname for the current directory
  info       Show the route registered for the current directory
  move       Move a route from another directory to the current directory
  port       Lease a bare internal port without a hostname
  release    Release the route for the current directory
  list       List active routes and their health
  doctor     Diagnose daemon state, DNS, and CA trust
  version    Print version and build metadata
  daemon     Run the daemon in the foreground (normally launchd-managed)
  lease      Alias for add (kept for backward compatibility)

Examples:
  lewp setup && lewp system start
  cd ~/projects/audit/feature-1 && lewp add
  eval "$(lewp add --shell)" && PORT=$PORT bin/dev

Run "lewp <command> --help" for command-specific help.
`

// Per-command help. Each is printed to stdout on `lewp <cmd> --help`.
const (
	setupHelp = `lewp setup — one-time macOS setup for .lewp routing and HTTPS

Setup may prompt for your password (sudo) to install /etc/resolver/lewp, and
macOS may prompt you to trust the local development CA in your keychain.

Usage:
  lewp setup

It writes /etc/resolver/lewp (via sudo when needed), creates the local CA under
~/Library/Application Support/lewp/, installs the launchd plist, and trusts the
CA with the macOS "security" tool. Run "lewp system start" afterward.
`

	systemHelp = `lewp system — manage the launchd-managed daemon

Usage:
  lewp system <action>

Actions:
  start      Bootstrap the daemon (falls back to kickstart if already loaded)
  stop       Bootout the daemon
  restart    Kickstart the daemon
  status     Report whether the daemon is responding
  uninstall  Remove the launchd, resolver, and keychain-trust integration

Examples:
  lewp system start
  lewp system status
`

	addHelp = `lewp add — register a stable port and .lewp hostname for this directory

Re-running from the same directory returns the same port and host. Root and name
are inferred from the directory layout unless overridden.

Usage:
  lewp add [--root <root>] [--name <name>] [--host <host>] [--json] [--shell]

Flags:
  --root <root>   Override the inferred root segment of the hostname
  --name <name>   Override the inferred instance segment of the hostname
  --host <host>   Register an explicit apex host (e.g. audit.lewp)
  --json          Emit the route as a JSON object
  --shell         Emit shell "export" lines for use with eval

Examples:
  lewp add
  lewp add --root audit --name feature-1
  eval "$(lewp add --shell)"
`

	leaseHelp = `lewp lease — alias for "lewp add" (kept for backward compatibility)

"lease" behaves exactly like "add"; prefer "lewp add" in new scripts and docs.
Re-running from the same directory returns the same port and host. Root and name
are inferred from the directory layout unless overridden.

Usage:
  lewp lease [--root <root>] [--name <name>] [--host <host>] [--json] [--shell]

Flags:
  --root <root>   Override the inferred root segment of the hostname
  --name <name>   Override the inferred instance segment of the hostname
  --host <host>   Register an explicit apex host (e.g. audit.lewp)
  --json          Emit the route as a JSON object
  --shell         Emit shell "export" lines for use with eval

Examples:
  lewp lease
  lewp lease --root audit --name feature-1
  eval "$(lewp lease --shell)"
`

	infoHelp = `lewp info — show the route registered for the current directory

Reads existing registry data only; it never creates, allocates, or changes a
route. Exits non-zero if no route is registered for this directory.

Usage:
  lewp info [--json]

Flags:
  --json   Emit the route(s) as a JSON array

Examples:
  lewp info
  lewp info --json
`

	moveHelp = `lewp move — move a route from another directory to this one

Reassigns the active route(s) owned by --from to the current directory, keeping
the same host and port. Use this after moving or renaming a project folder so
its stable URL follows it. The source directory is left with no route.

Usage:
  lewp move --from <path> [--json]

Flags:
  --from <path>   Directory that currently owns the route to move (required)
  --json          Emit the moved route(s) as a JSON array

Examples:
  lewp move --from ~/projects/audit/old-feature
`

	portHelp = `lewp port — lease a bare internal port without a hostname

Useful for sidecar processes (asset bundlers, internal APIs) that need a stable
port but no .lewp host.

Usage:
  lewp port [--name <name>] [--json] [--shell]

Flags:
  --name <name>   Logical name for the port within this directory (default "port")
  --json          Emit the lease as a JSON object
  --shell         Emit shell "export" lines for use with eval

Examples:
  lewp port --name vite
  PORT=$(lewp port --name vite --shell)
`

	releaseHelp = `lewp release — release the route for the current directory

Usage:
  lewp release [--forget]

Flags:
  --forget   Also remove remembered identity and history for this directory

Examples:
  lewp release
  lewp release --forget
`

	listHelp = `lewp list — list routes and their health

States are derived from TCP checks: up, down, or stale.

Usage:
  lewp list [--all]

Flags:
  --all   Include released and stale history, not just active routes
`

	doctorHelp = `lewp doctor — diagnose daemon, DNS, and CA trust

Usage:
  lewp doctor

Reports daemon state, control-socket reachability, resolver configuration, and
whether the local CA is trusted in the macOS keychain.
`

	versionHelp = `lewp version — print version and build metadata

Usage:
  lewp version

Prints the version, git commit, UTC build time, and Go toolchain version.
`

	daemonHelp = `lewp daemon — run the daemon in the foreground

Usage:
  lewp daemon

Normally launchd starts this command. It owns the registry, .lewp DNS responder,
HTTP/HTTPS proxy, and local control socket.
`
)

func printMainHelp(w io.Writer) {
	fmt.Fprint(w, mainHelp)
}

// helpRequested reports whether -h/--help appears before a "--" terminator.
func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func runVersion(cfg Config) int {
	fmt.Fprintf(cfg.Stdout, "lewp version %s\n", cfg.Version)
	fmt.Fprintf(cfg.Stdout, "commit:  %s\n", cfg.Commit)
	fmt.Fprintf(cfg.Stdout, "built:   %s\n", cfg.BuildDate)
	fmt.Fprintf(cfg.Stdout, "go:      %s\n", runtime.Version())
	return 0
}

func defaultVersion() string {
	return buildinfo.Version
}

func defaultCommit() string {
	return buildinfo.Commit
}

func defaultBuildDate() string {
	return buildinfo.Date
}
