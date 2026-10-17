package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/control"
)

func TestWaitForDaemonRetriesUntilResponsive(t *testing.T) {
	ln := readinessListener(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; attempt < 2; attempt++ {
			conn, err := ln.Accept()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			var req control.Request
			if err := json.NewDecoder(conn).Decode(&req); err != nil || req.Command != "doctor" {
				t.Errorf("readiness request = %+v, err = %v", req, err)
			}
			if attempt == 1 {
				_ = json.NewEncoder(conn).Encode(control.Response{Checks: []string{"registry: readable"}})
			}
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitForDaemon(ctx, ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestWaitForDaemonBoundsUnresponsiveSocket(t *testing.T) {
	ln := readinessListener(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var req control.Request
		_ = json.NewDecoder(conn).Decode(&req)
		// Accept the request but never answer; wait for the client to close.
		var b [1]byte
		_, _ = conn.Read(b[:])
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := waitForDaemon(ctx, ln.Addr().String()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("readiness check exceeded its deadline")
	}
	<-done
}

func TestRunSystemStartFailsWhenLaunchctlSucceedsButDaemonDoesNot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"system", "start"},
		WorkDir:    t.TempDir(),
		SocketPath: filepath.Join(t.TempDir(), "absent.sock"),
		PlistPath:  writeTempPlist(t),
		Stdout:     &stdout,
		Stderr:     &stderr,
		RunCommand: func(context.Context, []string) error { return nil },
		RunCommandOutput: func(context.Context, []string) (string, error) {
			return "state = exited\nlast exit code = 1", nil
		},
	})
	if code != 1 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"launchctl succeeded", "did not become responsive", "daemon.err.log", "last exit code = 1"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("missing %q in %q", want, stderr.String())
		}
	}
	if strings.Contains(stdout.String(), "daemon is responding") {
		t.Fatalf("false readiness claim: %q", stdout.String())
	}
}

func TestRunSystemRestartConfirmsReadiness(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"system", "restart"},
		WorkDir:    t.TempDir(),
		SocketPath: startTestDaemon(t),
		Stdout:     &stdout,
		Stderr:     &stderr,
		RunCommand: func(context.Context, []string) error { return nil },
	})
	if code != 0 || !strings.Contains(stdout.String(), "daemon is responding") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func readinessListener(t *testing.T) net.Listener {
	t.Helper()
	// Keep Unix socket paths below macOS's length limit.
	dir, err := os.MkdirTemp("/tmp", "lewp-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
