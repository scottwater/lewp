package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/control"
)

func TestRunAliasAddListRemove(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "HOST=tags.app.work.lewp") || !strings.Contains(got, "STATE=new") || !strings.Contains(got, "HOST_KIND=alias") {
		t.Fatalf("alias add output unexpected: %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "list"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias list code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=tags.app.work.lewp") {
		t.Fatalf("alias list missing host: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "remove", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias remove code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "removed alias tags.app.work.lewp") {
		t.Fatalf("alias remove output unexpected: %q", stdout.String())
	}
}

func TestRunAliasAddAcceptsJSONAfterHost(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp", "--json"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add --json code=%d stderr=%q", code, stderr.String())
	}
	var lease control.LeaseResponse
	if err := json.Unmarshal(stdout.Bytes(), &lease); err != nil {
		t.Fatalf("alias add --json invalid JSON: %v\n%s", err, stdout.String())
	}
	if lease.Host != "tags.app.work.lewp" {
		t.Fatalf("lease host=%q", lease.Host)
	}
}

func TestRunAliasAddWarnsForExactCoveredBySameRouteWildcard(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "*.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("wildcard alias add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("covered alias add code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already covered by wildcard *.app.work.lewp") {
		t.Fatalf("stderr missing same-route wildcard warning: %q", stderr.String())
	}
}

func TestRunAliasListJSONEmptyArray(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := Run(Config{Args: []string{"lease", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "list", "--json"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias list --json code=%d stderr=%q", code, stderr.String())
	}
	var entries []control.ListEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("alias list --json invalid JSON: %v\n%s", err, stdout.String())
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("entries=%+v want empty array", entries)
	}
}

func TestRunAliasAddRequiresHostArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "add"}, WorkDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "lewp alias add <host>") {
		t.Fatalf("stderr missing usage: %q", stderr.String())
	}
}

func TestRunAliasAddHandlesMissingLeaseResponse(t *testing.T) {
	socketPath := startFakeControlResponse(t, control.Response{})
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "add", "tags.app.lewp"}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "malformed daemon response") {
		t.Fatalf("stderr missing malformed response error: %q", stderr.String())
	}
}

func TestRunAliasRemoveRequiresHostArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "remove"}, WorkDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "lewp alias remove <host>") {
		t.Fatalf("stderr missing usage: %q", stderr.String())
	}
}

func TestRunAliasRemoveBadFlagIsConcise(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "remove", "--bogus"}, WorkDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "lewp alias remove: unknown flag --bogus") || !strings.Contains(got, "lewp alias remove <host>") {
		t.Fatalf("stderr missing concise usage: %q", got)
	}
	if strings.Contains(got, "Usage of") {
		t.Fatalf("stderr leaked flag usage: %q", got)
	}
}

func TestRunAliasRemoveHandlesMissingRemoveResponse(t *testing.T) {
	socketPath := startFakeControlResponse(t, control.Response{})
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "remove", "tags.app.lewp"}, WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "malformed daemon response") {
		t.Fatalf("stderr missing malformed response error: %q", stderr.String())
	}
}

func TestRunAliasHelpAndCompletion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "--help"}, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("alias --help code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "lewp alias add <host>") || !strings.Contains(got, "*.app.lewp") {
		t.Fatalf("alias help missing usage/examples:\n%s", got)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(Config{Args: []string{"completion", "bash"}, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("completion bash code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "alias") || !strings.Contains(got, "add remove list") {
		t.Fatalf("completion missing alias command/subcommands:\n%s", got)
	}
}

func startFakeControlResponse(t *testing.T, resp control.Response) string {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "lewp-cli-fake-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := socketDir + "/control.sock"
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req control.Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(resp)
	}()
	return socketPath
}
