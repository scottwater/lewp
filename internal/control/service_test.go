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

	if err := svc.Release(ctx, ReleaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"}); err != nil {
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
	if err := svc.Release(ctx, ReleaseRequest{WorkDir: firstDir}); err != nil {
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
