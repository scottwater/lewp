package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
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

type releaseOptions struct {
	selectorType registry.ReleaseSelectorType
	path         string
	host         string
	name         string
	port         int
	pathSet      bool
	recursive    bool
	routeOnly    bool
	forget       bool
	dryRun       bool
	jsonOut      bool
	assumeYes    bool
}

// parseReleaseArgs hand-parses release flags so valued long flags can provide
// precise missing-value and numeric-port replacement guidance.
func parseReleaseArgs(args []string) (releaseOptions, error) {
	opts := releaseOptions{selectorType: registry.ReleaseSelectorPath}
	var hostSet, portSet bool
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(flagName string) (string, error) {
			if strings.HasPrefix(arg, flagName+"=") {
				got := strings.TrimPrefix(arg, flagName+"=")
				if got == "" {
					return "", fmt.Errorf("%s requires a value", flagName)
				}
				return got, nil
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", fmt.Errorf("%s requires a value", flagName)
			}
			i++
			if args[i] == "" {
				return "", fmt.Errorf("%s requires a value", flagName)
			}
			return args[i], nil
		}
		switch {
		case arg == "--path" || strings.HasPrefix(arg, "--path="):
			got, err := value("--path")
			if err != nil {
				return releaseOptions{}, err
			}
			opts.path, opts.pathSet = got, true
		case arg == "--host" || strings.HasPrefix(arg, "--host="):
			got, err := value("--host")
			if err != nil {
				return releaseOptions{}, err
			}
			got, err = canonicalReleaseHost(got)
			if err != nil {
				return releaseOptions{}, err
			}
			opts.host, hostSet = got, true
		case arg == "--name" || strings.HasPrefix(arg, "--name="):
			got, err := value("--name")
			if err != nil {
				return releaseOptions{}, err
			}
			if strings.TrimSpace(got) == "" {
				return releaseOptions{}, fmt.Errorf("--name requires a value")
			}
			if _, _, err := identity.NormalizeLabel(got); err != nil {
				return releaseOptions{}, fmt.Errorf("--name must identify a valid port name: %w", err)
			}
			opts.name = got
		case arg == "--port" || strings.HasPrefix(arg, "--port="):
			got, valueErr := value("--port")
			if valueErr != nil {
				return releaseOptions{}, fmt.Errorf("--port now requires a numeric port; use --name <name>")
			}
			port, err := strconv.Atoi(got)
			if err != nil {
				return releaseOptions{}, fmt.Errorf("--port now requires a numeric port; use --name %s", got)
			}
			if port < 1 || port > 65535 {
				return releaseOptions{}, fmt.Errorf("--port must be between 1 and 65535")
			}
			opts.port, portSet = port, true
		case arg == "--recursive":
			opts.recursive = true
		case arg == "--route":
			opts.routeOnly = true
		case arg == "--forget":
			opts.forget = true
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--json":
			opts.jsonOut = true
		case arg == "-y" || arg == "--yes":
			opts.assumeYes = true
		case arg == "--all":
			return releaseOptions{}, fmt.Errorf(`--all was removed; plain "lewp release" now frees the route and every bare port`)
		case strings.HasPrefix(arg, "-"):
			return releaseOptions{}, fmt.Errorf("unknown flag %s", arg)
		default:
			return releaseOptions{}, fmt.Errorf("unexpected argument %s", arg)
		}
	}

	selectors := 0
	for _, set := range []bool{opts.pathSet, hostSet, portSet} {
		if set {
			selectors++
		}
	}
	if selectors > 1 {
		return releaseOptions{}, fmt.Errorf("only one of --path, --host, and --port may be used")
	}
	if (hostSet || portSet) && (opts.routeOnly || opts.name != "") {
		return releaseOptions{}, fmt.Errorf("--route and --name require a path selector")
	}
	if opts.routeOnly && opts.name != "" {
		return releaseOptions{}, fmt.Errorf("--route and --name cannot be combined")
	}
	if opts.recursive && !opts.pathSet {
		return releaseOptions{}, fmt.Errorf("--recursive requires an explicit --path")
	}
	if hostSet {
		opts.selectorType = registry.ReleaseSelectorHost
	}
	if portSet {
		opts.selectorType = registry.ReleaseSelectorPort
	}
	return opts, nil
}

