package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

// startTestDaemon runs an in-process control server on a temp unix socket and
// returns its path once it is accepting connections.
func startTestDaemon(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-cli-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "control.sock")
	registryPath := filepath.Join(t.TempDir(), "registry.sqlite")
	errs := make(chan error, 1)
	go func() {
		errs <- control.Serve(ctx, socketPath, registryPath, registry.PortRange{Start: 41000, End: 41020})
	}()
	t.Cleanup(func() {
		cancel()
		<-errs
		_ = os.RemoveAll(socketDir)
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := control.Call(context.Background(), socketPath, control.Request{Command: "doctor"}); err == nil {
			return socketPath
		}
		select {
		case err := <-errs:
			t.Fatalf("control server exited before ready: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control socket %s not ready", socketPath)
	return ""
}

func TestRunLeaseAndInfoMoveCommandHelp(t *testing.T) {
	cases := map[string]string{
		"lease": "lewp lease",
		"info":  "lewp info",
		"move":  "--from",
	}
	for cmd, want := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: []string{cmd, "--help"}, Stdout: &stdout, Stderr: &stderr})
		if code != 0 {
			t.Fatalf("%s --help: code=%d stderr=%q", cmd, code, stderr.String())
		}
		if got := stdout.String(); !strings.Contains(got, want) {
			t.Fatalf("%s --help missing %q:\n%s", cmd, want, got)
		}
	}
}

func TestPortAndReleaseHelpDocumentGlobalReleaseTargets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"release", "--help"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"--path <path>", "--recursive", "--host <host>", "--port <number>", "--name <name>", "--route", "--forget", "--dry-run", "--json", "--yes, -y", `--host '*.app.work.lewp'`} {
		if !strings.Contains(got, want) {
			t.Fatalf("release help missing %q:\n%s", want, got)
		}
	}
	usage := `Usage:
  lewp release [--route | --name <name>] [--forget] [--dry-run] [--json]
  lewp release --path <path> [--recursive] [--route | --name <name>]
               [--forget] [--dry-run] [--json] [--yes|-y]
  lewp release --host <host> [--forget] [--dry-run] [--json]
  lewp release --port <number> [--forget] [--dry-run] [--json]`
	if !strings.Contains(got, usage) {
		t.Fatalf("release help usage does not distinguish implicit and explicit targets:\n%s", got)
	}
	for _, want := range []string{
		"A recursive human mutation prints its full plan.",
		"An interactive run without",
		"--yes asks for confirmation; a noninteractive run without it refuses the",
		"Human output with --yes keeps the preview but skips the prompt.",
		"recursive mutations in noninteractive or JSON mode.",
		"recursive JSON mutation requires --yes and emits one JSON object",
		"preview or prompt.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("release help missing recursive output contract %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--port [<name>]") {
		t.Fatalf("release help retains old named-port syntax:\n%s", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--help"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "lewp release --name vite") || strings.Contains(stdout.String(), "lewp release --port vite") {
		t.Fatalf("port help stale:\n%s", stdout.String())
	}
}

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
	if !strings.Contains(stdout.String(), "Released: 2") || !strings.Contains(stdout.String(), "Forgotten: 0") {
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

// TestRunReleasePortByName proves `lewp release --name <name>` frees a single
// bare port in the current path, leaves the route alone, and is idempotent.
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
	if code := Run(Config{Args: []string{"release", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("release --name code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `released port "vite"`) {
		t.Fatalf("release --name output unexpected: %q", stdout.String())
	}

	// The route survives a name-scoped release.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info after release --name code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "app.work.lewp") {
		t.Fatalf("route missing after port-scoped release: %q", stdout.String())
	}

	// Releasing the same name again is an idempotent no-op.
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"release", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("idempotent release --name code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `no active port named "vite"`) {
		t.Fatalf("idempotent release --name output unexpected: %q", stdout.String())
	}
}

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
	if !strings.Contains(stdout.String(), `released route "app.work.lewp"`) || !strings.Contains(stdout.String(), "Released: 1") {
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

func runOK(t *testing.T, cfg Config) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg.Stdout, cfg.Stderr = &stdout, &stderr
	if code := Run(cfg); code != 0 {
		t.Fatalf("%v code=%d stderr=%q", cfg.Args, code, stderr.String())
	}
	return stdout.String()
}

func parseEnvPort(t *testing.T, output string) int {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "PORT=") {
			var port int
			if _, err := fmt.Sscanf(line, "PORT=%d", &port); err == nil {
				return port
			}
		}
	}
	t.Fatalf("missing PORT in %q", output)
	return 0
}

func assertExactJSONKeys(t *testing.T, got map[string]json.RawMessage, keys ...string) {
	t.Helper()
	want := make(map[string]bool, len(keys))
	for _, key := range keys {
		want[key] = true
	}
	if len(got) != len(want) {
		t.Fatalf("JSON keys=%v want=%v", reflect.ValueOf(got).MapKeys(), keys)
	}
	for key := range got {
		if !want[key] {
			t.Fatalf("unexpected JSON key %q; keys=%v", key, reflect.ValueOf(got).MapKeys())
		}
	}
}

func assertInfoContains(t *testing.T, socketPath, workDir, needle string) {
	t.Helper()
	out := runOK(t, Config{Args: []string{"info"}, WorkDir: workDir, SocketPath: socketPath})
	if !strings.Contains(out, needle) {
		t.Fatalf("info for %s missing %q:\n%s", workDir, needle, out)
	}
}

func assertInfoMissing(t *testing.T, socketPath, workDir, needle string) {
	t.Helper()
	out := runOK(t, Config{Args: []string{"info"}, WorkDir: workDir, SocketPath: socketPath})
	if strings.Contains(out, needle) {
		t.Fatalf("info for %s unexpectedly contains %q:\n%s", workDir, needle, out)
	}
}

func assertNoActiveInfo(t *testing.T, socketPath, workDir string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"info"}, WorkDir: workDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "no Lewp route or port is registered") {
		t.Fatalf("info for %s code=%d stdout=%q stderr=%q", workDir, code, stdout.String(), stderr.String())
	}
}

