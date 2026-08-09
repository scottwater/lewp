package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

func TestServicePlansReleaseSelectorsAndCanonicalizesInput(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44000, End: 44030})
	ctx := context.Background()
	base := t.TempDir()
	dir := filepath.Join(base, "gone", "app")
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

	pathResult, pathPlan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: base, Path: "gone/../gone/app", Scope: registry.ReleaseScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if pathResult.Selector.Path == nil || *pathResult.Selector.Path != dir || pathResult.Matched != 1 || len(pathPlan.Items) != 1 {
		t.Fatalf("path result=%+v plan=%+v", pathResult, pathPlan)
	}
	for _, raw := range []string{" APP.WORK.LEWP. ", " TAGS.APP.WORK.LEWP. ", " *.APP.WORK.LEWP. "} {
		got, _, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: base, Host: raw})
		if err != nil || got.Matched != 1 || got.Items[0].Host != "app.work.lewp" || len(got.Items[0].Hosts) != 3 {
			t.Fatalf("host %q result=%+v err=%v", raw, got, err)
		}
	}
	byPort, _, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: base, Port: lease.Port})
	if err != nil || byPort.Matched != 1 || byPort.Items[0].Kind != identity.KindRoute {
		t.Fatalf("port result=%+v err=%v", byPort, err)
	}
}

func TestServicePathForgetIncludesReleasedHistoryAndReportsActions(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44100, End: 44130})
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Port(ctx, PortRequest{WorkDir: dir, Name: "vite"}); err != nil {
		t.Fatal(err)
	}
	routeOnly, plan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeRoute})
	if err != nil {
		t.Fatal(err)
	}
	if routeOnly.Matched != 1 {
		t.Fatalf("route plan=%+v", routeOnly)
	}
	if _, err := svc.ApplyRelease(ctx, plan, false); err != nil {
		t.Fatal(err)
	}

	result, forgetPlan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeAll, Forget: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 2 || result.Released != 1 || result.Forgotten != 2 {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(result.Items[0].Actions, []registry.ReleaseAction{registry.ReleaseActionForget}) {
		t.Fatalf("route actions=%v", result.Items[0].Actions)
	}
	if !reflect.DeepEqual(result.Items[1].Actions, []registry.ReleaseAction{registry.ReleaseActionRelease, registry.ReleaseActionForget}) {
		t.Fatalf("port actions=%v", result.Items[1].Actions)
	}
	applied, err := svc.ApplyRelease(ctx, forgetPlan, false)
	if err != nil || applied.Released != 1 || applied.Forgotten != 2 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestReleaseControlProtocolKeepsPlanPrivateFromPublicResult(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44200, End: 44220})
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	planned, err := dispatch(ctx, svc, Request{Command: "release-plan", Release: ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Release == nil || planned.ReleasePlan == nil || planned.Release.Matched != 1 {
		t.Fatalf("planned=%+v", planned)
	}
	public, err := json.Marshal(planned.Release)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"route_id", "lease_ids", "port_row", "fingerprint", "release_plan"} {
		if bytes.Contains(public, []byte(private)) {
			t.Fatalf("public JSON leaked %q: %s", private, public)
		}
	}
	applied, err := dispatch(ctx, svc, Request{Command: "release-apply", ReleasePlan: planned.ReleasePlan})
	if err != nil || applied.Release == nil || applied.Release.Released != 1 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestServiceReleaseSelectorsAreExactAndUnknownResultsUseArrays(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44230, End: 44260})
	ctx := context.Background()
	dir := t.TempDir()
	route, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "*.app.work.lewp"}); err != nil {
		t.Fatal(err)
	}
	bare, err := svc.Port(ctx, PortRequest{WorkDir: dir, Name: "VITE Dev"})
	if err != nil {
		t.Fatal(err)
	}

	concrete, _, err := svc.PlanRelease(ctx, ReleaseRequest{Host: "foo.app.work.lewp"})
	if err != nil || concrete.Matched != 0 || concrete.Items == nil {
		t.Fatalf("concrete wildcard result=%+v err=%v", concrete, err)
	}
	wildcard, _, err := svc.PlanRelease(ctx, ReleaseRequest{Host: "*.APP.WORK.LEWP."})
	if err != nil || wildcard.Matched != 1 {
		t.Fatalf("exact wildcard result=%+v err=%v", wildcard, err)
	}

	byRoutePort, _, err := svc.PlanRelease(ctx, ReleaseRequest{Port: route.Port})
	if err != nil || len(byRoutePort.Items) != 1 || byRoutePort.Items[0].Kind != identity.KindRoute {
		t.Fatalf("route port result=%+v err=%v", byRoutePort, err)
	}
	byBarePort, _, err := svc.PlanRelease(ctx, ReleaseRequest{Port: bare.Port})
	if err != nil || len(byBarePort.Items) != 1 || byBarePort.Items[0].Kind != identity.KindPort || byBarePort.Items[0].Name != "vite-dev" {
		t.Fatalf("bare port result=%+v err=%v", byBarePort, err)
	}

	unknown := []ReleaseRequest{
		{WorkDir: dir, Path: "missing", Scope: registry.ReleaseScopeAll},
		{Host: "missing.work.lewp"},
		{Port: 65535},
	}
	for _, req := range unknown {
		got, _, err := svc.PlanRelease(ctx, req)
		if err != nil || got.Matched != 0 || got.Released != 0 || got.Forgotten != 0 || got.Items == nil || len(got.Items) != 0 {
			t.Fatalf("unknown request=%+v result=%+v err=%v", req, got, err)
		}
	}
}

