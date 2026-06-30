package cli

import (
	"flag"
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
  add        Register a stable port and managed hostname for the current directory
  alias      Manage extra hostnames for the current route
  init       Generate a .lewp.local.toml identity file for the current directory
  info       Show routes and bare ports registered for the current directory
  move       Move a route from another directory to the current directory
  port       Lease a bare internal port without a hostname
  release    Release the route for the current directory
  list       List active routes and their health
  suffix     List or remove custom managed suffixes
  doctor     Diagnose daemon state, DNS, and CA trust
  logs       Show or tail the daemon logs
  completion Print a shell completion script (bash|zsh|fish)
  version    Print version
  daemon     Run the daemon in the foreground (normally launchd-managed)

Examples:
  lewp setup && lewp system start
  cd ~/projects/atlas/feature-1 && lewp add
  eval "$(lewp add --shell)" && PORT=$PORT bin/dev

Configuration (highest priority first):
  flags             --root, --name, --host on lewp add
  environment       LEWP_ROOT, LEWP_NAME, LEWP_HOST
  .lewp.local.toml  nearest file up the tree; keys: root, name, host
  inference         parent dir -> root, current dir -> name

The .lewp.local.toml file is meant to stay local/uncommitted. Run "lewp init"
to generate one and to print a .git/info/exclude line that keeps it out of git.

Run "lewp <command> --help" for command-specific help.
`

// Per-command help. Each is printed to stdout on `lewp <cmd> --help`.
const (
	setupHelp = `lewp setup — one-time macOS setup for .lewp routing and .lewp HTTPS

Setup may prompt for your password (sudo) to install /etc/resolver/lewp, and
macOS may prompt you to trust the local development CA in your keychain.

Usage:
  lewp setup [--suffix <suffix>] [--allow-domain-mirror] [--start]

It writes /etc/resolver/lewp (via sudo when needed), creates the local CA under
~/Library/Application Support/lewp/, installs the launchd plist, and trusts the
CA with the macOS "security" tool. Run "lewp system start" afterward.

Custom public dev suffixes default to safe-subtree mode:
  lewp setup --suffix local.todoordie.com

This installs a resolver only for the subtree, so todoordie.com and
www.todoordie.com continue to use public DNS.

Domain mirror mode is explicit because it shadows public DNS locally while the
resolver exists:
  lewp setup --suffix localkickofflabs.com --allow-domain-mirror
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

	addHelp = `lewp add — register a stable port and host for this directory

Re-running from the same directory returns the same port and host. By default,
the host is under .lewp. Root and name are inferred from the directory layout
unless overridden.

Identity is discovered in this order: CLI flags, then LEWP_ROOT / LEWP_NAME /
LEWP_HOST environment variables, then the nearest .lewp.local.toml, then path
inference. Inferred values and warnings are printed to stderr so --shell and
$(...) capture only the clean env lines.

Usage:
  lewp add [--root <root>] [--name <name>] [--host <host>] [--auto-suffix] [--json] [--shell]

Flags:
  --root <root>   Override the inferred root segment of the hostname
  --name <name>   Override the inferred instance segment of the hostname
  --host <host>   Register an explicit .lewp or managed custom-suffix host
  --auto-suffix   On an explicit --host conflict, append a deterministic suffix
                  instead of failing
  --json          Emit the route as a JSON object
  --shell         Emit shell "export" lines for use with eval

An explicit --host that is already assigned to another directory fails by
default with the conflicting path and cleanup guidance. An inferred host that
conflicts is given a stable deterministic suffix automatically.

Examples:
  lewp add
  lewp add --root atlas --name feature-1
  eval "$(lewp add --shell)"
`

	aliasHelp = `lewp alias — manage extra hostnames for the current route

Usage:
  lewp alias add <host> [--json]
  lewp alias remove <host>
  lewp alias list [--json]

Examples:
  lewp alias add tags.app.lewp
  lewp alias add '*.app.lewp'
  lewp alias list
`

	initHelp = `lewp init — generate a .lewp.local.toml for this directory

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
`

	infoHelp = `lewp info — show routes and bare ports for the current directory

Reads existing registry data only; it never creates, allocates, or changes a
route or port. Exits non-zero if no route or port is registered for this
directory.

Usage:
  lewp info [--json]

Flags:
  --json   Emit the registered entries as a JSON array

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
  lewp move --from ~/projects/atlas/old-feature
`

	portHelp = `lewp port — lease a bare internal port without a hostname

Useful for sidecar processes (asset bundlers, internal APIs) that need a stable
port but no .lewp host.

Usage:
  lewp port [--name <name>] [--json] [--shell]
  lewp port release [--name <name>] [--forget]

Flags:
  --name <name>   Logical name for the port within this directory (default "port")
  --json          Emit the lease as a JSON object
  --shell         Emit shell "export" lines for use with eval

With no --name a bare port is leased under the default name "port", so repeated
"lewp port" calls from the same directory return the same number.

--shell emits one "export" line per value, so evaluate it rather than capturing
it into a single variable. For just the number, prefer --json with jq.

Subcommand:
  lewp port release [--name <name>] [--forget]
      Release a single bare port lease for this directory (default name "port").
      Add --forget to also drop its remembered identity. Releasing nothing is
      reported, not an error. Use "lewp release --all" to drop the route and
      every bare port at once.

Examples:
  lewp port --name vite
  eval "$(lewp port --name vite --shell)"
  VITE_RUBY_PORT="$(lewp port --name vite --json | jq -r .port)"
  lewp port release --name vite
`

	releaseHelp = `lewp release — release the route (and optionally bare ports) for this directory

Usage:
  lewp release [--all] [--forget]

Flags:
  --all      Also release every bare port leased for this directory, not just
             the route
  --forget   Also remove remembered identity and history for this directory

Release is idempotent: releasing when nothing is active is reported, never an
error. To release a single named bare port instead of all of them, use
"lewp port release --name <name>".

Examples:
  lewp release
  lewp release --all
  lewp release --forget
`

	listHelp = `lewp list — list routes and bare ports with their health

States are derived from TCP checks: up, down, or stale. The human table has
HOST, NAME, KIND, PORT, STATE, and PATH columns; bare ports show "-" for HOST.

Usage:
  lewp list [--all] [--json]

Flags:
  --all    Include released and stale history, not just active entries
  --json   Emit the entries as a JSON array

Examples:
  lewp list
  lewp list --json | jq '.[] | select(.kind == "route")'
`

	suffixHelp = `lewp suffix — list or remove custom managed suffixes

Usage:
  lewp suffix list
  lewp suffix remove <suffix>

Commands:
  list             List built-in and custom managed suffixes
  remove <suffix>  Remove a custom managed suffix and its resolver file

.lewp is built in and cannot be removed.
`

	doctorHelp = `lewp doctor — diagnose daemon, DNS, and CA trust

Usage:
  lewp doctor

Reports daemon state, control-socket reachability, resolver configuration, and
whether the local CA is trusted in the macOS keychain. It also compares the
binary launchd is configured to run against the CLI you are running now, so you
can tell whether bin/install / bin/reinstall updated what launchd launches.
`

	logsHelp = `lewp logs — show or tail the launchd-managed daemon logs

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
`

	versionHelp = `lewp version — print version

Usage:
  lewp version [--detailed]

Prints only the public version by default.

Flags:
  --detailed   Include git commit, UTC build time, and Go toolchain version
`

	completionHelp = `lewp completion — print a shell completion script

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

// parseFlags parses a subcommand's flags and, on error, prints a concise
// message plus a pointer to the command's --help instead of letting the flag
// package dump a generated usage block built from our intentionally terse flag
// descriptions. Returns false when the caller should exit with code 2.
func parseFlags(cfg Config, fs *flag.FlagSet, name string) bool {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if err := fs.Parse(cfg.Args[1:]); err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp %s: %v\n", name, err)
		fmt.Fprintf(cfg.Stderr, "Run: lewp %s --help\n", name)
		return false
	}
	return true
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
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	detailed := fs.Bool("detailed", false, "")
	if !parseFlags(cfg, fs, "version") {
		return 2
	}
	fmt.Fprintf(cfg.Stdout, "lewp version %s\n", cfg.Version)
	if *detailed {
		fmt.Fprintf(cfg.Stdout, "commit:  %s\n", cfg.Commit)
		fmt.Fprintf(cfg.Stdout, "built:   %s\n", cfg.BuildDate)
		fmt.Fprintf(cfg.Stdout, "go:      %s\n", runtime.Version())
	}
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