func TestRunReleaseValidatesSelectorsBeforeCallingDaemon(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"release", "--path", "/work/app", "--host", "app.work.lewp"}, "only one of --path, --host, and --port"},
		{[]string{"release", "--host", "app.work.lewp", "--route"}, "--route and --name require a path selector"},
		{[]string{"release", "--port", "42137", "--name", "vite"}, "--route and --name require a path selector"},
		{[]string{"release", "--route", "--name", "vite"}, "--route and --name cannot be combined"},
		{[]string{"release", "--recursive"}, "--recursive requires an explicit --path"},
		{[]string{"release", "--path"}, "--path requires a value"},
		{[]string{"release", "--path", ""}, "--path requires a value"},
		{[]string{"release", "--host"}, "--host requires a value"},
		{[]string{"release", "--host", ""}, "--host requires a value"},
		{[]string{"release", "--host", "   "}, "--host requires a value"},
		{[]string{"release", "--host", "app.work.lewp.."}, "--host must be a valid DNS host"},
		{[]string{"release", "--host", "app..work.lewp"}, "--host must be a valid DNS host"},
		{[]string{"release", "--host", "tags.*.work.lewp"}, "--host must be a valid DNS host"},
		{[]string{"release", "--host", "*app.work.lewp"}, "--host must be a valid DNS host"},
		{[]string{"release", "--name"}, "--name requires a value"},
		{[]string{"release", "--name", ""}, "--name requires a value"},
		{[]string{"release", "--name", "   "}, "--name requires a value"},
		{[]string{"release", "--name", "/"}, "--name must identify a valid port name"},
		{[]string{"release", "--port"}, "--port now requires a numeric port; use --name <name>"},
		{[]string{"release", "--port", "vite"}, "--port now requires a numeric port; use --name vite"},
		{[]string{"release", "--port", "0"}, "--port must be between 1 and 65535"},
		{[]string{"release", "--port", "65536"}, "--port must be between 1 and 65535"},
		{[]string{"release", "--bogus"}, "unknown flag --bogus"},
		{[]string{"release", "extra"}, "unexpected argument extra"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args[1:], "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(Config{Args: tc.args, WorkDir: t.TempDir(), SocketPath: t.TempDir() + "/missing.sock", Stdout: &stdout, Stderr: &stderr})
			if code != 2 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%q want %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestParseReleaseCanonicalizesGenericCustomSuffixHostOnce(t *testing.T) {
	opts, err := parseReleaseArgs([]string{"--host", " *.APP.Local.Example.COM. "})
	if err != nil {
		t.Fatal(err)
	}
	if opts.host != "*.app.local.example.com" {
		t.Fatalf("host=%q", opts.host)
	}
	req := releaseRequest(Config{WorkDir: t.TempDir()}, opts)
	if req.Host != opts.host {
		t.Fatalf("request host=%q want canonical %q", req.Host, opts.host)
	}
}

func TestRunReleaseTargetsExternalPathAndNamedPort(t *testing.T) {
	socketPath := startTestDaemon(t)
	caller := t.TempDir()
	target := filepath.Join(caller, "deleted", "app")
	other := t.TempDir()
	runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: target, SocketPath: socketPath})
	runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: target, SocketPath: socketPath})
	runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: other, SocketPath: socketPath})

	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"release", "--path", "deleted/app", "--name", "vite"}, WorkDir: caller, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 || !strings.Contains(stdout.String(), `released port "vite"`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertInfoContains(t, socketPath, target, "app.work.lewp")
	assertInfoContains(t, socketPath, other, "vite")
}

func TestRunReleaseByPrimaryAliasAndWildcard(t *testing.T) {
	for _, host := range []string{" APP.WORK.LEWP. ", " TAGS.APP.WORK.LEWP. ", " *.APP.WORK.LEWP. "} {
		t.Run(host, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			dir := t.TempDir()
			runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"alias", "add", "*.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"port", "--name", "keep"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"release", "--host", host}, WorkDir: t.TempDir(), SocketPath: socketPath})
			assertInfoMissing(t, socketPath, dir, "app.work.lewp")
		})
	}
}

func TestRunReleaseJSONContractAndDryRunDoesNotMutate(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath})
	runOK(t, Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath})
	runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})

	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"release", "--path", dir, "--dry-run", "--json"}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("JSON: %v\n%s", err, stdout.String())
	}
	assertExactJSONKeys(t, document, "operation", "dry_run", "selector", "matched", "released", "forgotten", "items")
	if string(document["items"]) == "null" {
		t.Fatal("items must be a non-null array")
	}
	var got control.ReleaseResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Operation != "release" || !got.DryRun || got.Matched != 2 || got.Released != 2 || got.Forgotten != 0 || len(got.Items) != 2 {
		t.Fatalf("got=%+v", got)
	}
	var selector map[string]json.RawMessage
	if err := json.Unmarshal(document["selector"], &selector); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, selector, "type", "path", "implicit", "recursive", "scope")
	if got.Selector.Type != registry.ReleaseSelectorPath || got.Selector.Path == nil || *got.Selector.Path != dir || got.Selector.Implicit == nil || *got.Selector.Implicit || got.Selector.Recursive == nil || *got.Selector.Recursive || got.Selector.Scope == nil || *got.Selector.Scope != registry.ReleaseScopeAll {
		t.Fatalf("selector=%+v", got.Selector)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(document["items"], &items); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"release_plan", "route_id", "fingerprint", `"id"`} {
		if strings.Contains(stdout.String(), private) {
			t.Fatalf("private field %q leaked: %s", private, stdout.String())
		}
	}
	for _, item := range items {
		var kind identity.Kind
		if err := json.Unmarshal(item["kind"], &kind); err != nil {
			t.Fatal(err)
		}
		if string(item["ports"]) == "null" || string(item["actions"]) == "null" {
			t.Fatalf("item arrays must be non-null: %s", stdout.String())
		}
		if kind == identity.KindRoute {
			assertExactJSONKeys(t, item, "kind", "path", "state", "port", "ports", "actions", "host", "hosts")
			if string(item["hosts"]) == "null" {
				t.Fatal("route hosts must be a non-null array")
			}
			var hosts []map[string]json.RawMessage
			if err := json.Unmarshal(item["hosts"], &hosts); err != nil {
				t.Fatal(err)
			}
			for _, host := range hosts {
				assertExactJSONKeys(t, host, "host", "type")
			}
		} else {
			assertExactJSONKeys(t, item, "kind", "path", "state", "port", "ports", "actions", "name")
		}
	}
	assertInfoContains(t, socketPath, dir, "app.work.lewp")
	assertInfoContains(t, socketPath, dir, "vite")
}

