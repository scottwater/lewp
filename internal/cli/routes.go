package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/identity"
)

func runLease(cfg Config) int {
	fs := flag.NewFlagSet("lease", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	root := fs.String("root", "", "")
	name := fs.String("name", "", "")
	host := fs.String("host", "", "")
	jsonOut := fs.Bool("json", false, "")
	shell := fs.Bool("shell", false, "")
	autoSuffix := fs.Bool("auto-suffix", false, "")
	reset := fs.Bool("reset", false, "")
	if !parseFlags(cfg, fs, "lease") {
		return 2
	}
	// The wire command stays "add" so a newer CLI keeps working against an
	// older daemon; only the CLI-visible verb is "lease".
	resp, err := call(cfg, control.Request{Command: "add", Lease: control.LeaseRequest{WorkDir: cfg.WorkDir, Root: *root, Name: *name, Host: *host, AutoSuffix: *autoSuffix, Reset: *reset, Env: cfg.Env}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, cfg.Stderr, *resp.Lease, *jsonOut, *shell)
	return 0
}

func runPort(cfg Config) int {
	fs := flag.NewFlagSet("port", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	name := fs.String("name", "port", "")
	jsonOut := fs.Bool("json", false, "")
	shell := fs.Bool("shell", false, "")
	if !parseFlags(cfg, fs, "port") {
		return 2
	}
	if fs.NArg() != 0 {
		if fs.Arg(0) == "release" {
			fmt.Fprintln(cfg.Stderr, `lewp port: "port release" was removed; use: lewp release --port [<name>]`)
		} else {
			fmt.Fprintf(cfg.Stderr, "lewp port: unexpected argument %s\n", fs.Arg(0))
		}
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "port", Port: control.PortRequest{WorkDir: cfg.WorkDir, Name: *name, Env: cfg.Env}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, cfg.Stderr, *resp.Lease, *jsonOut, *shell)
	return 0
}

func runAlias(cfg Config) int {
	if len(cfg.Args) < 2 {
		fmt.Fprint(cfg.Stderr, aliasHelp)
		return 2
	}
	switch cfg.Args[1] {
	case "add":
		return runAliasAdd(cfg)
	case "remove":
		return runAliasRemove(cfg)
	case "list":
		return runAliasList(cfg)
	default:
		fmt.Fprintf(cfg.Stderr, "lewp alias: unknown action %q\nRun: lewp alias --help\n", cfg.Args[1])
		return 2
	}
}

func runAliasAdd(cfg Config) int {
	host, jsonOut, err := parseAliasHostArgs(cfg.Args[2:])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp alias add: %v\n", err)
		fmt.Fprintln(cfg.Stderr, "lewp alias add <host>")
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "alias-add", Alias: control.AliasRequest{WorkDir: cfg.WorkDir, Host: host}})
	if err != nil {
		return daemonError(cfg, err)
	}
	if resp.Lease == nil {
		fmt.Fprintln(cfg.Stderr, "malformed daemon response: missing alias lease")
		return 1
	}
	writeLease(cfg.Stdout, cfg.Stderr, *resp.Lease, jsonOut, false)
	return 0
}

func runAliasRemove(cfg Config) int {
	host, err := parseAliasRemoveArgs(cfg.Args[2:])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp alias remove: %v\n", err)
		fmt.Fprintln(cfg.Stderr, "lewp alias remove <host>")
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "alias-remove", Alias: control.AliasRequest{WorkDir: cfg.WorkDir, Host: host}})
	if err != nil {
		return daemonError(cfg, err)
	}
	if resp.AliasRemove == nil {
		fmt.Fprintln(cfg.Stderr, "malformed daemon response: missing alias remove result")
		return 1
	}
	if resp.AliasRemove.Removed == 0 {
		fmt.Fprintf(cfg.Stdout, "no alias %s for this directory\n", host)
		return 0
	}
	fmt.Fprintf(cfg.Stdout, "removed alias %s\n", host)
	return 0
}

func parseAliasRemoveArgs(args []string) (string, error) {
	var host string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return "", fmt.Errorf("unknown flag %s", arg)
		}
		if host != "" {
			return "", fmt.Errorf("too many arguments")
		}
		host = arg
	}
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	return host, nil
}

