package registry

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
)

func TestMigrateCreatesRouteAliasSchema(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for _, table := range []string{"routes", "route_hosts", "leases", "ports", "events"} {
		assertTableExists(t, db, table)
	}

	assertColumn(t, db, "routes", "normalized_name")
	assertColumn(t, db, "route_hosts", "host_type")
	assertColumn(t, db, "leases", "route_id")
	assertColumn(t, db, "ports", "normalized_name")
	assertColumn(t, db, "ports", "state")

	for _, old := range []string{"identities"} {
		assertTableMissing(t, db, old)
	}
}

func TestMigrateResetsOldSchema(t *testing.T) {
	path := t.TempDir() + "/registry.sqlite"
	oldDB, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldDB.Exec(`
create table identities (
	id integer primary key autoincrement,
	root text not null,
	name text not null,
	normalized_root text not null,
	normalized_name text not null,
	host text not null default '',
	host_kind text not null default '',
	host_source text not null default '',
	path text not null,
	kind text not null,
	created_at text not null,
	updated_at text not null
);
insert into identities(root, name, normalized_root, normalized_name, host, host_kind, host_source, path, kind, created_at, updated_at)
values('old', 'app', 'old', 'app', 'app.old.lewp', 'instance', 'inferred', '/tmp/old', 'route', 'now', 'now');
pragma user_version=1;
`); err != nil {
		_ = oldDB.Close()
		t.Fatal(err)
	}
	if err := oldDB.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertTableMissing(t, store.db, "identities")
	for _, table := range []string{"routes", "route_hosts", "leases", "ports", "events"} {
		assertTableExists(t, store.db, table)
	}
	assertUserVersion(t, store.db, schemaVersion)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertTableMissing(t, reopened.db, "identities")
	for _, table := range []string{"routes", "route_hosts", "leases", "ports", "events"} {
		assertTableExists(t, reopened.db, table)
	}
	assertUserVersion(t, reopened.db, schemaVersion)
}

func TestSQLiteForeignKeysEnabled(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, err := db.Exec(`insert into route_hosts(route_id, host, host_type, source, created_at, updated_at) values(?, ?, ?, ?, ?, ?)`,
		999, "missing.example.lewp", HostTypeAlias, "test", "now", "now")
	if err == nil {
		t.Fatal("insert with nonexistent route_id succeeded; foreign keys are disabled")
	}
}

func TestReleaseForgetDeletesRouteDependents(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)

	lease, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "select count(*) from route_hosts where route_id=?", lease.RouteID); got != 1 {
		t.Fatalf("route_hosts before forget = %d, want 1", got)
	}
	if got := countRows(t, store.db, "select count(*) from leases where route_id=?", lease.RouteID); got != 1 {
		t.Fatalf("leases before forget = %d, want 1", got)
	}

	released, err := store.Release(ctx, ident.Path, identity.KindRoute, ident.NormalizedName, true)
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if got := countRows(t, store.db, "select count(*) from routes where id=?", lease.RouteID); got != 0 {
		t.Fatalf("routes after forget = %d, want 0", got)
	}
	if got := countRows(t, store.db, "select count(*) from route_hosts where route_id=?", lease.RouteID); got != 0 {
		t.Fatalf("route_hosts after forget = %d, want 0", got)
	}
	if got := countRows(t, store.db, "select count(*) from leases where route_id=?", lease.RouteID); got != 0 {
		t.Fatalf("leases after forget = %d, want 0", got)
	}
}

func TestBarePortLeaseDoesNotUseCollidingRouteID(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	routeIdent := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	routeLease, err := store.Lease(ctx, routeIdent, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	portIdent := testIdentity(t, "", identity.KindPort)
	portIdent.Name = "vite"
	portIdent.NormalizedName = "vite"
	portLease, err := store.Lease(ctx, portIdent, PortRange{Start: 41011, End: 41020})
	if err != nil {
		t.Fatal(err)
	}
	if portLease.ID != routeLease.RouteID {
		t.Fatalf("test setup did not create colliding route/port ids: route=%d port=%d", routeLease.RouteID, portLease.ID)
	}
	if portLease.RouteID != 0 {
		t.Fatalf("bare port exposed route id %d, want 0", portLease.RouteID)
	}
	if got, err := store.Identity(ctx, portLease.RouteID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("bare port route lookup returned identity=%+v err=%v", got, err)
	}
}

func TestRememberRejectsDuplicateActivePortAcrossRoutesAndPorts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first identity.Kind
		next  identity.Kind
	}{
		{name: "route_then_port", first: identity.KindRoute, next: identity.KindPort},
		{name: "port_then_route", first: identity.KindPort, next: identity.KindRoute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			defer store.Close()
			ctx := context.Background()
			port := 43123

			if err := store.Remember(ctx, testIdentityForKind(t, tc.first), port); err != nil {
				t.Fatal(err)
			}
			if err := store.Remember(ctx, testIdentityForKind(t, tc.next), port); err == nil {
				t.Fatal("allowed duplicate active port across route and bare port")
			}
		})
	}
}