func TestServiceReleaseReuseSelectsCurrentOwnerAndPathForgetIsIsolated(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44270, End: 44270})
	ctx := context.Background()
	oldDir := t.TempDir()
	old, err := svc.Lease(ctx, LeaseRequest{WorkDir: oldDir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	_, oldPlan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: oldDir, Implicit: true, Scope: registry.ReleaseScopeRoute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyRelease(ctx, oldPlan, false); err != nil {
		t.Fatal(err)
	}

	newDir := t.TempDir()
	current, err := svc.Lease(ctx, LeaseRequest{WorkDir: newDir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if current.Host != old.Host || current.Port != old.Port {
		t.Fatalf("identity not reused: old=%+v current=%+v", old, current)
	}
	for _, req := range []ReleaseRequest{{Host: old.Host}, {Port: old.Port}} {
		got, _, err := svc.PlanRelease(ctx, req)
		if err != nil || got.Matched != 1 || got.Items[0].Path != newDir {
			t.Fatalf("current-owner request=%+v result=%+v err=%v", req, got, err)
		}
	}

	forgotten, forgetPlan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: oldDir, Implicit: true, Scope: registry.ReleaseScopeAll, Forget: true})
	if err != nil || forgotten.Matched != 1 || forgotten.Items[0].Path != oldDir {
		t.Fatalf("old path forget=%+v err=%v", forgotten, err)
	}
	if _, err := svc.ApplyRelease(ctx, forgetPlan, false); err != nil {
		t.Fatal(err)
	}
	stillCurrent, _, err := svc.PlanRelease(ctx, ReleaseRequest{Host: old.Host})
	if err != nil || stillCurrent.Matched != 1 || stillCurrent.Items[0].Path != newDir {
		t.Fatalf("current owner changed after old forget: %+v err=%v", stillCurrent, err)
	}
	gone, _, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: oldDir, Implicit: true, Scope: registry.ReleaseScopeAll, Forget: true})
	if err != nil || gone.Matched != 0 || gone.Items == nil {
		t.Fatalf("old history remains: %+v err=%v", gone, err)
	}
}

func TestServiceReleaseJSONVariantsAndNullableFields(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44280, End: 44300})
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Port(ctx, PortRequest{WorkDir: dir, Name: "VITE Dev"}); err != nil {
		t.Fatal(err)
	}

	pathResult, pathPlan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Path: ".", Scope: registry.ReleaseScopeRoute})
	if err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, pathResult.Selector, "type", "path", "implicit", "recursive", "scope")
	assertExactJSONKeys(t, mustPlanRelease(t, svc, ctx, ReleaseRequest{Host: "APP.WORK.LEWP"}).Selector, "type", "host")
	assertExactJSONKeys(t, mustPlanRelease(t, svc, ctx, ReleaseRequest{Port: *pathResult.Items[0].Port}).Selector, "type", "port")

	if _, err := svc.ApplyRelease(ctx, pathPlan, false); err != nil {
		t.Fatal(err)
	}
	forget, _, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeAll, Forget: true})
	if err != nil || len(forget.Items) != 2 {
		t.Fatalf("forget=%+v err=%v", forget, err)
	}
	if forget.Operation != "release" || forget.Selector.Path == nil || *forget.Selector.Path != dir || forget.Selector.Implicit == nil || !*forget.Selector.Implicit {
		t.Fatalf("implicit forget response=%+v", forget)
	}
	assertExactJSONKeys(t, forget, "operation", "dry_run", "selector", "matched", "released", "forgotten", "items")
	assertExactJSONKeys(t, forget.Selector, "type", "path", "implicit", "recursive", "scope")
	assertExactJSONKeys(t, forget.Items[0], "kind", "path", "state", "port", "ports", "actions", "host", "hosts")
	assertExactJSONKeys(t, forget.Items[1], "kind", "path", "state", "port", "ports", "actions", "name")
	for _, host := range forget.Items[0].Hosts {
		assertExactJSONKeys(t, host, "host", "type")
	}

	payload, err := json.Marshal(forget)
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	if string(object.Items[0]["port"]) != "null" {
		t.Fatalf("released active port is not null: %s", payload)
	}
}

