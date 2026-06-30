package control

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
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

func TestServiceForwardedEnvResolvesIdentity(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	// The daemon must honor identity env values the *client* forwarded, not its
	// own process environment. Set a misleading value in the daemon process to
	// prove it is ignored even when the client forwards different ones.
	t.Setenv("LEWP_ROOT", "daemon-root")
	t.Setenv("LEWP_NAME", "daemon-name")

	lease, err := svc.Lease(ctx, LeaseRequest{
		WorkDir: dir,
		Env:     map[string]string{"LEWP_ROOT": "audit", "LEWP_NAME": "feature-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Host != "feature-1.audit.lewp" {
		t.Fatalf("forwarded env not applied: host=%q", lease.Host)
	}
	if lease.RootSource != identity.SourceEnv || lease.NameSource != identity.SourceEnv {
		t.Fatalf("sources root=%s name=%s want env", lease.RootSource, lease.NameSource)
	}
}

func TestServiceLeaseAllowsConfiguredCustomSuffix(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	svc.SetManagedSuffixes([]string{"lewp", "local.todoordie.com"})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{
		WorkDir: dir,
		Host:    "feature-1.local.todoordie.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Host != "feature-1.local.todoordie.com" {
		t.Fatalf("host=%q", lease.Host)
	}
}

func TestServiceSetManagedSuffixesCanonicalizesNames(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	svc.SetManagedSuffixes([]string{"Local.TodoOrDie.Com.", "lewp", "local.todoordie.com"})

	want := []string{"lewp", "local.todoordie.com"}
	if !reflect.DeepEqual(svc.managedSuffixes, want) {
		t.Fatalf("managedSuffixes=%v want %v", svc.managedSuffixes, want)
	}
}

func TestServiceSetManagedSuffixesKeepsBuiltInWithCustomOnly(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	svc.SetManagedSuffixes([]string{"local.todoordie.com"})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{
		WorkDir: dir,
		Host:    "feature-1.audit.lewp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Host != "feature-1.audit.lewp" {
		t.Fatalf("host=%q", lease.Host)
	}
}

func TestServiceIgnoresDaemonProcessEnv(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	// With no forwarded env, identity must come from inference — never the
	// daemon's own LEWP_* variables. An empty (or nil) request env must keep the
	// daemon process environment from leaking into resolution.
	t.Setenv("LEWP_ROOT", "daemon-root")
	t.Setenv("LEWP_NAME", "daemon-name")
	t.Setenv("LEWP_HOST", "daemon.lewp")

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Root == "daemon-root" || lease.Name == "daemon-name" || lease.Host == "daemon.lewp" {
		t.Fatalf("daemon process env leaked into lease: %+v", lease)
	}
	if lease.RootSource != identity.SourceInferred || lease.NameSource != identity.SourceInferred {
		t.Fatalf("sources root=%s name=%s want inferred", lease.RootSource, lease.NameSource)
	}

	// A nil request env must behave identically: still no daemon-env leakage.
	nilEnv, err := svc.Port(ctx, PortRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if nilEnv.Root == "daemon-root" || nilEnv.Name == "daemon-name" {
		t.Fatalf("daemon process env leaked into port lease: %+v", nilEnv)
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

func TestServiceExplicitHostConflictFailsByDefault(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: firstDir, Host: "audit.lewp"}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Lease(ctx, LeaseRequest{WorkDir: secondDir, Host: "audit.lewp"})
	if err == nil {
		t.Fatal("explicit host conflict should fail by default")
	}
	var conflict *HostConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want HostConflictError, got %T: %v", err, err)
	}
	if conflict.OwnerPath != firstDir {
		t.Fatalf("conflict owner path=%q want %q", conflict.OwnerPath, firstDir)
	}
	msg := err.Error()
	if !strings.Contains(msg, firstDir) || !strings.Contains(msg, "lewp release --forget") || !strings.Contains(msg, "--auto-suffix") {
		t.Fatalf("conflict message missing cleanup guidance: %q", msg)
	}

	// Opting into --auto-suffix keeps the deterministic-suffix behavior.
	suffixed, err := svc.Lease(ctx, LeaseRequest{WorkDir: secondDir, Host: "audit.lewp", AutoSuffix: true})
	if err != nil {
		t.Fatalf("auto-suffix lease failed: %v", err)
	}
	if suffixed.Host == "audit.lewp" || !strings.HasSuffix(suffixed.Host, ".lewp") {
		t.Fatalf("auto-suffix did not change host: %q", suffixed.Host)
	}
}

func TestServiceAutoSuffixExactCustomSuffixStaysInsideManagedSuffix(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	svc.SetManagedSuffixes([]string{"localkickofflabs.com"})
	ctx := context.Background()
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: firstDir, Host: "localkickofflabs.com"}); err != nil {
		t.Fatal(err)
	}
	suffixed, err := svc.Lease(ctx, LeaseRequest{
		WorkDir:    secondDir,
		Host:       "localkickofflabs.com",
		AutoSuffix: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if suffixed.Host == "localkickofflabs.com" {
		t.Fatalf("auto-suffix did not change host: %q", suffixed.Host)
	}
	if !strings.HasSuffix(suffixed.Host, ".localkickofflabs.com") {
		t.Fatalf("auto-suffix escaped managed suffix: %q", suffixed.Host)
	}
	if strings.HasSuffix(suffixed.Host, "-localkickofflabs.com") {
		t.Fatalf("auto-suffix rewrote apex outside resolver suffix: %q", suffixed.Host)
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

func TestDoctorReportsHTTPSState(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	checks := svc.Doctor(context.Background())
	var https, registryOK bool
	for _, check := range checks {
		if strings.Contains(check, "https:") {
			https = true
		}
		if check == "registry: readable" {
			registryOK = true
		}
	}
	if !https {
		t.Fatalf("doctor checks missing HTTPS state: %v", checks)
	}
	if !registryOK {
		t.Fatalf("doctor checks missing registry readability: %v", checks)
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
