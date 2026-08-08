package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
)

func releaseRouteIdentity(path, name, host string) identity.Result {
	return identity.Result{Root: "work", Name: name, NormalizedRoot: "work", NormalizedName: name,
		Host: host, HostKind: identity.HostKindInstance, HostSource: identity.SourceCLI,
		Path: path, Kind: identity.KindRoute}
}

func releasePortIdentity(path, name string) identity.Result {
	return identity.Result{Name: name, NormalizedName: name, Path: path, Kind: identity.KindPort}
}

func TestPlanReleaseGroupsRoutesAndPortHistoryAtPath(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43000, End: 43020})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, route.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, route.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	first, err := store.LeasePort(ctx, releasePortIdentity(dir, "vite"), PortRange{Start: 43000, End: 43020})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindPort, "vite", false); err != nil {
		t.Fatal(err)
	}
	second, err := store.LeasePort(ctx, releasePortIdentity(dir, "vite"), PortRange{Start: 43000, End: 43020})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("items=%d want 2: %+v", len(plan.Items), plan.Items)
	}
	if plan.Items[0].Kind != identity.KindRoute || len(plan.Items[0].Hosts) != 3 || len(plan.Items[0].Leases) != 1 {
		t.Fatalf("route not grouped: %+v", plan.Items[0])
	}
	if plan.Items[1].Kind != identity.KindPort || len(plan.Items[1].PortRows) != 2 {
		t.Fatalf("port history not grouped: %+v", plan.Items[1])
	}
	wantPorts := []int{first.Port}
	if second.Port != first.Port {
		wantPorts = append(wantPorts, second.Port)
	}
	sort.Ints(wantPorts)
	if !reflect.DeepEqual(plan.Items[1].Ports, wantPorts) {
		t.Fatalf("ports=%v want unique %v", plan.Items[1].Ports, wantPorts)
	}
}

func TestPlanReleaseSelectorsRespectBoundariesAndCurrentOwnership(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	app := filepath.Join(root, "app")
	child := filepath.Join(app, "child")
	application := filepath.Join(root, "application")
	for i, path := range []string{app, child, application} {
		if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(path, fmt.Sprintf("app-%d", i), fmt.Sprintf("app-%d.work.lewp", i)), PortRange{Start: 43100, End: 43130}); err != nil {
			t.Fatal(err)
		}
	}
	recursive, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: app, Recursive: true, Scope: ReleaseScopeRoute}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{recursive.Items[0].Path, recursive.Items[1].Path}; !reflect.DeepEqual(got, []string{app, child}) {
		t.Fatalf("recursive paths=%v", got)
	}
	hostPlan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: "app-0.work.lewp"}, false)
	if err != nil || len(hostPlan.Items) != 1 || hostPlan.Items[0].Path != app {
		t.Fatalf("host plan=%+v err=%v", hostPlan, err)
	}
	active := hostPlan.Items[0].ActivePort
	if active == nil {
		t.Fatal("route has nil active port")
	}
	portPlan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPort, Port: *active}, false)
	if err != nil || len(portPlan.Items) != 1 || portPlan.Items[0].Path != app {
		t.Fatalf("port plan=%+v err=%v", portPlan, err)
	}
	if _, err := store.ApplyRelease(ctx, hostPlan); err != nil {
		t.Fatal(err)
	}
	historical, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: "app-0.work.lewp"}, true)
	if err != nil || len(historical.Items) != 0 {
		t.Fatalf("released host matched: %+v err=%v", historical, err)
	}
}

func TestPlanReleaseMatchesExactAliasWildcardAndBarePortOwners(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43140, End: 43160})
	if err != nil {
		t.Fatal(err)
	}
	for host, hostType := range map[string]string{"tags.app.work.lewp": HostTypeAlias, "*.app.work.lewp": HostTypeWildcard} {
		if _, _, err := store.AddRouteHost(ctx, route.RouteID, host, hostType, "cli"); err != nil {
			t.Fatal(err)
		}
	}
	bare, err := store.LeasePort(ctx, releasePortIdentity(dir, "vite"), PortRange{Start: 43140, End: 43160})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: " TAGS.APP.WORK.LEWP. "}, false)
	if err != nil || len(alias.Items) != 1 || alias.Items[0].Kind != identity.KindRoute {
		t.Fatalf("alias plan=%+v err=%v", alias, err)
	}
	wildcard, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: "*.APP.WORK.LEWP."}, false)
	if err != nil || len(wildcard.Items) != 1 || wildcard.Items[0].Kind != identity.KindRoute {
		t.Fatalf("wildcard plan=%+v err=%v", wildcard, err)
	}
	concrete, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: "foo.app.work.lewp"}, false)
	if err != nil || len(concrete.Items) != 0 {
		t.Fatalf("concrete host resolved through wildcard: plan=%+v err=%v", concrete, err)
	}
	port, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPort, Port: bare.Port}, false)
	if err != nil || len(port.Items) != 1 || port.Items[0].Kind != identity.KindPort || port.Items[0].Name != "vite" {
		t.Fatalf("bare-port owner plan=%+v err=%v", port, err)
	}
}