func TestMovePathAllowsReleasedDestinationHistory(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	src := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	dest := testIdentity(t, "old.audit.lewp", identity.KindRoute)

	lease, err := store.Lease(ctx, src, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lease(ctx, dest, PortRange{Start: 41011, End: 41020}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, dest.Path, identity.KindRoute, dest.NormalizedName, false); err != nil {
		t.Fatal(err)
	}

	moved, err := store.MovePath(ctx, src.Path, dest.Path, identity.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].Port != lease.Port || moved[0].Host != src.Host {
		t.Fatalf("moved=%+v lease=%+v", moved, lease)
	}
}

func TestOpenHandlesConcurrentStartup(t *testing.T) {
	path := t.TempDir() + "/registry.sqlite"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- store.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLeaseRouteReusesStablePort(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)

	first, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if first.Port != second.Port {
		t.Fatalf("port changed: %d -> %d", first.Port, second.Port)
	}
	if second.RouteID != first.RouteID {
		t.Fatalf("identity changed: %d -> %d", first.RouteID, second.RouteID)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(first.Port)))
	if err != nil {
		t.Fatalf("could not occupy leased port %d: %v", first.Port, err)
	}
	defer ln.Close()

	third, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if third.Port != first.Port {
		t.Fatalf("running app port not reused: %d -> %d", first.Port, third.Port)
	}
}

func TestLeaseSkipsBusyPortAndBarePortHasNoHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	store := openTestStore(t)
	ctx := context.Background()
	ident := testIdentity(t, "", identity.KindPort)
	ident.Name = "vite"
	ident.NormalizedName = "vite"
	ident.HostKind = ""
	ident.HostSource = identity.SourceCLI

	lease, err := store.Lease(ctx, ident, PortRange{Start: busy, End: busy + 2})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Port == busy {
		t.Fatalf("allocated busy port %d", busy)
	}

	records, err := store.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != identity.KindPort || records[0].Host != "" || records[0].Port != lease.Port {
		t.Fatalf("bare port listed incorrectly: %+v", records)
	}
}

func TestLeaseReusesReleasedPortForSameIdentity(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)

	first, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, ident.Path, identity.KindRoute, ident.NormalizedName, false); err != nil {
		t.Fatal(err)
	}
	second, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if second.Port != first.Port {
		t.Fatalf("released port not reused: %d -> %d", first.Port, second.Port)
	}
}

func TestLeaseRejectsActiveHostOwnedByAnotherIdentity(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	first := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	second := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	second.NormalizedName = "feature-two"
	second.Name = "feature-two"

	if _, err := store.Lease(ctx, first, PortRange{Start: 41000, End: 41010}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lease(ctx, second, PortRange{Start: 41000, End: 41010}); err == nil {
		t.Fatal("allowed active host collision")
	}
}

func TestConcurrentLeasesAllocateUniquePorts(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	const n = 12
	portRange := PortRange{Start: 41200, End: 41299}

	var wg sync.WaitGroup
	ports := make([]int, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ident := identity.Result{
				Root:           "audit",
				Name:           "svc-" + strconv.Itoa(i),
				NormalizedRoot: "audit",
				NormalizedName: "svc-" + strconv.Itoa(i),
				Path:           t.TempDir(),
				Kind:           identity.KindPort,
				HostSource:     identity.SourceCLI,
			}
			lease, err := store.Lease(ctx, ident, portRange)
			errs[i] = err
			ports[i] = lease.Port
		}(i)
	}
	wg.Wait()

	seen := map[int]int{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("lease %d failed: %v", i, errs[i])
		}
		if prev, dup := seen[ports[i]]; dup {
			t.Fatalf("port %d allocated to both lease %d and lease %d", ports[i], prev, i)
		}
		seen[ports[i]] = i
	}
}

func TestLeasePersistsAcrossCloseAndReopen(t *testing.T) {
	path := t.TempDir() + "/registry.sqlite"
	ctx := context.Background()
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Lease(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	route, ok, err := reopened.RouteByHost(ctx, "feature.audit.lewp")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("lease did not persist across reopen")
	}
	if route.Port != lease.Port {
		t.Fatalf("persisted port changed: %d -> %d", lease.Port, route.Port)
	}
	if route.State != StateActive {
		t.Fatalf("persisted state = %q, want %q", route.State, StateActive)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store := openTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	return store.db
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir() + "/registry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var name string
	err := db.QueryRow("select name from sqlite_master where type='table' and name=?", table).Scan(&name)
	if err != nil {
		t.Fatalf("table %s missing: %v", table, err)
	}
}

func assertTableMissing(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var name string
	err := db.QueryRow("select name from sqlite_master where type='table' and name=?", table).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("table %s still exists or query failed: name=%q err=%v", table, name, err)
	}
}

func assertUserVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("pragma user_version").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var got int
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query("pragma table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return
		}
	}
	t.Fatalf("column %s.%s missing", table, column)
}

func testIdentity(t *testing.T, host string, kind identity.Kind) identity.Result {
	t.Helper()
	dir := t.TempDir()
	return identity.Result{
		Root:           "audit",
		Name:           "feature",
		NormalizedRoot: "audit",
		NormalizedName: "feature",
		Host:           host,
		HostKind:       identity.HostKindInstance,
		HostSource:     identity.SourceInferred,
		Path:           dir,
		Kind:           kind,
	}
}

func testIdentityForKind(t *testing.T, kind identity.Kind) identity.Result {
	t.Helper()
	ident := testIdentity(t, "feature.audit.lewp", kind)
	if kind == identity.KindPort {
		ident.Host = ""
		ident.HostKind = ""
		ident.HostSource = ""
		ident.Name = "vite"
		ident.NormalizedName = "vite"
	}
	return ident
}