func TestConfirmReleaseDefaultsNoAndAcceptsYes(t *testing.T) {
	cases := []struct {
		name                                 string
		input                                string
		assumeYes, interactive, want, prompt bool
	}{
		{name: "yes flag", assumeYes: true, want: true},
		{name: "short yes", input: "y\n", interactive: true, want: true, prompt: true},
		{name: "full yes", input: "YES\n", interactive: true, want: true, prompt: true},
		{name: "no", input: "n\n", interactive: true, prompt: true},
		{name: "empty", input: "\n", interactive: true, prompt: true},
		{name: "eof", interactive: true, prompt: true},
		{name: "noninteractive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cfg := Config{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(tc.input)}
			got := confirmRelease(cfg, tc.assumeYes, tc.interactive)
			if got != tc.want {
				t.Fatalf("got=%v want %v stdout=%q stderr=%q", got, tc.want, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "Proceed? [y/N]") != tc.prompt {
				t.Fatalf("prompt stdout=%q", stdout.String())
			}
		})
	}
}

func TestRunReleaseRecursiveDryRunRespectsPathBoundaries(t *testing.T) {
	socketPath := startTestDaemon(t)
	root := t.TempDir()
	app := filepath.Join(root, "app")
	child := filepath.Join(app, "child")
	application := filepath.Join(root, "application")
	for i, dir := range []string{app, child, application} {
		runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", fmt.Sprintf("app-%d", i)}, WorkDir: dir, SocketPath: socketPath})
		runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"release", "--path", app, "--recursive", "--dry-run", "--json"}, WorkDir: root, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: &bytes.Buffer{}})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var got control.ReleaseResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Matched != 4 {
		t.Fatalf("matched=%d want 4: %s", got.Matched, stdout.String())
	}
	for _, item := range got.Items {
		if item.Path == application {
			t.Fatalf("raw-prefix sibling matched: %+v", item)
		}
	}
}

func TestRunReleaseRecursiveRequiresYesWhenNoninteractiveOrJSON(t *testing.T) {
	for _, args := range [][]string{
		{"release", "--path", "/tmp/tree", "--recursive"},
		{"release", "--path", "/tmp/tree", "--recursive", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: args, WorkDir: t.TempDir(), SocketPath: t.TempDir() + "/missing.sock", Stdout: &stdout, Stderr: &stderr, Stdin: &bytes.Buffer{}})
		if code != 1 {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--yes") || strings.Contains(stdout.String(), "Proceed?") || stdout.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	}

	opts, err := parseReleaseArgs([]string{"--path", "/tmp/tree", "--recursive", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := executeRelease(Config{WorkDir: t.TempDir(), SocketPath: t.TempDir() + "/missing.sock", Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("yes\n")}, opts, true)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("interactive JSON refusal contacted daemon or emitted stdout: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunReleaseRecursiveYesAppliesPreviewedPlan(t *testing.T) {
	for _, yes := range []string{"--yes", "-y"} {
		t.Run(yes, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			root := t.TempDir()
			for i, dir := range []string{root, filepath.Join(root, "one"), filepath.Join(root, "two")} {
				runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", fmt.Sprintf("app-%d", i)}, WorkDir: dir, SocketPath: socketPath})
			}
			var stdout, stderr bytes.Buffer
			code := Run(Config{Args: []string{"release", "--path", root, "--recursive", yes}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: &bytes.Buffer{}})
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			output := stdout.String()
			if !strings.HasPrefix(output, "Release plan:\n") || !strings.Contains(output, "KIND") || !strings.Contains(output, "ACTIONS") || !strings.Contains(output, "Planned releases: 3\nPlanned forgets: 0\n") || !strings.HasSuffix(output, "Released: 3\nForgotten: 0\n") {
				t.Fatalf("missing explicit preview/final totals:\n%s", output)
			}
			for i, dir := range []string{root, filepath.Join(root, "one"), filepath.Join(root, "two")} {
				host := fmt.Sprintf("app-%d.work.lewp", i)
				matchingRows := 0
				for _, line := range strings.Split(output, "\n") {
					if strings.Contains(line, host) && strings.Contains(line, dir) {
						matchingRows++
					}
				}
				if matchingRows != 1 {
					t.Fatalf("preview logical item %q at %q must occur exactly once (no result-table duplicate):\n%s", host, dir, output)
				}
			}
			if strings.Count(output, "KIND") != 1 || strings.Contains(output, "Proceed?") {
				t.Fatalf("--yes preview was duplicated or prompted: %q", output)
			}
		})
	}
}

func TestRunReleaseRecursiveInteractiveAppliesOnlyAfterAffirmative(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		wantApply   bool
	}{
		{name: "yes", input: "yes\n", wantApply: true},
		{name: "no", input: "no\n"},
		{name: "empty", input: "\n"},
		{name: "eof"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			root := t.TempDir()
			child := filepath.Join(root, "child")
			for i, dir := range []string{root, child} {
				runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", fmt.Sprintf("interactive-%d", i)}, WorkDir: dir, SocketPath: socketPath})
			}
			opts, err := parseReleaseArgs([]string{"--path", root, "--recursive"})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := executeRelease(Config{WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(tc.input)}, opts, true)
			const prompt = "Proceed? [y/N] "
			prePrompt, postPrompt, found := strings.Cut(stdout.String(), prompt)
			if !found || strings.Contains(postPrompt, prompt) {
				t.Fatalf("exact prompt missing or duplicated: %q", stdout.String())
			}
			for _, want := range []string{
				"Release plan:\n",
				"KIND", "IDENTITY", "ACTIONS", "release",
				"Planned releases: 2\nPlanned forgets: 0\n",
			} {
				if !strings.Contains(prePrompt, want) {
					t.Fatalf("complete preview before prompt missing %q: %q", want, stdout.String())
				}
			}
			for _, item := range []struct{ host, path string }{
				{host: "interactive-0.work.lewp", path: root},
				{host: "interactive-1.work.lewp", path: child},
			} {
				foundItem := false
				for _, line := range strings.Split(prePrompt, "\n") {
					if strings.Contains(line, item.host) && strings.Contains(line, item.path) {
						foundItem = true
						break
					}
				}
				if !foundItem {
					t.Fatalf("complete preview before prompt missing logical item %+v: %q", item, stdout.String())
				}
			}
			if tc.wantApply {
				if code != 0 {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
				}
				if strings.Contains(postPrompt, "KIND") || strings.Contains(postPrompt, root) || strings.Contains(postPrompt, child) || !strings.Contains(postPrompt, "Released: 2\nForgotten: 0\n") {
					t.Fatalf("successful result must contain only final totals after prompt: %q", stdout.String())
				}
				assertNoActiveInfo(t, socketPath, root)
				assertNoActiveInfo(t, socketPath, child)
				return
			}
			if code != 1 || !strings.Contains(postPrompt, "release aborted") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			assertInfoContains(t, socketPath, root, "interactive-0.work.lewp")
			assertInfoContains(t, socketPath, child, "interactive-1.work.lewp")
		})
	}
}

func TestRunReleaseRecursiveRootDryRunPlansAllAbsolutePathsAndStillConfirms(t *testing.T) {
	socketPath := startTestDaemon(t)
	dirs := []string{t.TempDir(), t.TempDir()}
	for i, dir := range dirs {
		runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", fmt.Sprintf("root-%d", i)}, WorkDir: dir, SocketPath: socketPath})
	}
	var dry control.ReleaseResponse
	if err := json.Unmarshal([]byte(runOK(t, Config{Args: []string{"release", "--path", "/", "--recursive", "--dry-run", "--json"}, WorkDir: t.TempDir(), SocketPath: socketPath})), &dry); err != nil {
		t.Fatal(err)
	}
	if dry.Matched != len(dirs) {
		t.Fatalf("matched=%d want %d: %+v", dry.Matched, len(dirs), dry)
	}
	opts, err := parseReleaseArgs([]string{"--path", "/", "--recursive"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := executeRelease(Config{WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("n\n")}, opts, true); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for i, dir := range dirs {
		assertInfoContains(t, socketPath, dir, fmt.Sprintf("root-%d.work.lewp", i))
	}
}

func TestRunReleaseRecursiveNameMatchesDuplicateDescendantPorts(t *testing.T) {
	socketPath := startTestDaemon(t)
	root := t.TempDir()
	dirs := []string{root, filepath.Join(root, "one"), filepath.Join(root, "two")}
	for _, dir := range dirs {
		runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})
		runOK(t, Config{Args: []string{"port", "--name", "keep"}, WorkDir: dir, SocketPath: socketPath})
	}
	var got control.ReleaseResponse
	out := runOK(t, Config{Args: []string{"release", "--path", root, "--recursive", "--name", "vite", "--yes", "--json"}, WorkDir: t.TempDir(), SocketPath: socketPath})
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Matched != len(dirs) || got.Released != len(dirs) {
		t.Fatalf("got=%+v", got)
	}
	for _, dir := range dirs {
		assertInfoContains(t, socketPath, dir, "keep")
		assertInfoMissing(t, socketPath, dir, "vite")
	}
}

func TestRunReleaseRecursiveYesJSONEmitsExactlyOneObject(t *testing.T) {
	for _, yes := range []string{"--yes", "-y"} {
		t.Run(yes, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			root := t.TempDir()
			runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "json"}, WorkDir: root, SocketPath: socketPath})
			var stdout, stderr bytes.Buffer
			code := Run(Config{Args: []string{"release", "--path", root, "--recursive", yes, "--json"}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: &bytes.Buffer{}})
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			var got control.ReleaseResponse
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not exactly one JSON object: %v\n%s", err, stdout.String())
			}
			if got.Matched != 1 || strings.Contains(stdout.String(), "Proceed?") || strings.Contains(stdout.String(), "KIND") {
				t.Fatalf("impure JSON output: %q", stdout.String())
			}
		})
	}
}

