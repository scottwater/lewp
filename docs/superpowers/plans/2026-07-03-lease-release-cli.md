# Lease/Release CLI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename `lewp add` to `lewp lease`, make bare `lewp release` free everything a directory holds (route + aliases + bare ports), scope partial release with `--port [name]` / `--route`, and remove `lewp port release` and `lewp release --all`.

**Architecture:** Pure CLI-layer change. The control protocol already supports all three release scopes (`ReleaseRequest.All`, `Kind: identity.KindPort` + `Name`, and default route scope — see `internal/control/service.go:257`), so only the CLI command table, flag parsing, help text, completions, user-facing strings, and docs change. The wire command names (`"add"`, `"release"`) stay unchanged so a new CLI still talks to an older daemon.

**Tech Stack:** Go stdlib only (`flag`, hand-rolled arg loops as in `parseAliasHostArgs`). Tests use the existing in-process daemon harness (`startTestDaemon` in `internal/cli/route_commands_test.go`).

**Spec:** `docs/superpowers/specs/2026-07-02-lease-release-cli-design.md`

## Global Constraints

- Wire protocol unchanged: CLI keeps sending `control.Request{Command: "add"}` and `{Command: "release"}`; `ReleaseRequest` fields unchanged. Only CLI-visible names change.
- No back-compat alias: `lewp add` becomes an unknown command; `lewp port release` and `lewp release --all` get targeted removal errors (not silent aliases).
- Release stays idempotent: no-op releases report on stdout and exit `0`.
- Never use `rm` in any shell step; use `trash` if a file must be deleted.
- Verify with `go build ./... && go test ./...` before every commit; tests must pass.

---

### Task 1: Rename `add` → `lease`

**Files:**
- Modify: `internal/cli/cli.go:94-97` (comment), `internal/cli/cli.go:183-188` (dispatch)
- Modify: `internal/cli/routes.go:15-34` (runAdd), `internal/cli/routes.go:243`, comments at `:398`, `:424`
- Modify: `internal/cli/help.go` (mainHelp lines 26/43-44/47, addHelp block lines 111-152)
- Modify: `internal/cli/completion.go` (line 15 command entry; bash line 76; zsh line 106; fish lines 132-138)
- Modify: `internal/cli/config.go:70`, `internal/cli/doctor.go:412,424,458,488,508`
- Modify: `internal/control/service.go:59-60`, `internal/control/service_alias.go:40,89`
- Modify: `internal/proxy/proxy.go:308`
- Test: `internal/cli/route_commands_test.go`, `internal/cli/cli_test.go`, `internal/proxy/proxy_test.go:156`

**Interfaces:**
- Consumes: existing `runAdd(cfg Config) int` in `internal/cli/routes.go:15`.
- Produces: `runLease(cfg Config) int` (same body, renamed) dispatched from `case "lease":` in `cli.go`; help const `leaseHelp` replacing `addHelp`. Task 2 assumes these names exist.

- [ ] **Step 1: Write the failing test**

In `internal/cli/route_commands_test.go`, rename `TestRunAddAndInfoMoveCommandHelp` (line 55) and change its `"add"` case:

```go
func TestRunLeaseAndInfoMoveCommandHelp(t *testing.T) {
	cases := map[string]string{
		"lease": "lewp lease",
		"info":  "lewp info",
		"move":  "--from",
	}
```

In `internal/cli/cli_test.go`, replace `TestRunLeaseCommandIsRemoved` (lines ~1801-1813) with its inverse:

```go
func TestRunAddCommandIsRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"add"}, Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "unknown command \"add\"") || !strings.Contains(got, "lewp lease") {
		t.Fatalf("stderr missing rename guidance: %q", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunLeaseAndInfoMoveCommandHelp|TestRunAddCommandIsRemoved' -v`
Expected: FAIL — `lease --help` exits 2 (unknown command), and `add` still resolves so the removed-command test fails.

- [ ] **Step 3: Rename the command in source**

`internal/cli/cli.go` — dispatch (lines 183-188):

```go
	case "lease":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, leaseHelp)
			return 0
		}
		return runLease(cfg)
```

Also update the comment at `cli.go:94-97`: change `` `lewp add` dies with `root "/" is unusable` `` to `` `lewp lease` dies with `root "/" is unusable` ``.

