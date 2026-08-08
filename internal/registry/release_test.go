package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

func TestPlanReleaseRejectsInvalidSelectorsWithoutMutation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	lease, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 42900, End: 42920})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		selector ReleaseSelector
	}{
		{name: "invalid type", selector: ReleaseSelector{Type: ReleaseSelectorType("invalid")}},
		{name: "path missing path", selector: ReleaseSelector{Type: ReleaseSelectorPath, Scope: ReleaseScopeAll}},
		{name: "path invalid scope", selector: ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScope("invalid")}},
		{name: "path all with name", selector: ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll, Name: "app"}},
		{name: "path route with name", selector: ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeRoute, Name: "app"}},
		{name: "path name missing name", selector: ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeName}},
		{name: "path with host", selector: ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Host: "app.work.lewp", Scope: ReleaseScopeAll}},
		{name: "path with port", selector: ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Port: lease.Port, Scope: ReleaseScopeAll}},
		{name: "host missing host", selector: ReleaseSelector{Type: ReleaseSelectorHost}},
		{name: "host with path", selector: ReleaseSelector{Type: ReleaseSelectorHost, Host: "app.work.lewp", Path: dir}},
		{name: "host with port", selector: ReleaseSelector{Type: ReleaseSelectorHost, Host: "app.work.lewp", Port: lease.Port}},
		{name: "host recursive", selector: ReleaseSelector{Type: ReleaseSelectorHost, Host: "app.work.lewp", Recursive: true}},
		{name: "host with scope", selector: ReleaseSelector{Type: ReleaseSelectorHost, Host: "app.work.lewp", Scope: ReleaseScopeAll}},
		{name: "host with name", selector: ReleaseSelector{Type: ReleaseSelectorHost, Host: "app.work.lewp", Name: "app"}},
		{name: "port missing port", selector: ReleaseSelector{Type: ReleaseSelectorPort}},
		{name: "port negative", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: -1}},
		{name: "port too high", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: 65536}},
		{name: "port with path", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: lease.Port, Path: dir}},
		{name: "port with host", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: lease.Port, Host: "app.work.lewp"}},
		{name: "port recursive", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: lease.Port, Recursive: true}},
		{name: "port with scope", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: lease.Port, Scope: ReleaseScopeRoute}},
		{name: "port with name", selector: ReleaseSelector{Type: ReleaseSelectorPort, Port: lease.Port, Name: "app"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.PlanRelease(ctx, tc.selector, true); err == nil {
				t.Fatal("PlanRelease accepted invalid selector")
			}
			if _, err := store.ApplyRelease(ctx, ReleasePlan{Selector: tc.selector, Forget: true}); err == nil {
				t.Fatal("ApplyRelease accepted invalid selector")
			}
			if got := countRows(t, store.db, `select count(*) from routes where id=?`, lease.RouteID); got != 1 {
				t.Fatalf("routes=%d want 1 after rejected selector", got)
			}
			if got := countRows(t, store.db, `select count(*) from leases where id=? and state=?`, lease.ID, StateActive); got != 1 {
				t.Fatalf("active leases=%d want 1 after rejected selector", got)
			}
		})
	}
}