func TestServiceReleaseDryRunReportsActionsWithoutMutation(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44321, End: 44329})
	ctx := context.Background()
	dir := t.TempDir()
	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}

	planned, plan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeRoute, Forget: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyRelease(ctx, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	for label, got := range map[string]ReleaseResponse{"planned": planned, "applied": applied} {
		if !got.DryRun || got.Operation != "release" || got.Matched != 1 || got.Released != 1 || got.Forgotten != 1 || len(got.Items) != 1 {
			t.Fatalf("%s dry-run=%+v", label, got)
		}
	}
	stillActive, _, err := svc.PlanRelease(ctx, ReleaseRequest{Host: lease.Host})
	if err != nil || stillActive.Matched != 1 || stillActive.Items[0].State == registry.StateReleased {
		t.Fatalf("dry-run mutated route: result=%+v err=%v", stillActive, err)
	}
}

func TestServiceLegacyExplicitRouteReleaseRequiresMatchingName(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44341, End: 44350})
	ctx := context.Background()
	dir := t.TempDir()
	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "current"})
	if err != nil {
		t.Fatal(err)
	}

	for _, req := range []ReleaseRequest{
		{WorkDir: dir, Name: "other"},
		{WorkDir: dir, Root: "other-root"},
		{WorkDir: dir, Root: "other-root", Name: "other"},
	} {
		got, err := svc.Release(ctx, req)
		if err != nil || got.Matched != 0 || got.Released != 0 || got.Items == nil {
			t.Fatalf("mismatched legacy request=%+v result=%+v err=%v", req, got, err)
		}
		current, _, err := svc.PlanRelease(ctx, ReleaseRequest{Host: lease.Host})
		if err != nil || current.Matched != 1 || current.Items[0].State == registry.StateReleased {
			t.Fatalf("mismatched legacy release mutated route: result=%+v err=%v", current, err)
		}
	}

	got, err := svc.Release(ctx, ReleaseRequest{WorkDir: dir, Root: "work", Name: "current"})
	if err != nil || got.Released != 1 {
		t.Fatalf("matching legacy release=%+v err=%v", got, err)
	}
}

