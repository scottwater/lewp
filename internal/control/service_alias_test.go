package control

import (
	"context"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

func TestServiceAliasAddRequiresActiveRoute(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	_, err := svc.AliasAdd(context.Background(), AliasRequest{WorkDir: t.TempDir(), Host: "tags.app.lewp"})
	if err == nil {
		t.Fatal("expected alias add without route to fail")
	}
	if !strings.Contains(err.Error(), "no active route for this directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServiceAliasListWithoutActiveRouteReturnsEmptyList(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})

	entries, err := svc.AliasList(context.Background(), AliasRequest{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("entries=%+v want empty non-nil slice", entries)
	}
}

func TestServiceAliasAddReusesRoutePort(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "Tags.App.Work.Lewp."})
	if err != nil {
		t.Fatal(err)
	}
	if alias.Port != lease.Port || alias.Host != "tags.app.work.lewp" || alias.HostKind != HostKindAlias {
		t.Fatalf("bad alias response: %+v lease=%+v", alias, lease)
	}
	again, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "tags.app.work.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if again.LeaseState != "reused" {
		t.Fatalf("duplicate alias state=%q", again.LeaseState)
	}
}

func TestServiceAliasAddPrimaryHostIsReusedPrimary(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "app.work.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if alias.Port != lease.Port || alias.HostKind != identity.HostKindInstance || alias.LeaseState != "reused" {
		t.Fatalf("primary alias response should reflect primary host reuse: %+v", alias)
	}
	entries, err := svc.AliasList(ctx, AliasRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("primary host should not be listed as alias: %+v", entries)
	}
}

func TestServiceAliasAddWarnsWhenExactAliasCoveredBySameRouteWildcard(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "*.app.work.lewp"}); err != nil {
		t.Fatal(err)
	}
	alias, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "tags.app.work.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(alias.Warnings) == 0 || !strings.Contains(alias.Warnings[0], "*.app.work.lewp") {
		t.Fatalf("missing same-route wildcard warning: %+v", alias)
	}
}

func TestServiceAliasWildcardConflictNamesCoveredHost(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: firstDir, Host: "tags.app.lewp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: secondDir, Host: "app.lewp"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: secondDir, Host: "*.app.lewp"})
	if err == nil {
		t.Fatal("expected wildcard conflict")
	}
	if !strings.Contains(err.Error(), "wildcard *.app.lewp would cover tags.app.lewp") || !strings.Contains(err.Error(), firstDir) {
		t.Fatalf("error missing covered host/path: %v", err)
	}
}

func TestServiceAliasRemoveValidatesHost(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AliasRemove(ctx, AliasRequest{WorkDir: dir, Host: "*.app.work.lewp.."})
	if err == nil {
		t.Fatal("expected invalid alias remove host to fail")
	}
}

func TestServiceAliasRemoveAndList(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "tags.app.work.lewp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "*.app.work.lewp"}); err != nil {
		t.Fatal(err)
	}
	entries, err := svc.AliasList(ctx, AliasRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("alias entries=%d want 2: %+v", len(entries), entries)
	}
	for _, entry := range entries {
		if entry.Kind != identity.Kind("alias") || entry.Port != lease.Port || entry.Host == "app.work.lewp" {
			t.Fatalf("bad alias list entry: %+v", entry)
		}
	}
	removed, err := svc.AliasRemove(ctx, AliasRequest{WorkDir: dir, Host: "*.App.Work.Lewp."})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Removed != 1 {
		t.Fatalf("removed=%d want 1", removed.Removed)
	}
	entries, err = svc.AliasList(ctx, AliasRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Host != "tags.app.work.lewp" {
		t.Fatalf("alias not removed: %+v", entries)
	}
}