func TestPlanReleaseRejectsInvalidPersistedStatesWithoutForgetting(t *testing.T) {
	cases := []struct {
		name       string
		create     func(context.Context, *Store, string) (int64, error)
		corrupt    func(*Store, int64) error
		rowTable   string
		identityIn string
	}{
		{
			name: "route lease",
			create: func(ctx context.Context, store *Store, path string) (int64, error) {
				lease, err := store.LeaseRoute(ctx, releaseRouteIdentity(path, "app", "app.work.lewp"), PortRange{Start: 42930, End: 42950})
				return lease.ID, err
			},
			corrupt: func(store *Store, id int64) error {
				_, err := store.db.Exec(`update leases set state='corrupt' where id=?`, id)
				return err
			},
			rowTable:   "leases",
			identityIn: "route",
		},
		{
			name: "bare port row",
			create: func(ctx context.Context, store *Store, path string) (int64, error) {
				lease, err := store.LeasePort(ctx, releasePortIdentity(path, "vite"), PortRange{Start: 42930, End: 42950})
				return lease.ID, err
			},
			corrupt: func(store *Store, id int64) error {
				_, err := store.db.Exec(`update ports set state='corrupt' where id=?`, id)
				return err
			},
			rowTable:   "ports",
			identityIn: "port",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			dir := t.TempDir()
			rowID, err := tc.create(ctx, store, dir)
			if err != nil {
				t.Fatal(err)
			}
			selector := ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}
			validPlan, err := store.PlanRelease(ctx, selector, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.corrupt(store, rowID); err != nil {
				t.Fatal(err)
			}

			assertInvalidStateError := func(label string, err error) {
				t.Helper()
				if err == nil {
					t.Fatalf("%s accepted corrupt persisted state", label)
				}
				for _, want := range []string{tc.identityIn, dir, fmt.Sprintf("row %d", rowID), `state "corrupt"`} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s error %q missing %q", label, err, want)
					}
				}
			}
			_, err = store.PlanRelease(ctx, selector, true)
			assertInvalidStateError("PlanRelease", err)
			_, err = store.ApplyRelease(ctx, validPlan)
			assertInvalidStateError("ApplyRelease", err)
			if got := countRows(t, store.db, fmt.Sprintf(`select count(*) from %s where id=? and state='corrupt'`, tc.rowTable), rowID); got != 1 {
				t.Fatalf("corrupt history rows=%d want 1 after rejected forget", got)
			}
		})
	}
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

