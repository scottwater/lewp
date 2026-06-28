package registry

import (
	"context"
	"database/sql"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
)

func TestMigrateCreatesPlanSchema(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for _, table := range []string{"identities", "leases", "events"} {
		var name string
		err := db.QueryRow("select name from sqlite_master where type='table' and name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	assertColumn(t, db, "identities", "host_kind")
	assertColumn(t, db, "identities", "host_source")
	assertColumn(t, db, "identities", "kind")
	assertColumn(t, db, "leases", "last_seen_at")
	assertColumn(t, db, "leases", "released_at")
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
	if second.IdentityID != first.IdentityID {
		t.Fatalf("identity changed: %d -> %d", first.IdentityID, second.IdentityID)
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

	got, err := store.Identity(ctx, lease.IdentityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != identity.KindPort || got.Host != "" {
		t.Fatalf("bare port identity persisted incorrectly: %+v", got)
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