`internal/cli/routes.go` — rename the handler (line 15). The wire command stays `"add"`:

```go
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
```

Also in `routes.go`: line 243 `"Run: lewp add"` → `"Run: lewp lease"`; comments at lines 398 and 424 `lewp add --shell` → `lewp lease --shell`.

- [ ] **Step 4: Update help text**

`internal/cli/help.go`, `mainHelp`: line 26 becomes

```
  lease      Lease a stable port and managed hostname for the current directory
```

lines 43-44 become

```
  cd ~/projects/atlas/feature-1 && lewp lease
  eval "$(lewp lease --shell)" && PORT=$PORT bin/dev
```

line 47 becomes

```
  flags             --root, --name, --host on lewp lease
```

Rename the `addHelp` const to `leaseHelp` and update its text — header line, Usage line, the remembered-override paragraph, and Examples all switch `add` → `lease`:

```go
	leaseHelp = `lewp lease — lease a stable port and host for this directory

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
default with the conflicting path and cleanup guidance. An inferred host that
conflicts is given a stable deterministic suffix automatically.

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
`
```

- [ ] **Step 5: Update completions and remaining user-facing strings**

`internal/cli/completion.go`:
- line 15: `{"add", "Register a stable port and .lewp hostname for this directory"},` → `{"lease", "Lease a stable port and .lewp hostname for this directory"},`
- bash (line 76): `add)` → `lease)` (same flag list)
- zsh (line 106): `add) _values 'add options' ...` → `lease) _values 'lease options' ...` (same flags)
- fish (lines 132-138): all six `__fish_seen_subcommand_from add` → `__fish_seen_subcommand_from lease`

Other strings:
- `internal/cli/config.go:70`: `# Keys: root, name, host. See: lewp lease --help`
- `internal/cli/doctor.go:458`: `Run:    "lewp lease",`
- `internal/cli/doctor.go:508`: `...; lewp lease here would use a -<suffix> host`
- `internal/cli/doctor.go` comments at 412, 424, 488: `lewp add` → `lewp lease`
- `internal/control/service.go:59-60`:
  ```
  "Use another:    lewp lease --host <name>.lewp\n"+
  "Suffix anyway:  lewp lease --host %s --auto-suffix",
  ```
- `internal/control/service_alias.go:40` and `:89`: `"no active route for this directory\nRun: lewp lease"`
- `internal/proxy/proxy.go:308`: `writeCommandBlock(w, "lewp lease")`

- [ ] **Step 6: Update remaining tests mechanically**

Rewrite `Args: []string{"add", ...}` call sites (this pattern cannot match `{"alias", "add", ...}`, which must stay):

```bash
perl -pi -e 's/\[\]string\{"add"/[]string{"lease"/g' internal/cli/*_test.go
```

Then hand-fix the string assertions:
- `internal/cli/cli_test.go:1757`: in the main-help wants list, `"add"` → `"lease"`
- `internal/cli/cli_test.go:1817`: map key `"add": "--shell"` → `"lease": "--shell"` (this test asserts help contains `"lewp "+cmd`, so it now checks `lewp lease`)
- `internal/cli/cli_test.go:756` comment: `lewp add` → `lewp lease`
- `internal/proxy/proxy_test.go:156`: `"lewp add"` → `"lewp lease"`

- [ ] **Step 7: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS everywhere. If a test still references the old verb, the failure output names it — fix it the same way as Step 6 and re-run.

- [ ] **Step 8: Commit**