func runAliasList(cfg Config) int {
	jsonOut, err := parseAliasListArgs(cfg.Args[2:])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp alias list: %v\n", err)
		fmt.Fprintln(cfg.Stderr, "lewp alias list")
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "alias-list", Alias: control.AliasRequest{WorkDir: cfg.WorkDir}})
	if err != nil {
		return daemonError(cfg, err)
	}
	entries := resp.Entries
	if entries == nil {
		entries = []control.ListEntry{}
	}
	writeRoutes(cfg.Stdout, cfg.Stderr, entries, jsonOut)
	return 0
}

func parseAliasHostArgs(args []string) (string, bool, error) {
	var host string
	var jsonOut bool
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown flag %s", arg)
			}
			if host != "" {
				return "", false, fmt.Errorf("too many arguments")
			}
			host = arg
		}
	}
	if host == "" {
		return "", false, fmt.Errorf("host is required")
	}
	return host, jsonOut, nil
}

func parseAliasListArgs(args []string) (bool, error) {
	var jsonOut bool
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			if strings.HasPrefix(arg, "-") {
				return false, fmt.Errorf("unknown flag %s", arg)
			}
			return false, fmt.Errorf("too many arguments")
		}
	}
	return jsonOut, nil
}

func runInfo(cfg Config) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	jsonOut := fs.Bool("json", false, "")
	shellOut := fs.Bool("shell", false, "")
	portOnly := fs.Bool("port", false, "")
	name := fs.String("name", "", "")
	host := fs.String("host", "", "")
	if !parseFlags(cfg, fs, "info") {
		return 2
	}
	if *jsonOut && *shellOut {
		fmt.Fprintln(cfg.Stderr, "lewp info: --json and --shell cannot be combined")
		return 2
	}
	if *jsonOut && *portOnly {
		fmt.Fprintln(cfg.Stderr, "lewp info: --json and --port cannot be combined")
		return 2
	}
	if *shellOut && *portOnly {
		fmt.Fprintln(cfg.Stderr, "lewp info: --shell and --port cannot be combined")
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "info", Info: control.InfoRequest{WorkDir: cfg.WorkDir}})
	if err != nil {
		return daemonError(cfg, err)
	}
	if len(resp.Entries) == 0 {
		fmt.Fprintln(cfg.Stderr, "no Lewp route or port is registered for this directory")
		fmt.Fprintln(cfg.Stderr, "Run: lewp lease")
		fmt.Fprintln(cfg.Stderr, "Or lease a bare port: lewp port --name <name>")
		fmt.Fprintln(cfg.Stderr, "Or move an existing route here: lewp move --from <path>")
		return 1
	}
	entries := filterInfoEntries(resp.Entries, *name, *host)
	if *jsonOut {
		if entries == nil {
			entries = []control.ListEntry{}
		}
		_ = json.NewEncoder(cfg.Stdout).Encode(entries)
		return 0
	}
	if len(entries) == 0 {
		fmt.Fprintln(cfg.Stderr, "no matching Lewp route or port for this directory")
		return 1
	}
	if *portOnly {
		if len(entries) != 1 {
			fmt.Fprintln(cfg.Stderr, "lewp info --port requires exactly one matching entry; pass --host <host> or --name <name>")
			return 2
		}
		fmt.Fprintf(cfg.Stdout, "%d", entries[0].Port)
		return 0
	}
	if *shellOut {
		if len(entries) != 1 {
			fmt.Fprintln(cfg.Stderr, "lewp info --shell requires exactly one matching entry; pass --host <host> or --name <name>")
			return 2
		}
		writeInfoShell(cfg.Stdout, entries[0])
		return 0
	}
	writeEntriesTable(cfg.Stdout, entries)
	return 0
}