// canonicalReleaseHost performs the CLI's one normalization pass and then
// validates generic DNS syntax. Suffix membership is intentionally left to the
// daemon because configured custom suffixes are daemon-owned state.
func canonicalReleaseHost(input string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(input))
	if host == "" {
		return "", fmt.Errorf("--host requires a value")
	}
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	invalid := func() (string, error) {
		return "", fmt.Errorf("--host must be a valid DNS host or leading wildcard pattern")
	}
	if host == "" || strings.HasSuffix(host, ".") || len(host) > 253 {
		return invalid()
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if label == "*" {
			if i != 0 || len(labels) == 1 {
				return invalid()
			}
			continue
		}
		if label == "" || len(label) > 63 || strings.Contains(label, "*") || !isDNSAlphaNumeric(label[0]) || !isDNSAlphaNumeric(label[len(label)-1]) {
			return invalid()
		}
		for j := 1; j < len(label)-1; j++ {
			if !isDNSAlphaNumeric(label[j]) && label[j] != '-' {
				return invalid()
			}
		}
	}
	return host, nil
}

func isDNSAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}

func releaseRequest(cfg Config, opts releaseOptions) control.ReleaseRequest {
	req := control.ReleaseRequest{WorkDir: cfg.WorkDir, Forget: opts.forget, DryRun: opts.dryRun}
	switch opts.selectorType {
	case registry.ReleaseSelectorHost:
		req.Host = opts.host
	case registry.ReleaseSelectorPort:
		req.Port = opts.port
	default:
		if opts.pathSet {
			req.Path = opts.path
		} else {
			req.Implicit = true
		}
		req.Recursive = opts.recursive
		req.Scope = registry.ReleaseScopeAll
		if opts.routeOnly {
			req.Scope = registry.ReleaseScopeRoute
		}
		if opts.name != "" {
			req.Scope = registry.ReleaseScopeName
			req.Name = opts.name
		}
	}
	return req
}

func runRelease(cfg Config) int {
	opts, err := parseReleaseArgs(cfg.Args[1:])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp release: %v\n", err)
		fmt.Fprintln(cfg.Stderr, "Run: lewp release --help")
		return 2
	}
	return executeRelease(cfg, opts, isTerminal(cfg.Stdin))
}

// executeRelease separates terminal detection from the release workflow so the
// interactive confirmation path can be exercised deterministically. Recursive
// mutations require either an affirmative terminal confirmation or --yes;
// exact selectors retain their unprompted plan/apply behavior.
func executeRelease(cfg Config, opts releaseOptions, interactive bool) int {
	if opts.recursive && !opts.dryRun && !opts.assumeYes && (opts.jsonOut || !interactive) {
		return releaseConfirmationRequired(cfg)
	}

	planned, err := call(cfg, control.Request{Command: "release-plan", Release: releaseRequest(cfg, opts)})
	if err != nil {
		return daemonError(cfg, err)
	}
	if planned.Release == nil || planned.ReleasePlan == nil {
		fmt.Fprintln(cfg.Stderr, "malformed daemon response: missing release result or plan")
		return 1
	}
	if opts.dryRun {
		result := *planned.Release
		result.DryRun = true
		writeReleaseResult(cfg.Stdout, result, opts.jsonOut, "")
		return 0
	}

	if opts.recursive {
		// There is no destructive operation to confirm or send when the plan is
		// empty. Render the normal no-op response and leave the private plan unused.
		if len(planned.Release.Items) == 0 {
			writeReleaseResult(cfg.Stdout, *planned.Release, opts.jsonOut, "")
			return 0
		}
		if !opts.jsonOut {
			writeRecursiveReleasePreview(cfg.Stdout, *planned.Release)
			if !confirmRelease(cfg, opts.assumeYes, interactive) {
				return 1
			}
		}
	}

	// Apply the exact private plan that produced the preview. In particular, do
	// not rebuild a stale recursive plan after the user has approved its output.
	applied, err := call(cfg, control.Request{Command: "release-apply", ReleasePlan: planned.ReleasePlan})
	if err != nil {
		return daemonError(cfg, err)
	}
	if applied.Release == nil {
		fmt.Fprintln(cfg.Stderr, "malformed daemon response: missing release result")
		return 1
	}
	result := *applied.Release
	// The private plan intentionally has no CLI-origin metadata. Preserve the
	// planned public selector so implicit current-directory output remains exact.
	result.Selector = planned.Release.Selector
	writeReleaseResult(cfg.Stdout, result, opts.jsonOut, "")
	return 0
}