func TestPlanReleaseCanonicalizesLexicalPathsAndNames(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	stored := filepath.Join(root, "missing", "child")
	if _, err := store.LeasePort(ctx, releasePortIdentity(stored, "vite-dev"), PortRange{Start: 43170, End: 43180}); err != nil {
		t.Fatal(err)
	}
	selectorPath := filepath.Join(root, "missing", "other", "..", "child")
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: selectorPath, Scope: ReleaseScopeName, Name: "Vite Dev"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Selector.Path != stored || plan.Selector.Name != "vite-dev" || len(plan.Items) != 1 {
		t.Fatalf("canonical plan=%+v", plan)
	}
}

func TestApplyReleaseCountsActiveAndReleasedForgetTargets(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43200, End: 43220})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "active"), PortRange{Start: 43200, End: 43220}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "old"), PortRange{Start: 43200, End: 43220}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindPort, "old", false); err != nil {
		t.Fatal(err)
	}

	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyRelease(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Released != 2 || result.Forgotten != 3 {
		t.Fatalf("result=%+v", result)
	}
	if got := countRows(t, store.db, `select count(*) from routes where path=?`, dir); got != 0 {
		t.Fatalf("routes=%d", got)
	}
	if got := countRows(t, store.db, `select count(*) from route_hosts where route_id=?`, route.RouteID); got != 0 {
		t.Fatalf("route_hosts=%d", got)
	}
	if got := countRows(t, store.db, `select count(*) from leases where route_id=?`, route.RouteID); got != 0 {
		t.Fatalf("leases=%d", got)
	}
	if got := countRows(t, store.db, `select count(*) from events where route_id=?`, route.RouteID); got != 0 {
		t.Fatalf("events=%d", got)
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=?`, dir); got != 0 {
		t.Fatalf("ports=%d", got)
	}
}

func TestApplyReleaseRejectsChangedPlanButAllowsOutsideChanges(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	inScope := filepath.Join(root, "app")
	outside := filepath.Join(root, "other")
	if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(inScope, "app", "app.work.lewp"), PortRange{Start: 43300, End: 43330}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: inScope, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(outside, "vite"), PortRange{Start: 43300, End: 43330}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRelease(ctx, plan); err != nil {
		t.Fatalf("outside change invalidated plan: %v", err)
	}

	if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(inScope, "app", "app.work.lewp"), PortRange{Start: 43300, End: 43330}); err != nil {
		t.Fatal(err)
	}
	stale, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: inScope, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(inScope, "new"), PortRange{Start: 43300, End: 43330}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRelease(ctx, stale); !errors.Is(err, ErrReleasePlanChanged) {
		t.Fatalf("err=%v want ErrReleasePlanChanged", err)
	}
	active, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: inScope, Scope: ReleaseScopeAll}, false)
	if err != nil || len(active.Items) != 2 {
		t.Fatalf("partial mutation after rejection: %+v err=%v", active, err)
	}
}

func TestApplyReleaseWithoutForgetRetainsRouteHistory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43400, End: 43420})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeRoute}, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyRelease(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Released != 1 || result.Forgotten != 0 {
		t.Fatalf("result=%+v", result)
	}
	for table, want := range map[string]int{"routes": 1, "route_hosts": 1, "leases": 1, "events": 2} {
		column := "route_id"
		if table == "routes" {
			column = "id"
		}
		if got := countRows(t, store.db, fmt.Sprintf("select count(*) from %s where %s=?", table, column), route.RouteID); got != want {
			t.Fatalf("%s rows=%d want %d", table, got, want)
		}
	}
}

func TestApplyReleaseRejectsInScopeIdentityChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(context.Context, *Store, string) error
	}{
		{name: "released", mutate: func(ctx context.Context, store *Store, path string) error {
			_, err := store.Release(ctx, path, identity.KindRoute, "app", false)
			return err
		}},
		{name: "reactivated", mutate: func(ctx context.Context, store *Store, path string) error {
			if _, err := store.Release(ctx, path, identity.KindRoute, "app", false); err != nil {
				return err
			}
			_, err := store.LeaseRoute(ctx, releaseRouteIdentity(path, "app", "app.work.lewp"), PortRange{Start: 43500, End: 43530})
			return err
		}},
		{name: "forgotten", mutate: func(ctx context.Context, store *Store, path string) error {
			_, err := store.Release(ctx, path, identity.KindRoute, "app", true)
			return err
		}},
		{name: "reassigned", mutate: func(ctx context.Context, store *Store, path string) error {
			return store.Remember(ctx, releaseRouteIdentity(path, "app", "app.work.lewp"), 49999)
		}},
		{name: "replaced", mutate: func(ctx context.Context, store *Store, path string) error {
			if _, err := store.Release(ctx, path, identity.KindRoute, "app", true); err != nil {
				return err
			}
			_, err := store.LeaseRoute(ctx, releaseRouteIdentity(path, "app", "app.work.lewp"), PortRange{Start: 43500, End: 43530})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			dir := t.TempDir()
			if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43500, End: 43530}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "keep"), PortRange{Start: 43500, End: 43530}); err != nil {
				t.Fatal(err)
			}
			stale, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.mutate(ctx, store, dir); err != nil {
				t.Fatal(err)
			}
			before, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApplyRelease(ctx, stale); !errors.Is(err, ErrReleasePlanChanged) {
				t.Fatalf("err=%v want ErrReleasePlanChanged", err)
			}
			after, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, true)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Fingerprint, after.Fingerprint) {
				t.Fatalf("stale apply mutated allocations: before=%+v after=%+v", before.Items, after.Items)
			}
			if got := countRows(t, store.db, `select count(*) from ports where path=? and normalized_name=? and state=?`, dir, "keep", StateActive); got != 1 {
				t.Fatalf("keep active rows=%d want 1", got)
			}
		})
	}
}

func TestApplyReleaseIgnoresFilesystemAndTCPHealthChanges(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	lease, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43600, End: 43620})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", lease.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result, err := store.ApplyRelease(ctx, plan)
	if err != nil {
		t.Fatalf("derived health invalidated plan: %v", err)
	}
	if result.Released != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestConcurrentApplyAndInScopeLeaseAreSerializable(t *testing.T) {
	ctx := context.Background()
	registryPath := filepath.Join(t.TempDir(), "registry.sqlite")
	store, err := Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	dir := t.TempDir()
	if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43700, End: 43730}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var applyResult ReleaseApplyResult
	var applyErr error
	var leaseErr error
	go func() {
		defer wg.Done()
		<-start
		applyResult, applyErr = store.ApplyRelease(ctx, plan)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, leaseErr = other.LeasePort(ctx, releasePortIdentity(dir, "new"), PortRange{Start: 43700, End: 43730})
	}()
	close(start)
	wg.Wait()
	if leaseErr != nil {
		t.Fatalf("concurrent lease: %v", leaseErr)
	}
	active, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	if errors.Is(applyErr, ErrReleasePlanChanged) {
		if len(active.Items) != 2 {
			t.Fatalf("lease-first state has %d active items, want 2: %+v", len(active.Items), active.Items)
		}
		return
	}
	if applyErr != nil {
		t.Fatalf("apply error=%v", applyErr)
	}
	if applyResult.Released != 1 || len(active.Items) != 1 || active.Items[0].Kind != identity.KindPort || active.Items[0].Name != "new" {
		t.Fatalf("apply-first state result=%+v active=%+v", applyResult, active.Items)
	}
	if got := countRows(t, store.db, `select count(*) from leases l join routes r on r.id=l.route_id where r.path=? and l.state=?`, dir, StateActive); got != 0 {
		t.Fatalf("original route has %d active leases", got)
	}
}

func TestApplyReleaseRollsBackMultiItemFailure(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if _, err := store.LeasePort(ctx, releasePortIdentity(dir, name), PortRange{Start: 43400, End: 43420}); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`create trigger fail_second_port before update of state on ports when new.normalized_name='two' begin select raise(abort, 'injected apply failure'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRelease(ctx, plan); err == nil {
		t.Fatal("apply succeeded despite injected failure")
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and state=?`, dir, StateActive); got != 2 {
		t.Fatalf("active rows=%d want 2 after rollback", got)
	}
}