```bash
git add -A internal/
git commit -m "feat(cli): rename add to lease

lease/release are now a true verb pair, and the CLI verb matches the
lease vocabulary the docs, JSON fields, and registry already use. The
wire command stays \"add\" for daemon compatibility. No alias is kept.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `release` frees everything; `--port`/`--route` scopes; remove `port release`

**Files:**
- Modify: `internal/cli/routes.go` (delete `runPortRelease` :56-77 and the release branch in `runPort` :37-39; rewrite `runRelease` :317-347; add `parseReleaseArgs`)
- Modify: `internal/cli/help.go` (mainHelp line 32, `portHelp`, `releaseHelp`)
- Modify: `internal/cli/completion.go` (release/port entries in all three shells)
- Test: `internal/cli/route_commands_test.go`

**Interfaces:**
- Consumes: `runLease` naming from Task 1; existing `control.ReleaseRequest{WorkDir, Name, Kind, Forget, All, Env}` and `identity.KindPort` (`internal/control/service.go:71-83`) — no control-layer changes.
- Produces: `parseReleaseArgs(args []string) (scope releaseScope, portName string, forget bool, err error)` with `releaseEverything | releaseRouteOnly | releasePortOnly`; rewritten `runRelease`. `runPort` rejects positional args.

- [ ] **Step 1: Write the failing tests**

In `internal/cli/route_commands_test.go`, replace `TestRunReleaseAllReleasesRouteAndPorts` (lines ~96-127) and `TestRunPortReleaseByName` (lines ~129-160) with:

```go
// TestRunReleaseFreesEverythingByDefault proves plain `lewp release` frees the
// route and every bare port for the directory in one call.
func TestRunReleaseFreesEverythingByDefault(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("lease code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("release code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "released 1 route(s) and 1 port(s)") {
		t.Fatalf("release output unexpected: %q", stdout.String())
	}

	// Nothing remains for the directory after a full release.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 1 {
		t.Fatalf("info after release code=%d stdout=%q", code, stdout.String())
	}

	// Releasing again is idempotent and reports the no-op without erroring.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("idempotent release code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no active route or port for this directory") {
		t.Fatalf("idempotent release output unexpected: %q", stdout.String())
	}
}

// TestRunReleasePortByName proves `lewp release --port <name>` frees a single
// bare port, leaves the route alone, and is idempotent.
func TestRunReleasePortByName(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("lease code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release", "--port", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("release --port code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `released port "vite"`) {
		t.Fatalf("release --port output unexpected: %q", stdout.String())
	}

	// The route survives a port-scoped release.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info after release --port code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "app.work.lewp") {
		t.Fatalf("route missing after port-scoped release: %q", stdout.String())
	}

	// Releasing the same name again is an idempotent no-op.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release", "--port", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("idempotent release --port code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `no active port named "vite"`) {
		t.Fatalf("idempotent release --port output unexpected: %q", stdout.String())
	}
}
```

Append these new tests after them:

```go
// TestRunReleaseRouteOnlyKeepsPorts proves `lewp release --route` frees the
// route but leaves bare ports leased.
func TestRunReleaseRouteOnlyKeepsPorts(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("lease code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release", "--route"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("release --route code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "released 1 route(s)") {
		t.Fatalf("release --route output unexpected: %q", stdout.String())
	}

	// The bare port survives.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info after release --route code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "vite") {
		t.Fatalf("bare port missing after route-scoped release: %q", stdout.String())
	}
}

// TestRunReleaseFlagValidation covers scope conflicts and the removed --all and
// port release affordances, each with pointer text to the replacement.
func TestRunReleaseFlagValidation(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"release", "--route", "--port", "vite"}, "cannot be combined"},
		{[]string{"release", "--all"}, "--all was removed"},
		{[]string{"release", "--bogus"}, "unknown flag"},
		{[]string{"release", "extra"}, "unexpected argument"},
		{[]string{"port", "release"}, "lewp release --port"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: tc.args, WorkDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr})
		if code != 2 {
			t.Fatalf("%v: code=%d stderr=%q", tc.args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), tc.want) {
			t.Fatalf("%v: stderr missing %q: %q", tc.args, tc.want, stderr.String())
		}
	}
}

