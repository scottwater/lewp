package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/control"
)

func TestRunLeaseStaticPrintsAndListsStaticDir(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := filepath.Join(t.TempDir(), "work", "js")
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(Config{Args: []string{"lease", "--host", "kickoffjs.lewp", "--static", "dist"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"HTTPS_URL=https://kickoffjs.lewp\n", "STATIC_DIR=" + dist + "\n"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("lease output missing %q:\n%s", want, stdout.String())
		}
	}

	// --shell output stays limited to the assignable env lines.
	stdout.Reset()
	if code := Run(Config{Args: []string{"lease", "--shell"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "STATIC_DIR") {
		t.Fatalf("--shell exported STATIC_DIR:\n%s", stdout.String())
	}

	stdout.Reset()
	if code := Run(Config{Args: []string{"list"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(strings.TrimSpace(lines[0]), "STATIC") || !strings.HasSuffix(lines[1], dist) {
		t.Fatalf("list output:\n%s", stdout.String())
	}

	stdout.Reset()
	if code := Run(Config{Args: []string{"lease", "--no-static"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	if code := Run(Config{Args: []string{"list"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "STATIC") {
		t.Fatalf("list kept STATIC column after --no-static:\n%s", stdout.String())
	}
}

func TestRunLeaseStaticConflictsWithNoStatic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"lease", "--static", "dist", "--no-static"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code != 2 || !strings.Contains(stderr.String(), "--static and --no-static cannot be combined") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunLeaseStaticRequiresRestartAgainstOlderDaemon(t *testing.T) {
	// An older daemon drops the unknown static fields and answers with a plain
	// lease, which carries no static_supported flag.
	for _, flag := range []string{"--static=dist", "--no-static"} {
		socketPath, _ := startFakeControlPayloadsCapturing(t, controlPayloads(t,
			control.Response{Lease: &control.LeaseResponse{Port: 4100, URL: "http://js.work.lewp", Host: "js.work.lewp"}},
		))
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: []string{"lease", flag}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
		if code != 1 || stdout.Len() != 0 {
			t.Fatalf("%s code=%d stdout=%q stderr=%q", flag, code, stdout.String(), stderr.String())
		}
		if got := stderr.String(); !strings.Contains(got, "does not support --static") || !strings.Contains(got, "lewp system restart") {
			t.Fatalf("%s stderr lacks restart guidance: %q", flag, got)
		}
	}
}

func TestDoctorTargetChecksDescribeStaticRoutes(t *testing.T) {
	socketPath, _ := startFakeControlPayloadsCapturing(t, controlPayloads(t,
		control.Response{Entries: []control.ListEntry{
			{Host: "koljs.lewp", Port: 4100, State: "up", StaticDir: "/w/js/dist"},
			{Host: "gone.lewp", Port: 4101, State: "down", StaticDir: "/w/gone/dist"},
		}},
	))
	checks := targetPortChecks(Config{WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Status != statusOK || checks[0].Detail != "serving files from /w/js/dist" {
		t.Fatalf("up static check = %+v", checks[0])
	}
	if checks[1].Status != statusWarn || !strings.Contains(checks[1].Detail, "/w/gone/dist is missing") || strings.Contains(checks[1].Run, "start your app") {
		t.Fatalf("down static check = %+v", checks[1])
	}
}