func TestRunReleaseRecursiveStalePreviewFailsWithoutReplanning(t *testing.T) {
	path := "/work/tree/app"
	port := 42137
	plannedResult := control.ReleaseResponse{
		Operation: "release",
		Matched:   1,
		Released:  1,
		Items: []control.ReleaseItem{{
			Kind: identity.KindRoute, Path: path, State: registry.StateActive,
			Port: &port, Ports: []int{port}, Actions: []registry.ReleaseAction{registry.ReleaseActionRelease},
			Host: "app.work.lewp", Hosts: []control.ReleaseHost{{Host: "app.work.lewp", Type: "primary"}},
		}},
	}
	privatePlan := registry.ReleasePlan{Fingerprint: []registry.ReleasePlanItem{{Kind: identity.KindRoute, Path: path}}}
	socketPath, requests := startFakeControlPayloadsCapturing(t, controlPayloads(t,
		control.Response{Release: &plannedResult, ReleasePlan: &privatePlan},
		control.Response{Error: registry.ErrReleasePlanChanged.Error()},
	))
	opts, err := parseReleaseArgs([]string{"--path", "/work/tree", "--recursive"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := executeRelease(Config{WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("yes\n")}, opts, true)
	if code != 1 || !strings.Contains(stderr.String(), "release plan changed; rerun the release command") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Count(stdout.String(), "KIND") != 1 || !strings.Contains(stdout.String(), "Proceed? [y/N]") || strings.Contains(stdout.String(), "Released: 1") {
		t.Fatalf("stale plan output unexpected: %q", stdout.String())
	}
	var gotRequests []control.Request
	for len(gotRequests) < 2 {
		select {
		case req := <-requests:
			gotRequests = append(gotRequests, req)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for requests: %+v", gotRequests)
		}
	}
	select {
	case req := <-requests:
		t.Fatalf("unexpected third/replan request: %+v", req)
	default:
	}
	if gotRequests[0].Command != "release-plan" || gotRequests[1].Command != "release-apply" {
		t.Fatalf("request sequence=%q, %q; want release-plan then release-apply", gotRequests[0].Command, gotRequests[1].Command)
	}
	if gotRequests[0].ReleasePlan != nil {
		t.Fatalf("plan request unexpectedly carried private plan: %+v", gotRequests[0])
	}
	if !reflect.DeepEqual(gotRequests[1].ReleasePlan, &privatePlan) {
		t.Fatalf("apply plan=%+v want exact original private plan %+v", gotRequests[1].ReleasePlan, privatePlan)
	}
}

func TestRunReleaseRecursiveNoOpSkipsPrompt(t *testing.T) {
	socketPath := startTestDaemon(t)
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"release", "--path", t.TempDir(), "--recursive", "--yes"}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("yes\n")})
	if code != 0 || strings.Contains(stdout.String(), "Proceed?") || !strings.Contains(stdout.String(), "no matching route or port") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunReleaseExactPathMultiItemRemainsUnprompted(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "exact"}, WorkDir: dir, SocketPath: socketPath})
	runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"release", "--path", dir}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("n\n")})
	if code != 0 || strings.Contains(stdout.String(), "Proceed?") || !strings.Contains(stdout.String(), "KIND") || !strings.Contains(stdout.String(), "Released: 2") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunReleaseByNumericPortTargetsCurrentOwner(t *testing.T) {
	for _, kind := range []string{"route", "port"} {
		t.Run(kind, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			target, other := t.TempDir(), t.TempDir()
			var leaseOut string
			if kind == "route" {
				leaseOut = runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: target, SocketPath: socketPath})
			} else {
				leaseOut = runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: target, SocketPath: socketPath})
			}
			runOK(t, Config{Args: []string{"port", "--name", "target-keep"}, WorkDir: target, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"port", "--name", "keep"}, WorkDir: other, SocketPath: socketPath})
			port := parseEnvPort(t, leaseOut)
			runOK(t, Config{Args: []string{"release", "--port", fmt.Sprint(port)}, WorkDir: other, SocketPath: socketPath})
			assertInfoMissing(t, socketPath, target, fmt.Sprint(port))
			assertInfoContains(t, socketPath, other, "keep")
		})
	}
}

