package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/registry"
)

func TestServeConnRecoversFromHandlerPanic(t *testing.T) {
	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(server, func(context.Context, Request) (Response, error) {
			panic("crafted request blew up the handler")
		})
	}()

	go func() { _ = json.NewEncoder(client).Encode(Request{Command: "add"}) }()

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("expected error response after panic, got decode error: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("expected error response after panic, got: %+v", resp)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not return after recovering panic")
	}
}

func TestSocketCallAddUsesTempSocketAndRegistry(t *testing.T) {
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
		Command: "add",
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
		t.Fatalf("bad socket add response: %+v", resp)
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

func TestSocketCallAddAllowsConfiguredCustomSuffix(t *testing.T) {
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
		errs <- ServeWithSuffixes(ctx, socketPath, registryPath, registry.PortRange{Start: 41000, End: 41020}, []string{"lewp", "local.todoordie.com"})
	}()
	waitForSocket(t, socketPath, errs)

	resp, err := Call(ctx, socketPath, Request{
		Command: "add",
		Lease: LeaseRequest{
			WorkDir: t.TempDir(),
			Host:    "feature-1.local.todoordie.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Lease == nil || resp.Lease.Host != "feature-1.local.todoordie.com" {
		t.Fatalf("bad socket add response: %+v", resp)
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