func filterInfoEntries(entries []control.ListEntry, name, host string) []control.ListEntry {
	if name == "" && host == "" {
		return entries
	}
	filtered := make([]control.ListEntry, 0, len(entries))
	for _, entry := range entries {
		if name != "" && entry.Name != name {
			continue
		}
		if host != "" && entry.Host != host {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func runMove(cfg Config) int {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	from := fs.String("from", "", "")
	jsonOut := fs.Bool("json", false, "")
	if !parseFlags(cfg, fs, "move") {
		return 2
	}
	if strings.TrimSpace(*from) == "" {
		fmt.Fprintln(cfg.Stderr, "lewp move: --from <path> is required")
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "move", Move: control.MoveRequest{WorkDir: cfg.WorkDir, From: *from}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeRoutes(cfg.Stdout, cfg.Stderr, resp.Entries, *jsonOut)
	return 0
}

// releaseScope selects what `lewp release` frees for the current directory.
type releaseScope int

const (
	releaseEverything releaseScope = iota // route + aliases + every bare port
	releaseRouteOnly                      // route + aliases, keep bare ports
	releasePortOnly                       // one named bare port
)

// parseReleaseArgs hand-parses `lewp release` flags because --port takes an
// optional name and the stdlib flag package supports optional values only for
// booleans. --all is rejected with pointer text: plain release now covers it.
func parseReleaseArgs(args []string) (scope releaseScope, portName string, forget bool, err error) {
	scope = releaseEverything
	portName = "port"
	sawRoute, sawPort := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--forget":
			forget = true
		case arg == "--route":
			sawRoute = true
		case arg == "--port":
			sawPort = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				portName = args[i]
			}
		case strings.HasPrefix(arg, "--port="):
			sawPort = true
			portName = strings.TrimPrefix(arg, "--port=")
			if portName == "" {
				return 0, "", false, fmt.Errorf("--port= requires a name")
			}
		case arg == "--all":
			return 0, "", false, fmt.Errorf(`--all was removed; plain "lewp release" now frees the route and every bare port`)
		case strings.HasPrefix(arg, "-"):
			return 0, "", false, fmt.Errorf("unknown flag %s", arg)
		default:
			return 0, "", false, fmt.Errorf("unexpected argument %s", arg)
		}
	}
	if sawRoute && sawPort {
		return 0, "", false, fmt.Errorf("--route and --port cannot be combined")
	}
	if sawRoute {
		scope = releaseRouteOnly
	}
	if sawPort {
		scope = releasePortOnly
	}
	return scope, portName, forget, nil
}

func runRelease(cfg Config) int {
	scope, portName, forget, err := parseReleaseArgs(cfg.Args[1:])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp release: %v\n", err)
		fmt.Fprintln(cfg.Stderr, "Run: lewp release --help")
		return 2
	}
	req := control.ReleaseRequest{WorkDir: cfg.WorkDir, Forget: forget, Env: cfg.Env}
	switch scope {
	case releaseEverything:
		req.All = true
	case releasePortOnly:
		req.Kind = identity.KindPort
		req.Name = portName
	}
	resp, err := call(cfg, control.Request{Command: "release", Release: req})
	if err != nil {
		return daemonError(cfg, err)
	}
	var routes, ports int
	if resp.Release != nil {
		routes, ports = resp.Release.Routes, resp.Release.Ports
	}
	switch scope {
	case releasePortOnly:
		if ports == 0 {
			fmt.Fprintf(cfg.Stdout, "no active port named %q for this directory\n", portName)
			return 0
		}
		fmt.Fprintf(cfg.Stdout, "released port %q\n", portName)
	case releaseRouteOnly:
		if routes == 0 {
			fmt.Fprintln(cfg.Stdout, "no active route for this directory")
			return 0
		}
		fmt.Fprintf(cfg.Stdout, "released %d route(s)\n", routes)
	default:
		if routes == 0 && ports == 0 {
			fmt.Fprintln(cfg.Stdout, "no active route or port for this directory")
			return 0
		}
		fmt.Fprintf(cfg.Stdout, "released %d route(s) and %d port(s)\n", routes, ports)
	}
	return 0
}

func runList(cfg Config) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	all := fs.Bool("all", false, "")
	jsonOut := fs.Bool("json", false, "")
	if !parseFlags(cfg, fs, "list") {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "list", All: *all})
	if err != nil {
		return daemonError(cfg, err)
	}
	if *jsonOut {
		// Always emit a JSON array (never null) so `lewp list --json | jq` has a
		// stable shape even when nothing is registered.
		entries := resp.Entries
		if entries == nil {
			entries = []control.ListEntry{}
		}
		_ = json.NewEncoder(cfg.Stdout).Encode(entries)
		return 0
	}
	writeEntriesTable(cfg.Stdout, resp.Entries)
	return 0
}