// TestRunReleasePortDefaultName proves a bare `--port` targets the default
// "port" lease, mirroring `lewp port` with no --name.
func TestRunReleasePortDefaultName(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"port"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release", "--port"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("release --port code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `released port "port"`) {
		t.Fatalf("release --port output unexpected: %q", stdout.String())
	}
}
```

Update `TestPortAndReleaseHelpDocumentReleaseAffordances` (lines ~73-95) to guard the new affordances instead:

```go
// TestPortAndReleaseHelpDocumentReleaseAffordances guards that the help surfaces
// the release scope flags and the default bare-port name, so partial release
// stays discoverable now that plain release frees everything.
func TestPortAndReleaseHelpDocumentReleaseAffordances(t *testing.T) {
	cases := map[string][]string{
		"port":    {"lewp release --port", `default "port"`},
		"release": {"--route", "--port", "--forget"},
	}
```

(body of the loop unchanged)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunRelease|TestPortAndReleaseHelp' -v`
Expected: FAIL — plain `release` reports "no active route" without freeing ports, `--port`/`--route` are unknown flags, `port release` still succeeds, and the help lacks the new flags.

- [ ] **Step 3: Rewrite release parsing and dispatch in `internal/cli/routes.go`**

Delete `runPortRelease` (lines 56-77) and the release branch at the top of `runPort` (lines 37-39). Replace `runRelease` (lines 317-347) with:

```go
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
```

In `runPort`, after `parseFlags` succeeds, reject positional arguments — without this, `lewp port release` would silently LEASE a port named "port" (flag parsing stops at the first non-flag token):

```go
	if fs.NArg() != 0 {
		if fs.Arg(0) == "release" {
			fmt.Fprintln(cfg.Stderr, `lewp port: "port release" was removed; use: lewp release --port [<name>]`)
		} else {
			fmt.Fprintf(cfg.Stderr, "lewp port: unexpected argument %s\n", fs.Arg(0))
		}
		return 2
	}
```

`identity` is already imported in routes.go (used by the deleted `runPortRelease`); confirm the import survives the deletion since `runRelease` now uses `identity.KindPort`.

- [ ] **Step 4: Update help text in `internal/cli/help.go`**

`mainHelp` line 32:

```
  release    Release the route and bare ports for the current directory
```

Replace `releaseHelp`:

```go
	releaseHelp = `lewp release — release everything Lewp holds for this directory

By default release frees the whole directory: the route (with its aliases and
wildcard hosts) and every bare port. It is the inverse of "lewp lease" plus any
"lewp port" leases. Release is idempotent: releasing when nothing is active is
reported, never an error.

Usage:
  lewp release [--route | --port [<name>]] [--forget]

Flags:
  --route          Release only the route (and its aliases), keeping bare ports
  --port [<name>]  Release only one bare port (default name "port")
  --forget         Also remove remembered identity and history for what was
                   released

Examples:
  lewp release
  lewp release --route
  lewp release --port vite
  lewp release --forget
`
```

In `portHelp`, remove the `lewp port release` usage line and the whole `Subcommand:` block, and point at the new spelling. Replace the const with:

```go
	portHelp = `lewp port — lease a bare internal port without a hostname

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

To free a bare port, use "lewp release --port <name>" (plain "lewp release"
frees the route and every bare port at once).

Examples:
  lewp port --name vite
  eval "$(lewp port --name vite --shell)"
  VITE_RUBY_PORT="$(lewp port --name vite --json | jq -r .port)"
  lewp release --port vite
`
```

- [ ] **Step 5: Update completions in `internal/cli/completion.go`**

- line 21 command description: `{"release", "Release the route and bare ports for this directory"},`
- bash: change the `port)` case to drop the subcommand and its flag, and add a `release)` case:
  ```
      port)   COMPREPLY=( $(compgen -W "--name --json --shell --help" -- "$cur") ) ;;
      release) COMPREPLY=( $(compgen -W "--route --port --forget --help" -- "$cur") ) ;;
  ```
- zsh: same substitution:
  ```
        port) _values 'port options' --name --json --shell --help ;;
        release) _values 'release options' --route --port --forget --help ;;
  ```
- fish: delete the two `port` lines for `-a release` and `-l forget` (lines 146 and 150), keep `-l name/-l json/-l shell`, and add:
  ```go
  	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l route -d 'Release only the route'\n")
  	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l port -d 'Release one bare port'\n")
  	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l forget -d 'Forget remembered identity'\n")
  ```
- `internal/cli/completion_test.go`: three `wants` entries reference removed text — the bash want `"--shell --forget"` (came from the old port case) becomes `"--route --port --forget"`, the zsh want `"port subcommand/options"` becomes `"port options"`, and the fish want `"Release a bare port lease"` becomes `"Release one bare port"`.

- [ ] **Step 6: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: PASS. Watch for stragglers that still call `port release` or `release --all` (Step 1 replaced the known ones); fix any the failures name.

- [ ] **Step 7: Commit**

```bash
git add -A internal/
git commit -m "feat(cli): release frees everything; scope with --route/--port

Plain lewp release now frees the route, its aliases, and every bare
port, making it the true inverse of lease. Partial release moves to
release --route and release --port [name]; port release and
release --all are removed with pointer errors to the new spellings.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Update docs (README, DOCUMENTATION, requirements)

**Files:**
- Modify: `README.md`
- Modify: `DOCUMENTATION.md`
- Modify: `docs/requirements.md`

**Interfaces:**
- Consumes: the final CLI surface from Tasks 1-2. Verify any doc claim against `lewp <cmd> --help` output in the built binary if unsure.
- Produces: docs with zero references to `lewp add`, `lewp port release`, or `lewp release --all`.

- [ ] **Step 1: Global verb rename in all three files**

Apply everywhere, in `README.md`, `DOCUMENTATION.md`, and `docs/requirements.md`:
- `lewp add` → `lewp lease` (including inside code fences and eval examples)
- `` `add` ``-as-command-name prose → `` `lease` `` (e.g. "the same inference `add` uses" in the init section)
- Do NOT touch `alias add` / `lewp alias add`.

Check with: `grep -rn "lewp add" README.md DOCUMENTATION.md docs/requirements.md` → expect no output.

- [ ] **Step 2: Rewrite the release-related sections of `DOCUMENTATION.md`**

- Command index (lines 31-40): replace the `add`, `port`, `port release`, and `release` lines with:
  ```
  lewp lease [--root R] [--name N] [--host H] [--auto-suffix] [--reset] [--json|--shell]
  lewp port [--name N] [--json|--shell]
  lewp release [--route | --port [N]] [--forget]
  ```
- `## lewp add` heading → `## lewp lease`; update the section's intro sentence ("Register a routed app port..." → "Lease a routed app port and a Lewp-managed host for the current directory. This is the command for creating a route."); update the `--reset` subsection's cross-reference `[lewp release --forget](#lewp-release)` text (anchor stays `#lewp-release`).
- `## lewp port`: delete the `### lewp port release` subsection entirely; add a closing paragraph: "To free a bare port, use [`lewp release --port <name>`](#lewp-release); plain `lewp release` frees the route and every bare port at once."
- `## lewp release`: replace the section body with the release-everything contract:

  ~~~markdown
  ## `lewp release`

  Release everything Lewp holds for the current directory: the route (including
  its aliases and wildcard hosts) and every bare port.

  ```sh
  lewp release
  lewp release --route
  lewp release --port vite
  lewp release --forget
  ```

  By default `release` is the full inverse of `lewp lease` plus any `lewp port`
  leases. Scope it down with:

  - `--route` — release only the route and its aliases, keeping bare ports
  - `--port [<name>]` — release only one bare port (default name `port`,
    mirroring `lewp port` with no `--name`)

  `--route` and `--port` cannot be combined. Without `--forget`, history stays
  in the registry; with `--forget`, Lewp also removes the remembered
  identity/history for what was released (so `--port vite --forget` forgets only
  that port's identity).

  Release is idempotent: when nothing is active it reports `no active route or
  port for this directory` (or the `--route`/`--port` variants) and exits `0`.
  ~~~
- Update the alias section's "Run `lewp add` first" sentence and any remaining `(#lewp-port-release)` or `(#lewp-add)` anchors (→ `(#lewp-lease)`).

- [ ] **Step 3: Update `README.md` and `docs/requirements.md` release references**

- `README.md`: quick-start examples switch to `lewp lease`; any release mention drops `--all` (plain `lewp release` now covers it).
- `docs/requirements.md`: lines 245-246, 398, and 514-515 describe the release contract — update to "plain `lewp release` frees the current folder route and every bare port; `--route` and `--port [name]` scope it down". Line 398's usage becomes `lewp release [--route | --port [name]] [--forget]`.

Check with: `grep -rn "port release\|release --all" README.md DOCUMENTATION.md docs/requirements.md` → expect no output.

- [ ] **Step 4: Verify rendered consistency**

Run: `grep -rn "lewp add\|port release\|release --all" README.md DOCUMENTATION.md docs/ --include="*.md" | grep -v superpowers`
Expected: no output (the spec/plan under docs/superpowers keep their historical references).

- [ ] **Step 5: Commit**

```bash
git add README.md DOCUMENTATION.md docs/requirements.md
git commit -m "docs: document lease/release CLI redesign

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```
