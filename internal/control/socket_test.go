package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/registry"
)

func TestServeConnRecoversFromHandlerPanic(t *testing.T) {
	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(context.Background(), server, func(context.Context, Request) (Response, error) {
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

// TestCallHonorsContextDeadline covers the fix that propagates the caller's
// deadline to the connection after dialing: a daemon that accepts a connection
// but never replies must not hang the CLI. Without the SetDeadline, Decode here
// would block until the test timed out.
func TestCallHonorsContextDeadline(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	holdConn := make(chan struct{})
	t.Cleanup(func() { close(holdConn) })
	// Accept connections but never respond, simulating a wedged daemon.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-holdConn
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Call(ctx, socketPath, Request{Command: "doctor"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Call to fail against an unresponsive daemon")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after context deadline; connection deadline not applied")
	}
}

// TestServeRejectsOversizedRequest covers the request-size cap: a client that
// streams a body larger than maxRequestBytes gets an error response and the
// server stays up rather than buffering it without bound.
func TestServeRejectsOversizedRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")
	registryPath := t.TempDir() + "/registry.sqlite"

	errs := make(chan error, 1)
	go func() {
		errs <- Serve(ctx, socketPath, registryPath, registry.PortRange{Start: 41000, End: 41020})
	}()
	waitForSocket(t, socketPath, errs)

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// An unterminated JSON string longer than the cap: the decoder reads up to
	// the limit, then the LimitReader reports EOF and Decode fails.
	oversized := append([]byte(`{"command":"`), make([]byte, maxRequestBytes+1<<20)...)
	for i := len(`{"command":"`); i < len(oversized); i++ {
		oversized[i] = 'a'
	}
	go func() { _, _ = conn.Write(oversized) }()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("expected error response for oversized request, got decode error: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("expected error response for oversized request, got: %+v", resp)
	}

	// The server must remain healthy for a normal request afterward.
	if _, err := Call(ctx, socketPath, Request{Command: "doctor"}); err != nil {
		t.Fatalf("server unhealthy after oversized request: %v", err)
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

func TestServeConnClosesIdleConnectionOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	dispatchCalled := make(chan struct{}, 1)
	go func() {
		defer close(done)
		serveConn(ctx, server, func(context.Context, Request) (Response, error) {
			dispatchCalled <- struct{}{}
			return Response{}, nil
		})
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not stop promptly after context cancellation")
	}
	select {
	case <-dispatchCalled:
		t.Fatal("dispatch ran without a request")
	default:
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

func TestSocketCallAliasCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")
	registryPath := t.TempDir() + "/nested/registry.sqlite"
	workDir := t.TempDir()

	errs := make(chan error, 1)
	go func() {
		errs <- Serve(ctx, socketPath, registryPath, registry.PortRange{Start: 41000, End: 41020})
	}()
	waitForSocket(t, socketPath, errs)

	add, err := Call(ctx, socketPath, Request{
		Command: "add",
		Lease: LeaseRequest{
			WorkDir: workDir,
			Root:    "work",
			Name:    "app",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := Call(ctx, socketPath, Request{
		Command: "alias-add",
		Alias: AliasRequest{
			WorkDir: workDir,
			Host:    "tags.app.work.lewp",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if alias.Lease == nil || add.Lease == nil || alias.Lease.Port != add.Lease.Port || alias.Lease.Host != "tags.app.work.lewp" {
		t.Fatalf("bad alias add response: add=%+v alias=%+v", add, alias)
	}
	aliasPayload := callRaw(t, ctx, socketPath, Request{
		Command: "alias-add",
		Alias: AliasRequest{
			WorkDir: workDir,
			Host:    "tags.app.work.lewp",
		},
	})
	if string(aliasPayload) == "" || containsJSONKey(aliasPayload, "entries") {
		t.Fatalf("alias add response should not include entries: %s", aliasPayload)
	}
	missingList, err := Call(ctx, socketPath, Request{
		Command: "alias-list",
		Alias:   AliasRequest{WorkDir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if missingList.Entries == nil || len(missingList.Entries) != 0 {
		t.Fatalf("missing-route alias list should decode as []: %+v", missingList)
	}
	emptyWorkDir := t.TempDir()
	if _, err := Call(ctx, socketPath, Request{
		Command: "add",
		Lease: LeaseRequest{
			WorkDir: emptyWorkDir,
			Root:    "work",
			Name:    "empty",
		},
	}); err != nil {
		t.Fatal(err)
	}
	emptyList, err := Call(ctx, socketPath, Request{
		Command: "alias-list",
		Alias:   AliasRequest{WorkDir: emptyWorkDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if emptyList.Entries == nil || len(emptyList.Entries) != 0 {
		t.Fatalf("empty alias list should decode as []: %+v", emptyList)
	}
	list, err := Call(ctx, socketPath, Request{
		Command: "alias-list",
		Alias:   AliasRequest{WorkDir: workDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 1 || list.Entries[0].Host != "tags.app.work.lewp" {
		t.Fatalf("bad alias list response: %+v", list)
	}
	remove, err := Call(ctx, socketPath, Request{
		Command: "alias-remove",
		Alias: AliasRequest{
			WorkDir: workDir,
			Host:    "tags.app.work.lewp",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if remove.AliasRemove == nil || remove.AliasRemove.Removed != 1 {
		t.Fatalf("bad alias remove response: %+v", remove)
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

func TestLegacyReleaseErrorsReturnEmptyDispatchAndSocketResponses(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44351, End: 44360})
	ctx := context.Background()
	req := Request{Command: "release", Release: ReleaseRequest{}}

	got, err := dispatch(ctx, svc, req)
	if err == nil || !reflect.DeepEqual(got, Response{}) {
		t.Fatalf("dispatch response=%+v err=%v", got, err)
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConn(ctx, server, func(ctx context.Context, req Request) (Response, error) {
			return dispatch(ctx, svc, req)
		})
	}()
	if err := json.NewEncoder(client).Encode(req); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(client).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || raw["error"] == nil {
		t.Fatalf("socket error response=%v; want only error", raw)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return")
	}
}

func TestLegacyReleaseWirePreservesOldRequestAndResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")
	registryPath := filepath.Join(t.TempDir(), "registry.sqlite")
	workDir := t.TempDir()
	errs := make(chan error, 1)
	go func() {
		errs <- Serve(ctx, socketPath, registryPath, registry.PortRange{Start: 44361, End: 44370})
	}()
	waitForSocket(t, socketPath, errs)
	if _, err := Call(ctx, socketPath, Request{Command: "add", Lease: LeaseRequest{WorkDir: workDir, Root: "work", Name: "app"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(ctx, socketPath, Request{Command: "port", Port: PortRequest{WorkDir: workDir, Name: "vite"}}); err != nil {
		t.Fatal(err)
	}

	workDirJSON, err := json.Marshal(workDir)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyCounts := func(payload []byte, want map[string]int) {
		t.Helper()
		var outer map[string]json.RawMessage
		if err := json.Unmarshal(payload, &outer); err != nil {
			t.Fatalf("legacy response JSON: %v\n%s", err, payload)
		}
		if len(outer) != 1 || outer["release"] == nil {
			t.Fatalf("legacy outer response=%s", payload)
		}
		var release map[string]int
		if err := json.Unmarshal(outer["release"], &release); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(release, want) {
			t.Fatalf("legacy release response=%v want %v", release, want)
		}
	}

	payload := callRawJSON(t, ctx, socketPath, []byte(fmt.Sprintf(
		`{"command":"release","release":{"WorkDir":%s,"Root":"work","Env":{"LEWP_NAME":"app"}}}`,
		workDirJSON,
	)))
	assertLegacyCounts(payload, map[string]int{"routes": 1, "ports": 0})
	info, err := Call(ctx, socketPath, Request{Command: "info", Info: InfoRequest{WorkDir: workDir}})
	if err != nil || len(info.Entries) != 1 || info.Entries[0].Kind != "port" {
		t.Fatalf("legacy env-targeted route release entries=%+v err=%v", info.Entries, err)
	}

	payload = callRawJSON(t, ctx, socketPath, []byte(fmt.Sprintf(
		`{"command":"release","release":{"WorkDir":%s,"Kind":"port","Name":"vite","Env":{"LEWP_NAME":"ignored"}}}`,
		workDirJSON,
	)))
	assertLegacyCounts(payload, map[string]int{"routes": 0, "ports": 1})
	info, err = Call(ctx, socketPath, Request{Command: "info", Info: InfoRequest{WorkDir: workDir}})
	if err != nil || len(info.Entries) != 0 {
		t.Fatalf("legacy named-port release did not mutate allocation: entries=%+v err=%v", info.Entries, err)
	}
}

func TestReleasePlanReferenceKeepsLargeApplyRequestBounded(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44371, End: 44380})
	plan := registry.ReleasePlan{
		Selector: registry.ReleaseSelector{Type: registry.ReleaseSelectorPath, Path: "/work", Recursive: true, Scope: registry.ReleaseScopeRoute},
		Items:    make([]registry.ReleasePlanItem, 2000), Fingerprint: make([]registry.ReleasePlanItem, 2000),
	}
	for i := range plan.Items {
		item := registry.ReleasePlanItem{Kind: "route", Path: fmt.Sprintf("/work/app-%04d", i), Name: fmt.Sprintf("app-%04d", i), Host: fmt.Sprintf("app-%04d.work.lewp", i), RouteID: int64(i + 1)}
		plan.Items[i] = item
		plan.Fingerprint[i] = item
	}
	ref, err := svc.releasePlanReference(plan)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(Request{Command: "release-apply", ReleasePlan: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= maxRequestBytes/100 {
		t.Fatalf("compact apply request=%d bytes, want well below %d", len(payload), maxRequestBytes)
	}
	for _, private := range []string{"Fingerprint", "RouteID", "app-1999.work.lewp"} {
		if bytes.Contains(payload, []byte(private)) {
			t.Fatalf("compact apply request leaked full plan field %q: %s", private, payload)
		}
	}
}

func TestSocketCallReleasePlanAndApplyKeepsPrivatePlanOuter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("lewp-%d-", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")
	registryPath := t.TempDir() + "/registry.sqlite"
	workDir := t.TempDir()

	errs := make(chan error, 1)
	go func() {
		errs <- Serve(ctx, socketPath, registryPath, registry.PortRange{Start: 44330, End: 44340})
	}()
	waitForSocket(t, socketPath, errs)
	if _, err := Call(ctx, socketPath, Request{Command: "add", Lease: LeaseRequest{WorkDir: workDir, Root: "work", Name: "app"}}); err != nil {
		t.Fatal(err)
	}

	planned, err := Call(ctx, socketPath, Request{
		Command: "release-plan",
		Release: ReleaseRequest{WorkDir: workDir, Implicit: true, Scope: registry.ReleaseScopeAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Release == nil || planned.ReleasePlan == nil || planned.Release.Matched != 1 {
		t.Fatalf("planned=%+v", planned)
	}
	outer, err := json.Marshal(planned)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONKey(outer, "release_plan") {
		t.Fatalf("private plan missing from outer protocol: %s", outer)
	}
	public, err := json.Marshal(planned.Release)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"RouteID", "Leases", "PortRows", "Fingerprint", "release_plan"} {
		if bytes.Contains(public, []byte(private)) {
			t.Fatalf("public response leaked %q: %s", private, public)
		}
	}

	tamperedToken := *planned.ReleasePlan
	tamperedToken.Token = "tampered-" + tamperedToken.Token
	tamperedSelector := *planned.ReleasePlan
	tamperedSelector.Selector.Path = workDir + "-other"
	tamperedForget := *planned.ReleasePlan
	tamperedForget.Forget = true
	for name, tampered := range map[string]ReleasePlanReference{
		"token": tamperedToken, "selector": tamperedSelector, "forget": tamperedForget,
	} {
		if _, err := Call(ctx, socketPath, Request{Command: "release-apply", ReleasePlan: &tampered}); err == nil || !strings.Contains(err.Error(), registry.ErrReleasePlanChanged.Error()) {
			t.Fatalf("tampered %s plan reference error=%v", name, err)
		}
	}

	applied, err := Call(ctx, socketPath, Request{Command: "release-apply", ReleasePlan: planned.ReleasePlan})
	if err != nil || applied.Release == nil || applied.Release.Released != 1 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	if _, err := Call(ctx, socketPath, Request{Command: "release-apply", ReleasePlan: planned.ReleasePlan}); err == nil {
		t.Fatal("stale plan apply succeeded")
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

func callRaw(t *testing.T, ctx context.Context, socketPath string, req Request) []byte {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return callRawJSON(t, ctx, socketPath, payload)
}

func callRawJSON(t *testing.T, ctx context.Context, socketPath string, payload []byte) []byte {
	t.Helper()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func containsJSONKey(payload []byte, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
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
