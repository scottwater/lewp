package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/control"
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

func TestRunAddAndInfoMoveCommandHelp(t *testing.T) {
	cases := map[string]string{
		"add":  "lewp add",
		"info": "lewp info",
		"move": "--from",
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

func TestRunAddBadFlagNamesAdd(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"add", "--badflag"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "flag provided but not defined: -badflag") || strings.Contains(got, "lease") {
		t.Fatalf("bad flag output used wrong command name: %q", got)
	}
	if !strings.Contains(got, "lewp add:") || !strings.Contains(got, "Run: lewp add --help") {
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
		Args:       []string{"add"},
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
	if !strings.Contains(stderr.String(), "no Lewp route or port is registered") || !strings.Contains(stderr.String(), "lewp add") || !strings.Contains(stderr.String(), "lewp port") || !strings.Contains(stderr.String(), "lewp move") {
		t.Fatalf("info empty message missing guidance: %q", stderr.String())
	}

	// add registers a route.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"add", "--root", "audit", "--name", "feature-1"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	addOut := stdout.String()
	if !strings.Contains(addOut, "HOST=feature-1.audit.lewp") {
		t.Fatalf("add output missing host: %q", addOut)
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
	if !strings.Contains(infoOut, "ROUTES\n") || !strings.Contains(infoOut, "HOST=feature-1.audit.lewp") || !strings.Contains(infoOut, "URL=http://feature-1.audit.lewp") || !strings.Contains(infoOut, "DIR="+srcDir) {
		t.Fatalf("info output missing route fields: %q", infoOut)
	}
	// HTTPS_URL accompanies the HTTP URL for a routed host.
	if !strings.Contains(infoOut, "HTTPS_URL=https://feature-1.audit.lewp") {
		t.Fatalf("info output missing HTTPS_URL: %q", infoOut)
	}
	// The misleading PATH= key (which shadows $PATH) must be gone.
	if strings.Contains(infoOut, "PATH=") {
		t.Fatalf("info output should not emit PATH= (use DIR=): %q", infoOut)
	}
	if !strings.Contains(infoOut, "PORTS\n") || !strings.Contains(infoOut, "NAME=vite") || !strings.Contains(infoOut, "STATE=down") {
		t.Fatalf("info output missing bare port fields: %q", infoOut)
	}

	// move it to the destination directory, preserving host and port.
	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"move", "--from", srcDir}, WorkDir: destDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("move code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") || !strings.Contains(stdout.String(), "DIR="+destDir) {
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
	if !strings.Contains(stdout.String(), "PORTS\n") || !strings.Contains(stdout.String(), "NAME=vite") || strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") {
		t.Fatalf("source info after move should only show port: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code = Run(Config{Args: []string{"info"}, WorkDir: destDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("dest info after move code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") {
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
	if code := Run(Config{Args: []string{"add", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add app code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"add", "--host", "todoordie.lewp"}, WorkDir: otherDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
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

func isSpace(b byte) bool {
	return b == ' '
}

// TestRunListJSONEmitsEntries proves `lewp list --json` returns a parseable JSON
// array carrying each entry's host, kind, name, and state.
func TestRunListJSONEmitsEntries(t *testing.T) {
	socketPath := startTestDaemon(t)
	appDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"add", "--root", "work", "--name", "app"}, WorkDir: appDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
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
	if len(entries) != 2 {
		t.Fatalf("list --json entries=%d want 2:\n%s", len(entries), stdout.String())
	}
	var sawRoute, sawPort bool
	for _, e := range entries {
		switch e.Kind {
		case "route":
			sawRoute = true
			if e.Host != "app.work.lewp" {
				t.Fatalf("route entry host=%q", e.Host)
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
	if !sawRoute || !sawPort {
		t.Fatalf("list --json missing route or port kind: %s", stdout.String())
	}
}
