package control

import (
	"context"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/registry"
)

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

func TestServiceReleaseHidesAliases(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "tags.app.work.lewp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Release(ctx, ReleaseRequest{WorkDir: dir}); err != nil {
		t.Fatal(err)
	}
	entries, err := svc.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Host == "tags.app.work.lewp" {
			t.Fatalf("released alias still listed: %+v", entries)
		}
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

func TestServiceMovePreservesAliases(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	srcDir := t.TempDir()
	destDir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: srcDir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: srcDir, Host: "tags.app.work.lewp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, MoveRequest{WorkDir: destDir, From: srcDir}); err != nil {
		t.Fatal(err)
	}
	entries, err := svc.Info(ctx, InfoRequest{WorkDir: destDir})
	if err != nil {
		t.Fatal(err)
	}
	var sawPrimary, sawAlias bool
	for _, entry := range entries {
		sawPrimary = sawPrimary || entry.Host == "app.work.lewp"
		sawAlias = sawAlias || entry.Host == "tags.app.work.lewp"
	}
	if !sawPrimary || !sawAlias {
		t.Fatalf("moved route missing primary or alias: %+v", entries)
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