func TestServiceReleaseHealthChangeDoesNotInvalidatePlan(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44301, End: 44309})
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "missing", "app")
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	planned, plan, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeRoute})
	if err != nil || planned.Items[0].State != "stale" {
		t.Fatalf("planned=%+v err=%v", planned, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyRelease(ctx, plan, false)
	if err != nil || applied.Released != 1 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestServiceApplyReleasePropagatesChangedPlan(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 44310, End: 44320})
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"}); err != nil {
		t.Fatal(err)
	}
	_, stale, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeRoute})
	if err != nil {
		t.Fatal(err)
	}
	_, current, err := svc.PlanRelease(ctx, ReleaseRequest{WorkDir: dir, Implicit: true, Scope: registry.ReleaseScopeRoute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyRelease(ctx, current, false); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ApplyRelease(ctx, stale, false)
	if !errors.Is(err, registry.ErrReleasePlanChanged) || got.Items != nil {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

func mustPlanRelease(t *testing.T, svc *Service, ctx context.Context, req ReleaseRequest) ReleaseResponse {
	t.Helper()
	result, _, err := svc.PlanRelease(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertExactJSONKeys(t *testing.T, value any, want ...string) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != len(want) {
		t.Fatalf("JSON keys=%v want=%v: %s", reflect.ValueOf(object).MapKeys(), want, payload)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Fatalf("JSON omitted %q (want exact keys %v): %s", key, want, payload)
		}
	}
}

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

func TestHostConflictErrorQuotesCleanupPathForPOSIXShell(t *testing.T) {
	ownerPath := "/tmp/owner's $HOME `touch pwned` $(touch pwned)\nnext line"
	got := (&HostConflictError{Host: "audit.lewp", OwnerPath: ownerPath}).Error()
	want := "host audit.lewp is already assigned to \"/tmp/owner's $HOME `touch pwned` $(touch pwned)\\nnext line\"\n" +
		"Free it:        lewp release --path '/tmp/owner'\\''s $HOME `touch pwned` $(touch pwned)\nnext line' --route --forget\n" +
		"Use another:    lewp lease --host <name>.lewp\n" +
		"Suffix anyway:  lewp lease --host audit.lewp --auto-suffix"
	if got != want {
		t.Fatalf("HostConflictError.Error() mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestServiceExplicitHostConflictFailsByDefault(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	firstDir := filepath.Join(t.TempDir(), "first owner")
	if err := os.Mkdir(firstDir, 0o755); err != nil {
		t.Fatal(err)
	}
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
	cleanup := fmt.Sprintf("lewp release --path '%s' --route --forget", firstDir)
	if !strings.Contains(msg, cleanup) || strings.Contains(msg, "cd ") || !strings.Contains(msg, "--auto-suffix") {
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

func TestServiceResetClearsRememberedOverrideKeepingPort(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	// Set a host override, then confirm it is remembered by a later plain lease.
	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1", Host: "custom.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	remembered, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if remembered.Host != "custom.lewp" {
		t.Fatalf("override not remembered before reset: host=%q", remembered.Host)
	}

	// --reset re-resolves from flags/inference: the custom host is cleared back to
	// the deterministic default, the same port is kept (lease reused, not recreated),
	// and a warning names the cleared override.
	reset, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Host != "feature-1.audit.lewp" {
		t.Fatalf("reset did not clear host override: host=%q", reset.Host)
	}
	if reset.Port != first.Port {
		t.Fatalf("reset changed the port: first=%d reset=%d", first.Port, reset.Port)
	}
	if reset.LeaseState != "reused" {
		t.Fatalf("reset recreated the lease instead of reusing it: state=%q", reset.LeaseState)
	}
	if len(reset.Warnings) == 0 || !strings.Contains(strings.Join(reset.Warnings, " "), "custom.lewp") {
		t.Fatalf("reset warning missing cleared override: %+v", reset.Warnings)
	}

	// A later plain lease must NOT resurrect the old override: it is gone for good.
	after, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Host != "feature-1.audit.lewp" {
		t.Fatalf("override came back after reset: host=%q", after.Host)
	}
	if after.Port != first.Port {
		t.Fatalf("port not stable after reset: first=%d after=%d", first.Port, after.Port)
	}
}

func TestServiceResetClearsRememberedRootAndName(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "audit", "feature-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "custom-root", Name: "custom-name"})
	if err != nil {
		t.Fatal(err)
	}
	remembered, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if remembered.Host != "custom-name.custom-root.lewp" {
		t.Fatalf("root/name override not remembered before reset: host=%q", remembered.Host)
	}

	reset, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Root != "audit" || reset.Name != "feature-1" || reset.Host != "feature-1.audit.lewp" {
		t.Fatalf("reset did not clear root/name override: %+v", reset)
	}
	if reset.Port != first.Port {
		t.Fatalf("reset changed the port: first=%d reset=%d", first.Port, reset.Port)
	}
	warnings := strings.Join(reset.Warnings, " ")
	if !strings.Contains(warnings, "root custom-root -> audit") || !strings.Contains(warnings, "name custom-name -> feature-1") {
		t.Fatalf("reset warning missing cleared root/name: %+v", reset.Warnings)
	}
}

func TestServiceResetOnCleanDirectoryIsSilentNoOp(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	// Reset with nothing remembered leases normally and prints no reset warning.
	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "audit", Name: "feature-1", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Host != "feature-1.audit.lewp" || lease.Port == 0 {
		t.Fatalf("reset on clean dir produced bad lease: %+v", lease)
	}
	for _, w := range lease.Warnings {
		if strings.Contains(w, "reset remembered identity") {
			t.Fatalf("reset on clean dir emitted a spurious reset warning: %q", w)
		}
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

// TestDefaultPathsFailWithoutHome confirms the registry and socket default
// paths return an error rather than a cwd-relative fallback ("registry.sqlite")
// when the home directory cannot be determined, so a relative registry path cannot
// let the CLI and daemon operate on different SQLite files.
func TestDefaultPathsFailWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	for name, fn := range map[string]func() (string, error){
		"DefaultRegistryPath": DefaultRegistryPath,
		"DefaultSocketPath":   DefaultSocketPath,
	} {
		got, err := fn()
		if err == nil {
			t.Fatalf("%s returned %q, want error when home is unavailable", name, got)
		}
		if got != "" {
			t.Fatalf("%s returned non-empty path %q alongside error", name, got)
		}
		if !strings.Contains(err.Error(), "cannot determine home directory") {
			t.Fatalf("%s error = %q, want it to mention home directory", name, err)
		}
	}
}