func TestRunReleaseDryRunParityAcrossSelectors(t *testing.T) {
	cases := []string{"implicit", "path", "missing", "relative", "host", "port", "route", "name"}
	for _, family := range cases {
		t.Run(family, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			caller := t.TempDir()
			target := t.TempDir()
			if family == "relative" {
				target = filepath.Join(caller, "gone", "..", "app")
			}
			leaseOut := runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: target, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: target, SocketPath: socketPath})
			args := []string{"release"}
			workDir := target
			switch family {
			case "path":
				args = append(args, "--path", target)
				workDir = caller
			case "missing":
				args = append(args, "--path", filepath.Join(caller, "missing"))
				workDir = caller
			case "relative":
				args = append(args, "--path", "gone/../app")
				workDir = caller
			case "host":
				args = append(args, "--host", " APP.WORK.LEWP. ")
				workDir = caller
			case "port":
				args = append(args, "--port", fmt.Sprint(parseEnvPort(t, leaseOut)))
				workDir = caller
			case "route":
				args = append(args, "--route")
			case "name":
				args = append(args, "--name", "vite")
			}
			dryArgs := append(append([]string{}, args...), "--dry-run", "--json")
			var dry control.ReleaseResponse
			if err := json.Unmarshal([]byte(runOK(t, Config{Args: dryArgs, WorkDir: workDir, SocketPath: socketPath})), &dry); err != nil {
				t.Fatal(err)
			}
			assertInfoContains(t, socketPath, target, "app.work.lewp")
			assertInfoContains(t, socketPath, target, "vite")
			realArgs := append(append([]string{}, args...), "--json")
			var real control.ReleaseResponse
			if err := json.Unmarshal([]byte(runOK(t, Config{Args: realArgs, WorkDir: workDir, SocketPath: socketPath})), &real); err != nil {
				t.Fatal(err)
			}
			dry.DryRun = false
			if !reflect.DeepEqual(dry, real) {
				t.Fatalf("dry=%+v\nreal=%+v", dry, real)
			}
		})
	}
}

func TestRunReleaseNoOpJSONUsesEmptyItems(t *testing.T) {
	socketPath := startTestDaemon(t)
	base := t.TempDir()
	for _, args := range [][]string{
		{"release", "--path", "missing", "--json"},
		{"release", "--host", "missing.work.lewp", "--json"},
		{"release", "--port", "65535", "--json"},
		{"release", "--name", "missing", "--json"},
	} {
		var got control.ReleaseResponse
		if err := json.Unmarshal([]byte(runOK(t, Config{Args: args, WorkDir: base, SocketPath: socketPath})), &got); err != nil {
			t.Fatal(err)
		}
		if got.Matched != 0 || got.Released != 0 || got.Forgotten != 0 || got.Items == nil || len(got.Items) != 0 {
			t.Fatalf("args=%v got=%+v", args, got)
		}
	}
}

func TestRunReleasePathForgetRemovesActiveAndReleasedHistory(t *testing.T) {
	for _, releasedOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("released_only_%t", releasedOnly), func(t *testing.T) {
			socketPath := startTestDaemon(t)
			dir := t.TempDir()
			runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"alias", "add", "*.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})
			// Create historical route lease and bare-port rows, then reactivate each.
			runOK(t, Config{Args: []string{"release", "--route"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"release", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})
			runOK(t, Config{Args: []string{"port", "--name", "vite"}, WorkDir: dir, SocketPath: socketPath})
			if releasedOnly {
				runOK(t, Config{Args: []string{"release"}, WorkDir: dir, SocketPath: socketPath})
			}
			var got control.ReleaseResponse
			out := runOK(t, Config{Args: []string{"release", "--path", dir, "--forget", "--json"}, WorkDir: t.TempDir(), SocketPath: socketPath})
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			wantReleased := 2
			if releasedOnly {
				wantReleased = 0
			}
			if got.Matched != 2 || got.Released != wantReleased || got.Forgotten != 2 {
				t.Fatalf("got=%+v", got)
			}
			for _, item := range got.Items {
				if releasedOnly && !reflect.DeepEqual(item.Actions, []registry.ReleaseAction{registry.ReleaseActionForget}) {
					t.Fatalf("actions=%v", item.Actions)
				}
				if !releasedOnly && !reflect.DeepEqual(item.Actions, []registry.ReleaseAction{registry.ReleaseActionRelease, registry.ReleaseActionForget}) {
					t.Fatalf("actions=%v", item.Actions)
				}
			}
			all := runOK(t, Config{Args: []string{"list", "--all"}, WorkDir: dir, SocketPath: socketPath})
			if strings.Contains(all, dir) {
				t.Fatalf("history remains:\n%s", all)
			}
		})
	}
}