func releaseConfirmationRequired(cfg Config) int {
	fmt.Fprintln(cfg.Stderr, "release: refusing recursive mutation without confirmation")
	fmt.Fprintln(cfg.Stderr, "Re-run with --yes to proceed (required for non-interactive and JSON use)")
	return 1
}

// confirmRelease gates a recursive mutation after its complete human preview.
// Only an explicit yes is affirmative; no, blank input, and EOF all abort.
func confirmRelease(cfg Config, assumeYes, interactive bool) bool {
	if assumeYes {
		return true
	}
	if !interactive {
		releaseConfirmationRequired(cfg)
		return false
	}
	fmt.Fprint(cfg.Stdout, "Proceed? [y/N] ")
	line, _ := bufio.NewReader(cfg.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		fmt.Fprintln(cfg.Stdout, "release aborted")
		return false
	}
}

func writeRecursiveReleasePreview(w io.Writer, result control.ReleaseResponse) {
	writeReleaseTable(w, result.Items)
	fmt.Fprintf(w, "Planned releases: %d\nPlanned forgets: %d\n", result.Released, result.Forgotten)
}

func writeReleaseResult(w io.Writer, result control.ReleaseResponse, jsonOut bool, _ string) {
	if result.Items == nil {
		result.Items = []control.ReleaseItem{}
	}
	if jsonOut {
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if len(result.Items) == 0 {
		fmt.Fprintln(w, releaseNoMatch(result.Selector))
	} else if len(result.Items) == 1 {
		writeSingleReleaseResult(w, result.Items[0], result.DryRun)
	} else {
		writeReleaseTable(w, result.Items)
	}
	if result.DryRun {
		fmt.Fprintf(w, "Planned releases: %d\nPlanned forgets: %d\n", result.Released, result.Forgotten)
	} else {
		fmt.Fprintf(w, "Released: %d\nForgotten: %d\n", result.Released, result.Forgotten)
	}
}

func releaseNoMatch(selector control.ReleaseSelector) string {
	if selector.Type == registry.ReleaseSelectorHost && selector.Host != nil {
		return fmt.Sprintf("no active allocation for host %q", *selector.Host)
	}
	if selector.Type == registry.ReleaseSelectorPort && selector.Port != nil {
		return fmt.Sprintf("no active allocation on port %d", *selector.Port)
	}
	if selector.Name != nil {
		return fmt.Sprintf("no active port named %q for this path", *selector.Name)
	}
	if selector.Scope != nil && *selector.Scope == registry.ReleaseScopeRoute {
		if selector.Implicit != nil && *selector.Implicit {
			return "no active route for this directory"
		}
		return "no matching route for this path"
	}
	if selector.Implicit != nil && *selector.Implicit {
		return "no active route or port for this directory"
	}
	return "no matching route or port for this path"
}

func writeSingleReleaseResult(w io.Writer, item control.ReleaseItem, dryRun bool) {
	verbs := make([]string, 0, len(item.Actions))
	for _, action := range item.Actions {
		verb := string(action)
		if dryRun {
			verb = "would " + verb
		} else if action == registry.ReleaseActionRelease {
			verb = "released"
		} else {
			verb = "forgotten"
		}
		verbs = append(verbs, verb)
	}
	verb := strings.Join(verbs, " and ")
	if item.Kind == identity.KindRoute {
		fmt.Fprintf(w, "%s route %q at %s\n", verb, item.Host, item.Path)
	} else {
		fmt.Fprintf(w, "%s port %q at %s\n", verb, item.Name, item.Path)
	}
}

func writeReleaseTable(w io.Writer, items []control.ReleaseItem) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tIDENTITY\tPORT\tSTATE\tACTIONS\tPATH")
	for _, item := range items {
		identityText := item.Name
		if item.Kind == identity.KindRoute {
			hosts := make([]string, 0, len(item.Hosts))
			for _, host := range item.Hosts {
				hosts = append(hosts, host.Host)
			}
			identityText = strings.Join(hosts, ",")
			if identityText == "" {
				identityText = item.Host
			}
		}
		port := "-"
		if item.Port != nil {
			port = strconv.Itoa(*item.Port)
		}
		actions := make([]string, 0, len(item.Actions))
		for _, action := range item.Actions {
			actions = append(actions, string(action))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", item.Kind, identityText, port, item.State, strings.Join(actions, ","), item.Path)
	}
	_ = tw.Flush()
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
