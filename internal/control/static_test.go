package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/registry"
)

func TestServiceLeaseStaticResolvesRemembersAndClears(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44600, End: 44630})
	ctx := context.Background()
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	if err := os.Mkdir(dist, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "js", Static: "dist"})
	if err != nil {
		t.Fatal(err)
	}
	if first.StaticDir != dist {
		t.Fatalf("static dir = %q, want %q", first.StaticDir, dist)
	}

	// A plain re-lease keeps the static mode and the same port.
	again, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if again.StaticDir != dist || again.Port != first.Port {
		t.Fatalf("re-lease = %+v, want static %q on port %d", again, dist, first.Port)
	}
	entries, err := svc.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].StaticDir != dist {
		t.Fatalf("list entries = %+v", entries)
	}

	cleared, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, NoStatic: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.StaticDir != "" || cleared.Port != first.Port {
		t.Fatalf("--no-static lease = %+v", cleared)
	}
	route, ok, err := store.RouteByHost(ctx, first.Host)
	if err != nil || !ok || route.StaticDir != "" {
		t.Fatalf("route after --no-static = %+v, %v, %v", route, ok, err)
	}
}

func TestServiceLeaseStaticRejectsMissingOrFileTargets(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44640, End: 44660})
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kol.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for static, want := range map[string]string{
		"dist":   "does not exist",
		"kol.js": "is not a directory",
	} {
		_, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "js", Static: static})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--static %s err = %v, want %q", static, err, want)
		}
	}
	// A rejected --static must not leave a lease behind.
	entries, err := svc.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after rejected leases = %+v", entries)
	}
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Static: dir, NoStatic: true}); err == nil {
		t.Fatal("--static with --no-static succeeded")
	}
}

func TestServiceStaticRouteStateFollowsItsDirectory(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44640, End: 44670})
	ctx := context.Background()
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	if err := os.Mkdir(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "js", Static: "dist"})
	if err != nil {
		t.Fatal(err)
	}
	if !lease.StaticSupported {
		t.Fatal("lease response must advertise static support")
	}

	// Nothing listens on the leased port, yet the route is serving files.
	assertStates := func(want string) {
		t.Helper()
		entries, err := svc.List(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].State != want {
			t.Fatalf("list entries = %+v, want state %q", entries, want)
		}
		plan, _, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Path: dir, Scope: registry.ReleaseScopeRoute})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Items) != 1 || plan.Items[0].State != want {
			t.Fatalf("release plan items = %+v, want state %q", plan.Items, want)
		}
	}
	assertStates("up")

	if err := os.Remove(dist); err != nil {
		t.Fatal(err)
	}
	assertStates("down")
}