func TestRunReleaseOperationalFailures(t *testing.T) {
	plan := registry.ReleasePlan{}
	result := control.ReleaseResponse{Operation: "release", Items: []control.ReleaseItem{}}
	validPlan := control.Response{Release: &result, ReleasePlan: &plan}
	cases := []struct {
		name     string
		payloads []string
		want     string
	}{
		{"missing plan", controlPayloads(t, control.Response{Release: &result}), "missing release result or plan"},
		{"missing plan result", controlPayloads(t, control.Response{ReleasePlan: &plan}), "missing release result or plan"},
		{"missing apply result", controlPayloads(t, validPlan, control.Response{}), "missing release result"},
		{"plan daemon error", controlPayloads(t, control.Response{Error: "release planning failed"}), "release planning failed"},
		{"apply daemon error", controlPayloads(t, validPlan, control.Response{Error: "release apply failed"}), "release apply failed"},
		{"plan changed", controlPayloads(t, validPlan, control.Response{Error: registry.ErrReleasePlanChanged.Error()}), "release plan changed; rerun the release command"},
		{"malformed protocol", []string{"{not-json"}, "invalid character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(Config{
				Args:       []string{"release"},
				WorkDir:    t.TempDir(),
				SocketPath: startFakeControlPayloads(t, tc.payloads),
				Stdout:     &stdout,
				Stderr:     &stderr,
			})
			if code != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("operational failure wrote successful stdout: %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%q want %q", stderr.String(), tc.want)
			}
		})
	}
}

func controlPayloads(t *testing.T, responses ...control.Response) []string {
	t.Helper()
	payloads := make([]string, 0, len(responses))
	for _, response := range responses {
		payload, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, string(payload))
	}
	return payloads
}

func startFakeControlPayloads(t *testing.T, payloads []string) string {
	t.Helper()
	socketPath, _ := startFakeControlPayloadsCapturing(t, payloads)
	return socketPath
}

func startFakeControlPayloadsCapturing(t *testing.T, payloads []string) (string, <-chan control.Request) {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "lewp-cli-release-fake-")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "control.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(socketDir)
	})
	requests := make(chan control.Request, len(payloads)+1)
	go func() {
		responseIndex := 0
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req control.Request
			if err := json.NewDecoder(conn).Decode(&req); err == nil {
				requests <- req
			}
			payload := `{"error":"unexpected extra control request"}`
			if responseIndex < len(payloads) {
				payload = payloads[responseIndex]
			}
			responseIndex++
			_, _ = fmt.Fprintln(conn, payload)
			_ = conn.Close()
		}
	}()
	return socketPath, requests
}

func TestWriteReleaseResultHumanRendering(t *testing.T) {
	path := "/work/app"
	implicit := true
	explicit := false
	routeScope := registry.ReleaseScopeRoute
	for _, tc := range []struct {
		name     string
		selector control.ReleaseSelector
		want     string
		reject   string
	}{
		{"implicit route no-op", control.ReleaseSelector{Type: registry.ReleaseSelectorPath, Path: &path, Implicit: &implicit, Scope: &routeScope}, "no active route for this directory", "port"},
		{"explicit route no-op", control.ReleaseSelector{Type: registry.ReleaseSelectorPath, Path: &path, Implicit: &explicit, Scope: &routeScope}, "no matching route for this path", "port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			writeReleaseResult(&out, control.ReleaseResponse{Selector: tc.selector, Items: []control.ReleaseItem{}}, false, "")
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), tc.reject) {
				t.Fatalf("output=%q", out.String())
			}
		})
	}

	port := 42137
	bulk := control.ReleaseResponse{
		DryRun:   true,
		Matched:  2,
		Released: 2,
		Items: []control.ReleaseItem{
			{Kind: identity.KindRoute, Path: path, State: registry.StateActive, Port: &port, Ports: []int{41001, port}, Actions: []registry.ReleaseAction{registry.ReleaseActionRelease}, Host: "app.work.lewp", Hosts: []control.ReleaseHost{{Host: "app.work.lewp", Type: "primary"}, {Host: "tags.app.work.lewp", Type: "alias"}}},
			{Kind: identity.KindPort, Path: path, State: registry.StateReleased, Ports: []int{41002, 41003}, Actions: []registry.ReleaseAction{registry.ReleaseActionRelease}, Name: "vite"},
		},
	}
	var out bytes.Buffer
	writeReleaseResult(&out, bulk, false, "")
	got := out.String()
	for _, want := range []string{"KIND", "IDENTITY", "PORT", "STATE", "ACTIONS", "PATH", "app.work.lewp,tags.app.work.lewp", "vite", "Planned releases: 2", "Planned forgets: 0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bulk output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "app.work.lewp,tags.app.work.lewp") != 1 || strings.Count(got, "vite") != 1 || strings.Contains(got, "41001") || strings.Contains(got, "41002") || strings.Contains(got, "41003") {
		t.Fatalf("bulk output duplicated logical or historical rows:\n%s", got)
	}

	out.Reset()
	writeReleaseResult(&out, control.ReleaseResponse{
		Matched: 1, Forgotten: 1,
		Items: []control.ReleaseItem{{Kind: identity.KindRoute, Path: path, Host: "app.work.lewp", Actions: []registry.ReleaseAction{registry.ReleaseActionForget}}},
	}, false, "")
	if !strings.Contains(out.String(), `forgotten route "app.work.lewp" at /work/app`) {
		t.Fatalf("single forget output=%q", out.String())
	}
}

func TestRunMoveRequiresFrom(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"move"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "--from <path> is required") {
		t.Fatalf("stderr missing --from guidance: %q", stderr.String())
	}
}

func TestRunLeaseBadFlagNamesLease(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"lease", "--badflag"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "flag provided but not defined: -badflag") || strings.Contains(got, "add") {
		t.Fatalf("bad flag output used wrong command name: %q", got)
	}
	if !strings.Contains(got, "lewp lease:") || !strings.Contains(got, "Run: lewp lease --help") {
		t.Fatalf("bad flag output missing concise usage pointer: %q", got)
	}
	if strings.Contains(got, "Usage of") {
		t.Fatalf("bad flag output should not dump generated flag usage: %q", got)
	}
}

func TestRunInfoReportsDaemonNotRunning(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"info"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code == 0 {
		t.Fatal("info succeeded without daemon")
	}
	if !strings.Contains(stderr.String(), "lewp daemon is not running") {
		t.Fatalf("stderr missing daemon guidance: %q", stderr.String())
	}
}

// TestRunAddForwardsClientEnv proves the CLI forwards LEWP_* identity overrides
// to the daemon (which ignores its own environment). Without forwarding, the env
// help would advertise a feature that silently did nothing over the socket.
func TestRunAddForwardsClientEnv(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"lease"},
		WorkDir:    dir,
		SocketPath: socketPath,
		Env:        map[string]string{"LEWP_ROOT": "audit", "LEWP_NAME": "feature-1"},
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") {
		t.Fatalf("forwarded env not honored by daemon: %q", stdout.String())
	}
}