func TestApplyReleaseKeepsAndForgetsAllBarePortVersions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	first, err := store.LeasePort(ctx, releasePortIdentity(dir, "vite"), PortRange{Start: 43030, End: 43040})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindPort, "vite", false); err != nil {
		t.Fatal(err)
	}
	second, err := store.LeasePort(ctx, releasePortIdentity(dir, "vite"), PortRange{Start: 43030, End: 43040})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("second lease reused history row %d", first.ID)
	}

	selector := ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeName, Name: "vite"}
	releasePlan, err := store.PlanRelease(ctx, selector, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(releasePlan.Items) != 1 || len(releasePlan.Items[0].PortRows) != 2 {
		t.Fatalf("release plan did not include both versions: %+v", releasePlan.Items)
	}
	result, err := store.ApplyRelease(ctx, releasePlan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Released != 1 || result.Forgotten != 0 {
		t.Fatalf("release result=%+v", result)
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and normalized_name=?`, dir, "vite"); got != 2 {
		t.Fatalf("port history rows=%d want 2 after release", got)
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and normalized_name=? and state=?`, dir, "vite", StateReleased); got != 2 {
		t.Fatalf("released history rows=%d want 2", got)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if got := countRows(t, store.db, `select count(*) from ports where id=?`, id); got != 1 {
			t.Fatalf("history row %d count=%d want 1 after release", id, got)
		}
	}

	forgetPlan, err := store.PlanRelease(ctx, selector, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(forgetPlan.Items) != 1 || len(forgetPlan.Items[0].PortRows) != 2 || !reflect.DeepEqual(forgetPlan.Items[0].Actions, []ReleaseAction{ReleaseActionForget}) {
		t.Fatalf("forget plan did not group released history: %+v", forgetPlan.Items)
	}
	result, err = store.ApplyRelease(ctx, forgetPlan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Released != 0 || result.Forgotten != 1 {
		t.Fatalf("forget result=%+v", result)
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and normalized_name=?`, dir, "vite"); got != 0 {
		t.Fatalf("port history rows=%d want 0 after forget", got)
	}
}

func TestPlanReleaseNonrecursivePathMatchesOnlyExactParent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(parent, "parent", "parent.work.lewp"), PortRange{Start: 43050, End: 43060}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(child, "child", "child.work.lewp"), PortRange{Start: 43050, End: 43060}); err != nil {
		t.Fatal(err)
	}

	exact, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: parent, Scope: ReleaseScopeRoute}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Items) != 1 || exact.Items[0].Path != parent || exact.Items[0].Name != "parent" {
		t.Fatalf("nonrecursive plan=%+v want exact parent only", exact.Items)
	}

	recursive, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: parent, Recursive: true, Scope: ReleaseScopeRoute}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recursive.Items) != 2 {
		t.Fatalf("recursive items=%d want 2: %+v", len(recursive.Items), recursive.Items)
	}
	if got := []string{recursive.Items[0].Path, recursive.Items[1].Path}; !reflect.DeepEqual(got, []string{parent, child}) {
		t.Fatalf("recursive paths=%v want parent and child", got)
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

func TestPlanReleaseRecursiveRootMatchesAbsoluteStoredPaths(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	root := t.TempDir()
	routePath := filepath.Join(root, "route")
	portPath := filepath.Join(root, "port")
	if _, err := store.LeaseRoute(ctx, releaseRouteIdentity(routePath, "app", "app.work.lewp"), PortRange{Start: 43131, End: 43139}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(portPath, "vite"), PortRange{Start: 43131, End: 43139}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: string(filepath.Separator), Recursive: true, Scope: ReleaseScopeAll}, false)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{routePath, portPath}
	sort.Strings(wantPaths)
	if got := []string{plan.Items[0].Path, plan.Items[1].Path}; !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("recursive root paths=%v want %v", got, wantPaths)
	}
	for _, item := range plan.Items {
		if !filepath.IsAbs(item.Path) {
			t.Fatalf("stored path %q is not absolute", item.Path)
		}
	}
}

func TestPlanReleaseNumericPortUsesOnlyCurrentOwner(t *testing.T) {
	cases := []struct {
		name       string
		createOld  func(context.Context, *Store, string, int) error
		releaseOld func(context.Context, *Store, string) error
		createNew  func(context.Context, *Store, string, int) error
		wantKind   identity.Kind
		wantName   string
	}{
		{
			name: "released route then current bare port",
			createOld: func(ctx context.Context, store *Store, path string, port int) error {
				return store.Remember(ctx, releaseRouteIdentity(path, "old-route", "old-route.work.lewp"), port)
			},
			releaseOld: func(ctx context.Context, store *Store, path string) error {
				_, err := store.Release(ctx, path, identity.KindRoute, "old-route", false)
				return err
			},
			createNew: func(ctx context.Context, store *Store, path string, port int) error {
				return store.Remember(ctx, releasePortIdentity(path, "new-port"), port)
			},
			wantKind: identity.KindPort,
			wantName: "new-port",
		},
		{
			name: "released bare port then current route",
			createOld: func(ctx context.Context, store *Store, path string, port int) error {
				return store.Remember(ctx, releasePortIdentity(path, "old-port"), port)
			},
			releaseOld: func(ctx context.Context, store *Store, path string) error {
				_, err := store.Release(ctx, path, identity.KindPort, "old-port", false)
				return err
			},
			createNew: func(ctx context.Context, store *Store, path string, port int) error {
				return store.Remember(ctx, releaseRouteIdentity(path, "new-route", "new-route.work.lewp"), port)
			},
			wantKind: identity.KindRoute,
			wantName: "new-route",
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			root := t.TempDir()
			oldPath := filepath.Join(root, "old")
			newPath := filepath.Join(root, "new")
			port := 43161 + i
			if err := tc.createOld(ctx, store, oldPath, port); err != nil {
				t.Fatal(err)
			}
			if err := tc.releaseOld(ctx, store, oldPath); err != nil {
				t.Fatal(err)
			}

			historical, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPort, Port: port}, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(historical.Items) != 0 || len(historical.Fingerprint) != 0 {
				t.Fatalf("released historical owner matched numeric port: %+v", historical)
			}

			if err := tc.createNew(ctx, store, newPath, port); err != nil {
				t.Fatal(err)
			}
			current, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPort, Port: port}, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(current.Items) != 1 || current.Items[0].Path != newPath || current.Items[0].Kind != tc.wantKind || current.Items[0].Name != tc.wantName {
				t.Fatalf("current owner plan=%+v", current)
			}
		})
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

func TestApplyReleaseForgetsReactivatedRouteHistory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43230, End: 43250})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, route.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindRoute, "app", false); err != nil {
		t.Fatal(err)
	}
	reactivated, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43230, End: 43250})
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.ID == route.ID {
		t.Fatalf("reactivated lease reused row ID %d", route.ID)
	}

	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeRoute}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || len(plan.Items[0].Leases) != 2 {
		t.Fatalf("reactivated route history not grouped: %+v", plan.Items)
	}
	if got := []string{plan.Items[0].Leases[0].State, plan.Items[0].Leases[1].State}; !reflect.DeepEqual(got, []string{StateReleased, StateActive}) {
		t.Fatalf("lease history states=%v", got)
	}
	result, err := store.ApplyRelease(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Released != 1 || result.Forgotten != 1 {
		t.Fatalf("result=%+v", result)
	}
	assertRouteHistoryDeleted(t, store, route.RouteID)
}

func TestApplyReleaseForgetsReleasedOnlyRouteHistory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43260, End: 43280})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, route.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindRoute, "app", false); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, `select count(*) from events where route_id=?`, route.RouteID); got < 2 {
		t.Fatalf("events=%d want released route history before forget", got)
	}

	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeRoute}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || plan.Items[0].RegistryState != StateReleased || !reflect.DeepEqual(plan.Items[0].Actions, []ReleaseAction{ReleaseActionForget}) {
		t.Fatalf("released-only plan=%+v", plan)
	}
	result, err := store.ApplyRelease(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Released != 0 || result.Forgotten != 1 {
		t.Fatalf("result=%+v", result)
	}
	assertRouteHistoryDeleted(t, store, route.RouteID)
}

func assertRouteHistoryDeleted(t *testing.T, store *Store, routeID int64) {
	t.Helper()
	for table, column := range map[string]string{"routes": "id", "route_hosts": "route_id", "leases": "route_id", "events": "route_id"} {
		if got := countRows(t, store.db, fmt.Sprintf("select count(*) from %s where %s=?", table, column), routeID); got != 0 {
			t.Fatalf("%s rows=%d want 0", table, got)
		}
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

func TestApplyReleaseRejectsChangedRouteHostSetWithoutMutation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43540, End: 43560})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "keep"), PortRange{Start: 43540, End: 43560}); err != nil {
		t.Fatal(err)
	}
	stale, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: "app.work.lewp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, route.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}
	pathSelector := ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}
	before, err := store.PlanRelease(ctx, pathSelector, true)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRelease(ctx, stale); !errors.Is(err, ErrReleasePlanChanged) {
		t.Fatalf("err=%v want ErrReleasePlanChanged", err)
	}
	after, err := store.PlanRelease(ctx, pathSelector, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Fingerprint, after.Fingerprint) {
		t.Fatalf("stale apply mutated allocations: before=%+v after=%+v", before.Items, after.Items)
	}
	if len(after.Items) != 2 || len(after.Items[0].Hosts) != 2 {
		t.Fatalf("current allocations or added host not preserved: %+v", after.Items)
	}
	alias, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorHost, Host: "tags.app.work.lewp"}, false)
	if err != nil || len(alias.Items) != 1 || alias.Items[0].RouteID != route.RouteID {
		t.Fatalf("added alias not preserved: plan=%+v err=%v", alias, err)
	}
}

func TestApplyReleaseRejectsChangedReleasedBarePortHistoryWithoutMutation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43570, End: 43590})
	if err != nil {
		t.Fatal(err)
	}
	keep, err := store.LeasePort(ctx, releasePortIdentity(dir, "keep"), PortRange{Start: 43570, End: 43590})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "old"), PortRange{Start: 43570, End: 43590}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindPort, "old", false); err != nil {
		t.Fatal(err)
	}
	selector := ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}
	stale, err := store.PlanRelease(ctx, selector, false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "old"), PortRange{Start: 43570, End: 43590}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dir, identity.KindPort, "old", false); err != nil {
		t.Fatal(err)
	}
	before, err := store.PlanRelease(ctx, selector, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and normalized_name=? and state=?`, dir, "old", StateReleased); got != 2 {
		t.Fatalf("released old history rows=%d want 2 before stale apply", got)
	}

	if _, err := store.ApplyRelease(ctx, stale); !errors.Is(err, ErrReleasePlanChanged) {
		t.Fatalf("err=%v want ErrReleasePlanChanged", err)
	}
	after, err := store.PlanRelease(ctx, selector, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Fingerprint, after.Fingerprint) {
		t.Fatalf("stale apply mutated allocations: before=%+v after=%+v", before.Items, after.Items)
	}
	if got := countRows(t, store.db, `select count(*) from leases where id=? and state=?`, route.ID, StateActive); got != 1 {
		t.Fatalf("route active rows=%d want 1", got)
	}
	if got := countRows(t, store.db, `select count(*) from ports where id=? and state=?`, keep.ID, StateActive); got != 1 {
		t.Fatalf("keep active rows=%d want 1", got)
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and normalized_name=? and state=?`, dir, "old", StateReleased); got != 2 {
		t.Fatalf("released old history rows=%d want 2 after rejection", got)
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

func TestJoinReleaseRollbackError(t *testing.T) {
	original := errors.New("original failure")
	rollback := errors.New("rollback failure")

	joined := joinReleaseRollbackError(original, rollback, false, "apply release")
	if !errors.Is(joined, original) || !errors.Is(joined, rollback) {
		t.Fatalf("joined error=%v does not preserve both failures", joined)
	}
	if !strings.Contains(joined.Error(), "apply release transaction rollback") {
		t.Fatalf("joined error=%q missing rollback context", joined)
	}

	notCommitted := joinReleaseRollbackError(original, sql.ErrTxDone, false, "plan release")
	if !errors.Is(notCommitted, original) || !errors.Is(notCommitted, sql.ErrTxDone) {
		t.Fatalf("pre-commit ErrTxDone was incorrectly ignored: %v", notCommitted)
	}
	committed := joinReleaseRollbackError(nil, sql.ErrTxDone, true, "plan release")
	if committed != nil {
		t.Fatalf("post-commit ErrTxDone=%v want nil", committed)
	}
}

func TestApplyReleaseForgetFailureRollsBackWithContext(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	route, err := store.LeaseRoute(ctx, releaseRouteIdentity(dir, "app", "app.work.lewp"), PortRange{Start: 43380, End: 43399})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeasePort(ctx, releasePortIdentity(dir, "vite"), PortRange{Start: 43380, End: 43399}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanRelease(ctx, ReleaseSelector{Type: ReleaseSelectorPath, Path: dir, Scope: ReleaseScopeAll}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(fmt.Sprintf(`create trigger fail_route_forget before delete on routes when old.id=%d begin select raise(abort, 'injected forget failure'); end`, route.RouteID)); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyRelease(ctx, plan)
	if err == nil {
		t.Fatal("forget succeeded despite injected delete failure")
	}
	for _, want := range []string{`forget route "app"`, dir, "delete route history", "injected forget failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("apply error %q missing %q", err, want)
		}
	}
	if got := countRows(t, store.db, `select count(*) from routes where id=?`, route.RouteID); got != 1 {
		t.Fatalf("route rows=%d want 1 after rollback", got)
	}
	if got := countRows(t, store.db, `select count(*) from leases where route_id=? and state=?`, route.RouteID, StateActive); got != 1 {
		t.Fatalf("active route leases=%d want 1 after rollback", got)
	}
	if got := countRows(t, store.db, `select count(*) from events where route_id=?`, route.RouteID); got == 0 {
		t.Fatal("route events were not restored by rollback")
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and state=?`, dir, StateActive); got != 1 {
		t.Fatalf("active bare ports=%d want 1 after rollback", got)
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
	} else {
		for _, want := range []string{`release port "two"`, dir, "injected apply failure"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("apply error %q missing %q", err, want)
			}
		}
	}
	if got := countRows(t, store.db, `select count(*) from ports where path=? and state=?`, dir, StateActive); got != 2 {
		t.Fatalf("active rows=%d want 2 after rollback", got)
	}
}
