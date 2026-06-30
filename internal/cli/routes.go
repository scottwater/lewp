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

func runAdd(cfg Config) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	root := fs.String("root", "", "")
	name := fs.String("name", "", "")
	host := fs.String("host", "", "")
	jsonOut := fs.Bool("json", false, "")
	shell := fs.Bool("shell", false, "")
	autoSuffix := fs.Bool("auto-suffix", false, "")
	if !parseFlags(cfg, fs, "add") {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "add", Lease: control.LeaseRequest{WorkDir: cfg.WorkDir, Root: *root, Name: *name, Host: *host, AutoSuffix: *autoSuffix, Env: cfg.Env}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, cfg.Stderr, *resp.Lease, *jsonOut, *shell)
	return 0
}

func runPort(cfg Config) int {
	if len(cfg.Args) >= 2 && cfg.Args[1] == "release" {
		return runPortRelease(cfg)
	}
	fs := flag.NewFlagSet("port", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	name := fs.String("name", "port", "")
	jsonOut := fs.Bool("json", false, "")
	shell := fs.Bool("shell", false, "")
	if !parseFlags(cfg, fs, "port") {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "port", Port: control.PortRequest{WorkDir: cfg.WorkDir, Name: *name, Env: cfg.Env}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, cfg.Stderr, *resp.Lease, *jsonOut, *shell)
	return 0
}

// runPortRelease releases a single bare port lease for the current directory.
// Releasing nothing is reported but not treated as an error: release is
// idempotent.
func runPortRelease(cfg Config) int {
	fs := flag.NewFlagSet("port release", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	name := fs.String("name", "port", "")
	forget := fs.Bool("forget", false, "")
	if fs.Parse(cfg.Args[2:]) != nil {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "release", Release: control.ReleaseRequest{WorkDir: cfg.WorkDir, Name: *name, Kind: identity.KindPort, Forget: *forget, Env: cfg.Env}})
	if err != nil {
		return daemonError(cfg, err)
	}
	if resp.Release == nil || resp.Release.Ports == 0 {
		fmt.Fprintf(cfg.Stdout, "no active port named %q for this directory\n", *name)
		return 0
	}
	fmt.Fprintf(cfg.Stdout, "released port %q\n", *name)
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
	if !parseFlags(cfg, fs, "info") {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "info", Info: control.InfoRequest{WorkDir: cfg.WorkDir}})
	if err != nil {
		return daemonError(cfg, err)
	}
	if len(resp.Entries) == 0 {
		fmt.Fprintln(cfg.Stderr, "no Lewp route or port is registered for this directory")
		fmt.Fprintln(cfg.Stderr, "Run: lewp add")
		fmt.Fprintln(cfg.Stderr, "Or lease a bare port: lewp port --name <name>")
		fmt.Fprintln(cfg.Stderr, "Or move an existing route here: lewp move --from <path>")
		return 1
	}
	writeInfo(cfg.Stdout, cfg.Stderr, resp.Entries, *jsonOut)
	return 0
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

func runRelease(cfg Config) int {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	all := fs.Bool("all", false, "")
	forget := fs.Bool("forget", false, "")
	if !parseFlags(cfg, fs, "release") {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "release", Release: control.ReleaseRequest{WorkDir: cfg.WorkDir, Forget: *forget, All: *all, Env: cfg.Env}})
	if err != nil {
		return daemonError(cfg, err)
	}
	var routes, ports int
	if resp.Release != nil {
		routes, ports = resp.Release.Routes, resp.Release.Ports
	}
	if *all {
		if routes == 0 && ports == 0 {
			fmt.Fprintln(cfg.Stdout, "no active route or port for this directory")
			return 0
		}
		fmt.Fprintf(cfg.Stdout, "released %d route(s) and %d port(s)\n", routes, ports)
		return 0
	}
	if routes == 0 {
		fmt.Fprintln(cfg.Stdout, "no active route for this directory")
		return 0
	}
	fmt.Fprintf(cfg.Stdout, "released %d route(s)\n", routes)
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
	tw := tabwriter.NewWriter(cfg.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tNAME\tKIND\tPORT\tSTATE\tPATH")
	for _, entry := range resp.Entries {
		host := dashIfEmpty(entry.Host)
		name := dashIfEmpty(entry.Name)
		kind := dashIfEmpty(string(entry.Kind))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", host, name, kind, entry.Port, entry.State, entry.Path)
	}
	_ = tw.Flush()
	return 0
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
// and warnings go to stderr so that `eval "$(lewp add --shell)"` and any
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
	// under --shell so `eval "$(lewp add --shell)"` does not export bookkeeping
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

func writeInfo(stdout, stderr io.Writer, entries []control.ListEntry, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(stdout).Encode(entries)
		return
	}
	routes := make([]control.ListEntry, 0, len(entries))
	aliases := make([]control.ListEntry, 0, len(entries))
	ports := make([]control.ListEntry, 0, len(entries))
	for _, entry := range entries {
		switch entry.Kind {
		case identity.KindRoute:
			routes = append(routes, entry)
		case control.KindAlias:
			aliases = append(aliases, entry)
		case identity.KindPort:
			ports = append(ports, entry)
		}
	}
	var host string
	if len(routes) > 0 {
		fmt.Fprintln(stdout, "ROUTES")
		host = writeInfoRoutes(stdout, routes)
	}
	if len(routes) > 0 && len(aliases) > 0 {
		fmt.Fprintln(stdout)
	}
	if len(aliases) > 0 {
		fmt.Fprintln(stdout, "ALIASES")
		host = writeInfoRoutes(stdout, aliases)
	}
	if (len(routes) > 0 || len(aliases) > 0) && len(ports) > 0 {
		fmt.Fprintln(stdout)
	}
	if len(ports) > 0 {
		fmt.Fprintln(stdout, "PORTS")
		writeInfoPorts(stdout, ports)
	}
	writeLocalOnlyNote(stderr, host)
}

// writeInfoRoutes prints each route block and returns the last route host seen,
// so the caller can attach a single local-only note.
func writeInfoRoutes(w io.Writer, entries []control.ListEntry) string {
	var host string
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "PORT=%d\n", entry.Port)
		if entry.Host != "" {
			host = entry.Host
			fmt.Fprintf(w, "URL=http://%s\n", entry.Host)
			fmt.Fprintf(w, "HTTPS_URL=https://%s\n", entry.Host)
			fmt.Fprintf(w, "HOST=%s\n", entry.Host)
		}
		fmt.Fprintf(w, "STATE=%s\n", entry.State)
		fmt.Fprintf(w, "DIR=%s\n", entry.Path)
	}
	return host
}

func writeInfoPorts(w io.Writer, entries []control.ListEntry) {
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "NAME=%s\n", entry.Name)
		fmt.Fprintf(w, "PORT=%d\n", entry.Port)
		fmt.Fprintf(w, "STATE=%s\n", entry.State)
		fmt.Fprintf(w, "DIR=%s\n", entry.Path)
	}
}