func writeEntriesTable(stdout io.Writer, entries []control.ListEntry) {
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tNAME\tKIND\tPORT\tSTATE\tPATH")
	for _, entry := range entries {
		host := dashIfEmpty(entry.Host)
		name := dashIfEmpty(entry.Name)
		kind := dashIfEmpty(string(entry.Kind))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", host, name, kind, entry.Port, entry.State, entry.Path)
	}
	_ = tw.Flush()
}

// dashIfEmpty renders an empty column value as "-" so tabular output keeps a
// printable cell in every column (a bare port has no host, an apex route may
// have no instance name).
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// writeLease prints the lease to stdout as env lines (or JSON). Inference notes
// and warnings go to stderr so that `eval "$(lewp lease --shell)"` and any
// `PORT=$(...)` capture only see clean assignable output, never `#` comments or
// conflict warnings.
func writeLease(stdout, stderr io.Writer, lease control.LeaseResponse, jsonOut, shell bool) {
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(lease)
		return
	}
	prefix := ""
	if shell {
		prefix = "export "
	}
	fmt.Fprintf(stdout, "%sPORT=%d\n", prefix, lease.Port)
	if lease.URL != "" {
		fmt.Fprintf(stdout, "%sURL=%s\n", prefix, lease.URL)
	}
	// HTTPS_URL stays beside the HTTP URL in shell, human, and JSON output so a
	// script can pick the scheme it wants; it is only present when the lease has
	// a routable host.
	if lease.HTTPSURL != "" {
		fmt.Fprintf(stdout, "%sHTTPS_URL=%s\n", prefix, lease.HTTPSURL)
	}
	if lease.Host != "" {
		fmt.Fprintf(stdout, "%sHOST=%s\n", prefix, lease.Host)
	}
	// STATE and HOST_KIND are descriptive, human-facing fields. They are omitted
	// under --shell so `eval "$(lewp lease --shell)"` does not export bookkeeping
	// variables into the user's environment; JSON already carries them.
	if !shell {
		if lease.LeaseState != "" {
			fmt.Fprintf(stdout, "STATE=%s\n", lease.LeaseState)
		}
		if lease.HostKind != "" {
			fmt.Fprintf(stdout, "HOST_KIND=%s\n", lease.HostKind)
		}
	}
	if lease.RootSource == "inferred" {
		fmt.Fprintf(stderr, "# inferred root=%s\n", lease.Root)
	}
	if lease.NameSource == "inferred" {
		fmt.Fprintf(stderr, "# inferred name=%s\n", lease.Name)
	}
	for _, warning := range lease.Warnings {
		fmt.Fprintf(stderr, "# %s\n", warning)
	}
	if !shell {
		writeLocalOnlyNote(stderr, lease.Host)
	}
}

// writeLocalOnlyNote prints a stderr reminder that a `.lewp` host never leaves
// the machine. It is emitted only for human (non-shell) output and only to
// stderr so it never lands in a captured `$(...)` value or an eval'd export.
func writeLocalOnlyNote(stderr io.Writer, host string) {
	if isLewpHost(host) {
		fmt.Fprintf(stderr, "# %s is local-only (resolves to 127.0.0.1)\n", host)
	}
}

func isLewpHost(host string) bool {
	return host != "" && strings.HasSuffix(host, ".lewp")
}

// writeRoutes prints registry-backed route entries in the same env-style
// format as writeLease, one block per route.
func writeRoutes(stdout, stderr io.Writer, entries []control.ListEntry, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(entries)
		return
	}
	var host string
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "PORT=%d\n", entry.Port)
		if entry.Host != "" {
			host = entry.Host
			fmt.Fprintf(stdout, "URL=http://%s\n", entry.Host)
			fmt.Fprintf(stdout, "HTTPS_URL=https://%s\n", entry.Host)
			fmt.Fprintf(stdout, "HOST=%s\n", entry.Host)
		}
		fmt.Fprintf(stdout, "DIR=%s\n", entry.Path)
	}
	writeLocalOnlyNote(stderr, host)
}

func writeInfoShell(w io.Writer, entry control.ListEntry) {
	fmt.Fprintf(w, "export PORT=%d\n", entry.Port)
	if entry.Host != "" {
		fmt.Fprintf(w, "export URL=http://%s\n", entry.Host)
		fmt.Fprintf(w, "export HTTPS_URL=https://%s\n", entry.Host)
		fmt.Fprintf(w, "export HOST=%s\n", entry.Host)
	}
}
