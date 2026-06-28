package cli

import (
	"bytes"
	"context"
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
	if !strings.Contains(got, "flag provided but not defined: -badflag") || strings.Contains(got, "Usage of lease") {
		t.Fatalf("bad flag output used wrong command name: %q", got)
	}
	if !strings.Contains(got, "Usage of add") {
		t.Fatalf("bad flag output missing add usage: %q", got)
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
	if !strings.Contains(infoOut, "ROUTES\n") || !strings.Contains(infoOut, "HOST=feature-1.audit.lewp") || !strings.Contains(infoOut, "URL=http://feature-1.audit.lewp") || !strings.Contains(infoOut, "PATH="+srcDir) {
		t.Fatalf("info output missing route fields: %q", infoOut)
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
	if !strings.Contains(stdout.String(), "HOST=feature-1.audit.lewp") || !strings.Contains(stdout.String(), "PATH="+destDir) {
		t.Fatalf("move output missing moved route: %q", stdout.String())
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
	portCol := strings.Index(lines[0], "PORT")
	stateCol := strings.Index(lines[0], "STATE")
	pathCol := strings.Index(lines[0], "PATH")
	if portCol < 0 || stateCol < 0 || pathCol < 0 {
		t.Fatalf("list header missing columns:\n%s", got)
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
