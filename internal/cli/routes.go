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
	writeInfo(cfg.Stdout, resp.Entries, *jsonOut)
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
	writeRoutes(cfg.Stdout, resp.Entries, *jsonOut)
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
	if !parseFlags(cfg, fs, "list") {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "list", All: *all})
	if err != nil {
		return daemonError(cfg, err)
	}
	tw := tabwriter.NewWriter(cfg.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tPORT\tSTATE\tPATH")
	for _, entry := range resp.Entries {
		host := entry.Host
		if host == "" {
			host = "-"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", host, entry.Port, entry.State, entry.Path)
	}
	_ = tw.Flush()
	return 0
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
	if lease.Host != "" {
		fmt.Fprintf(stdout, "%sHOST=%s\n", prefix, lease.Host)
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
}

// writeRoutes prints registry-backed route entries in the same env-style
// format as writeLease, one block per route.
func writeRoutes(w io.Writer, entries []control.ListEntry, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(w).Encode(entries)
		return
	}
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "PORT=%d\n", entry.Port)
		if entry.Host != "" {
			fmt.Fprintf(w, "URL=http://%s\n", entry.Host)
			fmt.Fprintf(w, "HOST=%s\n", entry.Host)
		}
		fmt.Fprintf(w, "PATH=%s\n", entry.Path)
	}
}

func writeInfo(w io.Writer, entries []control.ListEntry, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(w).Encode(entries)
		return
	}
	routes := make([]control.ListEntry, 0, len(entries))
	ports := make([]control.ListEntry, 0, len(entries))
	for _, entry := range entries {
		switch entry.Kind {
		case identity.KindRoute:
			routes = append(routes, entry)
		case identity.KindPort:
			ports = append(ports, entry)
		}
	}
	if len(routes) > 0 {
		fmt.Fprintln(w, "ROUTES")
		writeInfoRoutes(w, routes)
	}
	if len(routes) > 0 && len(ports) > 0 {
		fmt.Fprintln(w)
	}
	if len(ports) > 0 {
		fmt.Fprintln(w, "PORTS")
		writeInfoPorts(w, ports)
	}
}

func writeInfoRoutes(w io.Writer, entries []control.ListEntry) {
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "PORT=%d\n", entry.Port)
		if entry.Host != "" {
			fmt.Fprintf(w, "URL=http://%s\n", entry.Host)
			fmt.Fprintf(w, "HOST=%s\n", entry.Host)
		}
		fmt.Fprintf(w, "STATE=%s\n", entry.State)
		fmt.Fprintf(w, "PATH=%s\n", entry.Path)
	}
}

func writeInfoPorts(w io.Writer, entries []control.ListEntry) {
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "NAME=%s\n", entry.Name)
		fmt.Fprintf(w, "PORT=%d\n", entry.Port)
		fmt.Fprintf(w, "STATE=%s\n", entry.State)
		fmt.Fprintf(w, "PATH=%s\n", entry.Path)
	}
}