func TestRunAddResetClearsRememberedOverride(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()

	// Remember a host override for this directory.
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"lease", "--root", "audit", "--name", "feature-1", "--host", "custom.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=custom.lewp") {
		t.Fatalf("override add missing host: %q", stdout.String())
	}

	// --reset re-resolves from flags: the override is dropped and a stderr note
	// reports what was cleared, while stdout keeps clean env lines.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"lease", "--reset", "--root", "audit", "--name", "feature-1"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("add --reset code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") {
		t.Fatalf("reset did not clear host override: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "custom.lewp") {
		t.Fatalf("reset warning leaked into stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "reset remembered identity") || !strings.Contains(stderr.String(), "custom.lewp") {
		t.Fatalf("reset note missing on stderr: %q", stderr.String())
	}
}

func TestRunAddInfoMoveRoundTrip(t *testing.T) {
	socketPath := startTestDaemon(t)
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// info on an empty directory exits non-zero with add/move guidance.
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"info"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 1 {
		t.Fatalf("info on empty dir code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no Lewp route or port is registered") || !strings.Contains(stderr.String(), "lewp lease") || !strings.Contains(stderr.String(), "lewp port") || !strings.Contains(stderr.String(), "lewp move") {
		t.Fatalf("info empty message missing guidance: %q", stderr.String())
	}

	// add registers a route.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"lease", "--root", "audit", "--name", "feature-1"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	addOut := stdout.String()
	if !strings.Contains(addOut, "HOST=feature-1.audit.lewp") {
		t.Fatalf("add output missing host: %q", addOut)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"alias", "add", "tags.feature-1.audit.lewp"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=tags.feature-1.audit.lewp") {
		t.Fatalf("alias add output missing host: %q", stdout.String())
	}

	// port registers a bare port for the same directory.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "HOST=") || !strings.Contains(stdout.String(), "PORT=") {
		t.Fatalf("port output should be bare: %q", stdout.String())
	}

	// info now reports the route and bare port for that directory.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"info"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("info code=%d stderr=%q", code, stderr.String())
	}
	infoOut := stdout.String()
	if !strings.HasPrefix(infoOut, "HOST") || !strings.Contains(infoOut, "KIND") || !strings.Contains(infoOut, "PATH") {
		t.Fatalf("info output should be a table: %q", infoOut)
	}
	if !strings.Contains(infoOut, "feature-1.audit.lewp") || !strings.Contains(infoOut, "feature-1  route") || !strings.Contains(infoOut, srcDir) {
		t.Fatalf("info output missing route fields: %q", infoOut)
	}
	if !strings.Contains(infoOut, "tags.feature-1.audit.lewp") || !strings.Contains(infoOut, "feature-1  alias") {
		t.Fatalf("info output missing alias fields: %q", infoOut)
	}
	if strings.Contains(infoOut, "PATH=") || strings.Contains(infoOut, "HOST=") || strings.Contains(infoOut, "PORT=") {
		t.Fatalf("info output should not emit env-style fields by default: %q", infoOut)
	}
	if !strings.Contains(infoOut, "vite") || !strings.Contains(infoOut, "port") || !strings.Contains(infoOut, "down") {
		t.Fatalf("info output missing bare port fields: %q", infoOut)
	}

	// move it to the destination directory, preserving host and port.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"move", "--from", srcDir}, WorkDir: destDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("move code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") || !strings.Contains(stdout.String(), "HOST=tags.feature-1.audit.lewp") || !strings.Contains(stdout.String(), "DIR="+destDir) {
		t.Fatalf("move output missing moved route: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "PATH=") {
		t.Fatalf("move output should not emit PATH= (use DIR=): %q", stdout.String())
	}

	// source still owns its bare port; route moved to destination.
	stdout.Reset()
	stderr.Reset()
	if code = Run(Config{Args: []string{"info"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("source info after move code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "vite") || !strings.Contains(stdout.String(), "port") || strings.Contains(stdout.String(), "feature-1.audit.lewp") {
		t.Fatalf("source info after move should only show port: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code = Run(Config{Args: []string{"info"}, WorkDir: destDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("dest info after move code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "feature-1.audit.lewp") || !strings.Contains(stdout.String(), "tags.feature-1.audit.lewp") {
		t.Fatalf("dest info after move missing route: %q", stdout.String())
	}
}

func TestRunInitWritesConfigAndExcludeGuidance(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"init", "--root", "audit", "--name", "feature-1"}, WorkDir: dir, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("init code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), ".git/info/exclude") {
		t.Fatalf("init missing exclude guidance: %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".lewp.local.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `root = "audit"`) || !strings.Contains(body, `name = "feature-1"`) {
		t.Fatalf("config file missing keys:\n%s", body)
	}

	// A second init without --force refuses to clobber the file.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"init"}, WorkDir: dir, Stdout: &stdout, Stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("second init should refuse: code=%d stderr=%q", code, stderr.String())
	}
}

// TestRunInitForceOverwritesMalformedConfig guards the regression where a
// malformed existing config made `lewp init --force` fail: --force resolves with
// the existing file ignored, so it can replace even an unparseable one.
func TestRunInitForceOverwritesMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lewp.local.toml")
	if err := os.WriteFile(path, []byte("root = \nnaem = \"typo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without --force, a malformed file should still produce the clean "already
	// exists" refusal rather than a TOML parse error (overwrite is decided before
	// the file is ever parsed).
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"init"}, WorkDir: dir, Stdout: &stdout, Stderr: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("init over malformed config should refuse cleanly: code=%d stderr=%q", code, stderr.String())
	}

	// With --force it overwrites despite the malformed file.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"init", "--force", "--root", "audit", "--name", "feature-1"}, WorkDir: dir, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("init --force over malformed config failed: code=%d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `root = "audit"`) || !strings.Contains(body, `name = "feature-1"`) {
		t.Fatalf("force did not regenerate config:\n%s", body)
	}
}

// TestRunInitForceIgnoresExistingConfigValues guards the regression where an
// existing config influenced the regenerated values: --force must rebuild from
// flags and inference only, never from the file it is replacing.
func TestRunInitForceIgnoresExistingConfigValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lewp.local.toml")
	if err := os.WriteFile(path, []byte("root = \"old-root\"\nname = \"old-name\"\nhost = \"old.lewp\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"init", "--force"}, WorkDir: dir, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("init --force failed: code=%d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "old-root") || strings.Contains(body, "old-name") || strings.Contains(body, "old.lewp") {
		t.Fatalf("regenerated config leaked old values:\n%s", body)
	}
}

