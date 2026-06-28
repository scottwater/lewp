package control

import (
	"context"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/registry"
)

func TestServiceLeasePortReleaseAndList(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Host != "feature-1.audit.lewp" || lease.Port == 0 || lease.URL != "http://feature-1.audit.lewp" {
		t.Fatalf("bad lease: %+v", lease)
	}

	again, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Port != lease.Port || again.Host != lease.Host {
		t.Fatalf("lease not stable: first=%+v second=%+v", lease, again)
	}

	port, err := svc.Port(ctx, PortRequest{WorkDir: dir, Name: "vite"})
	if err != nil {
		t.Fatal(err)
	}
	if port.Host != "" || port.URL != "" || port.Port == 0 {
		t.Fatalf("bad bare port lease: %+v", port)
	}

	entries, err := svc.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("list entries=%d want 2: %+v", len(entries), entries)
	}

	if _, err := svc.Release(ctx, ReleaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"}); err != nil {
		t.Fatal(err)
	}
	entries, err = svc.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != "port" {
		t.Fatalf("released route should be hidden from default list: %+v", entries)
	}
}

func TestServiceDeterministicConflictSuffix(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: firstDir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Lease(ctx, LeaseRequest{WorkDir: secondDir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Host == first.Host {
		t.Fatalf("conflicting host was reused: %s", second.Host)
	}
	if !strings.HasPrefix(second.Host, "feature-1-") || !strings.HasSuffix(second.Host, ".audit.lewp") {
		t.Fatalf("unexpected conflict host: %s", second.Host)
	}
	if len(second.Warnings) == 0 || !strings.Contains(second.Warnings[0], firstDir) {
		t.Fatalf("missing conflict warning with path: %+v", second.Warnings)
	}

	again, err := svc.Lease(ctx, LeaseRequest{WorkDir: secondDir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Host != second.Host {
		t.Fatalf("conflict suffix not stable: %s -> %s", second.Host, again.Host)
	}
}

func TestServiceRemembersExplicitHostOnPlainLease(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1", Host: "audit.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Host != first.Host || again.Host != "audit.lewp" {
		t.Fatalf("explicit host not remembered: first=%+v again=%+v", first, again)
	}
}

func TestServiceRemembersExplicitNameOnPlainLease(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "sso"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != first.Name || again.Host != first.Host {
		t.Fatalf("explicit name not remembered: first=%+v again=%+v", first, again)
	}
}

func TestServiceLeaseIgnoresDaemonEnvironment(t *testing.T) {
	t.Setenv("LEWP_ROOT", "env-root")
	t.Setenv("LEWP_NAME", "env-name")
	t.Setenv("LEWP_HOST", "env.lewp")

	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if lease.RootSource == "env" || lease.NameSource == "env" || lease.HostSource == "env" {
		t.Fatalf("lease used daemon environment: %+v", lease)
	}
	if lease.Host == "env.lewp" || lease.Root == "env-root" || lease.Name == "env-name" {
		t.Fatalf("lease values came from daemon environment: %+v", lease)
	}
}

func TestReleaseFreesHostForAnotherFolder(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: firstDir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Release(ctx, ReleaseRequest{WorkDir: firstDir}); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Lease(ctx, LeaseRequest{WorkDir: secondDir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Host != first.Host {
		t.Fatalf("released host not reused: first=%s second=%s", first.Host, second.Host)
	}
}

func TestServiceInfoReturnsActiveRouteForDir(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	port, err := svc.Port(ctx, PortRequest{WorkDir: dir, Name: "vite"})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := svc.Info(ctx, InfoRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("info entries=%d want 2: %+v", len(entries), entries)
	}
	var sawRoute, sawPort bool
	for _, entry := range entries {
		switch entry.Kind {
		case "route":
			sawRoute = entry.Host == lease.Host && entry.Port == lease.Port
		case "port":
			sawPort = entry.Host == "" && entry.Name == "vite" && entry.Port == port.Port
		}
	}
	if !sawRoute || !sawPort {
		t.Fatalf("info mismatch: entries=%+v route=%+v port=%+v", entries, lease, port)
	}
}

func TestServiceInfoReturnsBarePortForDir(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	port, err := svc.Port(ctx, PortRequest{WorkDir: dir, Name: "vite"})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := svc.Info(ctx, InfoRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("info entries=%d want 1: %+v", len(entries), entries)
	}
	if entries[0].Kind != "port" || entries[0].Name != "vite" || entries[0].Port != port.Port || entries[0].Host != "" {
		t.Fatalf("info missing bare port: entries=%+v port=%+v", entries, port)
	}
}

func TestServiceInfoEmptyForUnknownDir(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()

	dir := t.TempDir()
	entries, err := svc.Info(ctx, InfoRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no route for unknown dir, got %+v", entries)
	}
	records, err := store.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("info mutated registry for unknown dir %s: %+v", dir, records)
	}
}

func TestServiceMovePreservesPortAndHost(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	srcDir := t.TempDir()
	destDir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: srcDir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}

	moved, err := svc.Move(ctx, MoveRequest{WorkDir: destDir, From: srcDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].Port != lease.Port || moved[0].Host != lease.Host {
		t.Fatalf("move did not preserve port/host: %+v vs %+v", moved, lease)
	}

	srcInfo, err := svc.Info(ctx, InfoRequest{WorkDir: srcDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(srcInfo) != 0 {
		t.Fatalf("source still owns route after move: %+v", srcInfo)
	}
	destInfo, err := svc.Info(ctx, InfoRequest{WorkDir: destDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(destInfo) != 1 || destInfo[0].Port != lease.Port || destInfo[0].Host != lease.Host {
		t.Fatalf("destination missing moved route: %+v", destInfo)
	}
}

func TestServiceMoveErrorsWhenNoSourceRoute(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()

	_, err := svc.Move(ctx, MoveRequest{WorkDir: t.TempDir(), From: t.TempDir()})
	if err == nil {
		t.Fatal("expected error moving from a dir with no route")
	}
	if !strings.Contains(err.Error(), "nothing to move") {
		t.Fatalf("unexpected move error: %v", err)
	}
}

func TestServiceMoveRejectsSameDirectory(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Move(ctx, MoveRequest{WorkDir: dir, From: dir}); err == nil {
		t.Fatal("expected same-directory move error")
	}
}

func TestServiceMoveRejectsDestinationWithActiveRoute(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	srcDir := t.TempDir()
	destDir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: srcDir, Root: "audit", Name: "feature-1"}); err != nil {
		t.Fatal(err)
	}
	// Destination already owns a route, even under another name.
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: destDir, Root: "audit", Name: "feature-2"}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Move(ctx, MoveRequest{WorkDir: destDir, From: srcDir}); err == nil {
		t.Fatal("expected collision error when destination already has a route")
	}
}

func TestDoctorReportsHTTPSState(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	checks := svc.Doctor()
	found := false
	for _, check := range checks {
		if strings.Contains(check, "https:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor checks missing HTTPS state: %v", checks)
	}
}

func openStore(t *testing.T) *registry.Store {
	t.Helper()
	store, err := registry.Open(t.TempDir() + "/registry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
