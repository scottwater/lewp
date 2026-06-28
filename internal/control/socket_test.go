package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/registry"
)

func TestSocketCallLeaseUsesTempSocketAndRegistry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")
	registryPath := t.TempDir() + "/nested/registry.sqlite"

	errs := make(chan error, 1)
	go func() {
		errs <- Serve(ctx, socketPath, registryPath, registry.PortRange{Start: 41000, End: 41020})
	}()
	waitForSocket(t, socketPath, errs)

	resp, err := Call(ctx, socketPath, Request{
		Command: "lease",
		Lease: LeaseRequest{
			WorkDir: t.TempDir(),
			Root:    "audit",
			Name:    "feature-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Lease == nil || resp.Lease.Host != "feature-1.audit.lewp" {
		t.Fatalf("bad socket lease response: %+v", resp)
	}

	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func waitForSocket(t *testing.T, socketPath string, errs <-chan error) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := Call(context.Background(), socketPath, Request{Command: "doctor"}); err == nil {
			return
		}
		select {
		case err := <-errs:
			t.Fatalf("server exited before ready: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s did not become ready", socketPath)
}