func TestRunListAlignsColumnsWithMixedHosts(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()
	otherDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"port"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"lease", "--host", "todoordie.lewp"}, WorkDir: otherDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add other code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"list"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("list code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if strings.Contains(got, "\t") {
		t.Fatalf("list output should use spaces, not raw tabs:\n%s", got)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 {
		t.Fatalf("list lines=%d want 4:\n%s", len(lines), got)
	}
	nameCol := strings.Index(lines[0], "NAME")
	kindCol := strings.Index(lines[0], "KIND")
	portCol := strings.Index(lines[0], "PORT")
	stateCol := strings.Index(lines[0], "STATE")
	pathCol := strings.Index(lines[0], "PATH")
	if nameCol < 0 || kindCol < 0 || portCol < 0 || stateCol < 0 || pathCol < 0 {
		t.Fatalf("list header missing columns:\n%s", got)
	}
	// The human table must surface the lease NAME and KIND.
	if !strings.Contains(got, "route") || !strings.Contains(got, "port") {
		t.Fatalf("list output missing KIND values:\n%s", got)
	}
	for _, line := range lines[1:] {
		if !isSpace(line[portCol-1]) || !isSpace(line[stateCol-1]) || !isSpace(line[pathCol-1]) {
			t.Fatalf("list row not aligned to header columns:\n%s", got)
		}
		if line[portCol] == ' ' || line[stateCol] == ' ' || line[pathCol] == ' ' {
			t.Fatalf("list row missing value at aligned column:\n%s", got)
		}
	}
}

func TestRunInfoDefaultsToCurrentDirectoryTable(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()
	otherDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "other"}, WorkDir: otherDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add other code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "HOST") || !strings.Contains(got, "KIND") || strings.Contains(got, "ROUTE\n") || strings.Contains(got, "PORT=") {
		t.Fatalf("info should render as a table by default:\n%s", got)
	}
	if !strings.Contains(got, "app.work.lewp") || !strings.Contains(got, "tags.app.work.lewp") || !strings.Contains(got, "vite") {
		t.Fatalf("info table missing current directory entries:\n%s", got)
	}
	if strings.Contains(got, "other.work.lewp") {
		t.Fatalf("info table leaked another directory:\n%s", got)
	}
}

func TestRunInfoPortFiltersToOneEntryWithoutNewline(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--host", "tags.app.work.lewp", "--port"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info --host --port code=%d stderr=%q", code, stderr.String())
	}
	hostPort := stdout.String()
	if hostPort == "" || strings.Contains(hostPort, "\n") || strings.Contains(hostPort, "PORT=") {
		t.Fatalf("info --host --port stdout=%q want bare port without newline", hostPort)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--name", "vite", "--port"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info --name --port code=%d stderr=%q", code, stderr.String())
	}
	namePort := stdout.String()
	if namePort == "" || strings.Contains(namePort, "\n") || strings.Contains(namePort, "PORT=") {
		t.Fatalf("info --name --port stdout=%q want bare port without newline", namePort)
	}
	if namePort == hostPort {
		t.Fatalf("bare port should differ from routed host port; got %q", namePort)
	}
}

func TestRunInfoShellRequiresSingleFilteredEntry(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--shell"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 2 {
		t.Fatalf("info --shell code=%d want 2 stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires exactly one matching entry") || !strings.Contains(stderr.String(), "--host") {
		t.Fatalf("info --shell error should ask for a filter: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--shell", "--host", "app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info --shell --host code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "export PORT=") || !strings.Contains(got, "export HOST=app.work.lewp\n") || !strings.Contains(got, "export URL=http://app.work.lewp\n") {
		t.Fatalf("info --shell --host missing exports:\n%s", got)
	}
}

func TestRunInfoJSONAndTableFilters(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--json", "--host", "tags.app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info --json --host code=%d stderr=%q", code, stderr.String())
	}
	var entries []control.ListEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("info --json --host not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) != 1 || entries[0].Kind != control.KindAlias || entries[0].Host != "tags.app.work.lewp" {
		t.Fatalf("info --json --host entries=%+v", entries)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--json", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info --json --name code=%d stderr=%q", code, stderr.String())
	}
	entries = nil
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("info --json --name not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) != 2 || entries[0].Kind != identity.KindRoute || entries[1].Kind != control.KindAlias {
		t.Fatalf("info --json --name should return route and alias sharing name: %+v", entries)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"info", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("info --name code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "app.work.lewp") || !strings.Contains(got, "tags.app.work.lewp") {
		t.Fatalf("info --name should include route and alias sharing that name:\n%s", got)
	}
}

func TestRunInfoRejectsConflictingScalarFormats(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}

	for _, args := range [][]string{
		{"info", "--json", "--shell"},
		{"info", "--json", "--port"},
		{"info", "--shell", "--port"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := Run(Config{Args: args, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 2 {
			t.Fatalf("%v code=%d want 2 stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func isSpace(b byte) bool {
	return b == ' '
}

// TestRunListJSONEmitsEntries proves `lewp list --json` returns a parseable JSON
// array carrying each entry's host, kind, name, and state.
func TestRunListJSONEmitsEntries(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"port", "--name", "vite"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("port code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"list", "--json"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("list --json code=%d stderr=%q", code, stderr.String())
	}
	var entries []control.ListEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("list --json not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) != 3 {
		t.Fatalf("list --json entries=%d want 3:\n%s", len(entries), stdout.String())
	}
	if entries[0].Kind != "route" || entries[1].Kind != "alias" {
		t.Fatalf("list --json should order primary route before aliases: %+v", entries)
	}
	var sawRoute, sawAlias, sawPort bool
	for _, e := range entries {
		switch e.Kind {
		case "route":
			sawRoute = true
			if e.Host != "app.work.lewp" {
				t.Fatalf("route entry host=%q", e.Host)
			}
		case "alias":
			sawAlias = true
			if e.Host != "tags.app.work.lewp" {
				t.Fatalf("alias entry host=%q", e.Host)
			}
		case "port":
			sawPort = true
			if e.Name != "vite" {
				t.Fatalf("port entry name=%q", e.Name)
			}
		}
		if e.State == "" {
			t.Fatalf("entry missing state: %+v", e)
		}
	}
	if !sawRoute || !sawAlias || !sawPort {
		t.Fatalf("list --json missing route, alias, or port kind: %s", stdout.String())
	}
}
