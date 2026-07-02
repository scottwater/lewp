package registry

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestMigrateBacksUpAndLogsBeforeReset(t *testing.T) {
	path := t.TempDir() + "/registry.sqlite"

	// Populate a current-schema registry with a real route + lease.
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	if _, err := store.Lease(context.Background(), ident, PortRange{Start: 42000, End: 42010}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a schema bump / stale binary: an on-disk version below the binary.
	setUserVersionOnDisk(t, path, schemaVersion-1)

	var logBuf bytes.Buffer
	defer swapResetLog(&logBuf)()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	// The live registry was wiped...
	if got := countRows(t, reopened.db, "select count(*) from routes"); got != 0 {
		t.Fatalf("routes after reset = %d, want 0", got)
	}

	// ...but exactly one timestamped backup was written next to it...
	backups, err := filepath.Glob(path + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup files = %v, want exactly 1", backups)
	}

	// ...and that backup is an openable snapshot still holding the pre-reset route.
	backupDB, err := sql.Open("sqlite", dataSourceName(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	if got := countRows(t, backupDB, "select count(*) from routes"); got != 1 {
		t.Fatalf("routes in backup = %d, want 1", got)
	}
	assertUserVersion(t, backupDB, schemaVersion-1)

	// The reset was logged loudly and points at the backup.
	logged := logBuf.String()
	if !strings.Contains(logged, "RESETTING") {
		t.Fatalf("reset log missing loud warning: %q", logged)
	}
	if !strings.Contains(logged, backups[0]) {
		t.Fatalf("reset log %q does not mention backup path %q", logged, backups[0])
	}
}

func TestBackupBeforeResetDoesNotClobberExistingTimestampBackup(t *testing.T) {
	const stamp = "20260702T120000Z"
	path := t.TempDir() + "/registry.sqlite"
	firstBackup := backupPathForAttempt(path, stamp, 0)
	secondBackup := backupPathForAttempt(path, stamp, 1)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := os.WriteFile(firstBackup, []byte("previous backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer swapBackupNow(func() time.Time {
		return time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	})()

	gotBackup, err := store.backupBeforeReset()
	if err != nil {
		t.Fatal(err)
	}
	if gotBackup != secondBackup {
		t.Fatalf("backup path = %q, want %q", gotBackup, secondBackup)
	}
	if got := string(mustReadFile(t, firstBackup)); got != "previous backup" {
		t.Fatalf("first backup was clobbered: %q", got)
	}
	if _, err := os.Stat(secondBackup); err != nil {
		t.Fatalf("second backup missing: %v", err)
	}
}

func TestMigrateRefusesNewerSchemaRegistry(t *testing.T) {
	path := t.TempDir() + "/registry.sqlite"

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)
	if _, err := store.Lease(context.Background(), ident, PortRange{Start: 42000, End: 42010}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// A newer binary wrote this registry; the current binary must refuse it
	// rather than wipe data it does not understand (a downgrade).
	setUserVersionOnDisk(t, path, schemaVersion+1)

	var logBuf bytes.Buffer
	defer swapResetLog(&logBuf)()

	if _, err := Open(path); err == nil {
		t.Fatal("Open succeeded on a newer-versioned registry; want refusal")
	} else if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("error = %v, want mention of newer schema", err)
	}

	// Refusing must neither wipe, back up, nor log a reset.
	if logBuf.Len() != 0 {
		t.Fatalf("reset log wrote on refusal: %q", logBuf.String())
	}
	backups, err := filepath.Glob(path + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("backup files on refusal = %v, want none", backups)
	}
	raw, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if got := countRows(t, raw, "select count(*) from routes"); got != 1 {
		t.Fatalf("routes after refusal = %d, want 1 (data preserved)", got)
	}
}

func TestMigrateFreshRegistryDoesNotBackUpOrLog(t *testing.T) {
	path := t.TempDir() + "/registry.sqlite"

	var logBuf bytes.Buffer
	defer swapResetLog(&logBuf)()

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if logBuf.Len() != 0 {
		t.Fatalf("fresh Open logged a reset: %q", logBuf.String())
	}
	backups, err := filepath.Glob(path + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("fresh Open created backups: %v", backups)
	}
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

func TestEnsurePortAvailablePropagatesQueryErrors(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	// A failed count must surface the underlying error, not be silently treated
	// as "port occupied" (which previously masked DB/context failures).
	err = ensurePortAvailable(canceled, tx, 40000)
	if err == nil {
		t.Fatal("ensurePortAvailable returned nil for a canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error not propagated: got %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "already active") {
		t.Fatalf("query error masked as occupied port: %v", err)
	}
}

func TestConcurrentRememberRejectsDuplicateActivePort(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()
	const port = 43210

	routeIdent := testIdentityForKind(t, identity.KindRoute)
	portIdent := testIdentityForKind(t, identity.KindPort)

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ident := routeIdent
			if i%2 == 1 {
				ident = portIdent
			}
			errs[i] = store.Remember(ctx, ident, port)
		}(i)
	}
	wg.Wait()

	// The first writer to win the port succeeds; every later writer of the other
	// kind must fail the cross-table availability check. Regardless of
	// interleaving, exactly one active row may hold the port across both tables.
	succeeded := false
	for _, err := range errs {
		if err == nil {
			succeeded = true
		}
	}
	if !succeeded {
		t.Fatalf("no Remember succeeded: %v", errs)
	}
	got := countRows(t, store.db, `select
	(select count(*) from leases where port=? and state=?) +
	(select count(*) from ports where port=? and state=?)`, port, StateActive, port, StateActive)
	if got != 1 {
		t.Fatalf("active rows holding port %d = %d, want 1 (cross-table collision)", port, got)
	}
}

func TestConcurrentMovePathToSameDestinationKeepsOneRoute(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	srcA := testIdentity(t, "a.audit.lewp", identity.KindRoute)
	srcA.Name, srcA.NormalizedName = "a", "a"
	srcB := testIdentity(t, "b.audit.lewp", identity.KindRoute)
	srcB.Name, srcB.NormalizedName = "b", "b"
	if _, err := store.Lease(ctx, srcA, PortRange{Start: 41000, End: 41010}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lease(ctx, srcB, PortRange{Start: 41011, End: 41020}); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = store.MovePath(ctx, srcA.Path, dest, identity.KindRoute)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = store.MovePath(ctx, srcB.Path, dest, identity.KindRoute)
	}()
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful moves = %d, want exactly 1 (errs=%v)", successes, errs)
	}
	if got := countRows(t, store.db, `select count(*) from routes r join leases l on l.route_id=r.id where r.path=? and l.state=?`, dest, StateActive); got != 1 {
		t.Fatalf("active routes at destination = %d, want 1", got)
	}
	if got := countRows(t, store.db, `select count(*) from routes where path=?`, dest); got != 1 {
		t.Fatalf("route rows at destination = %d, want 1", got)
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

func TestLeaseRouteReusesStablePortAndPrimaryHost(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ident := testIdentity(t, "feature.audit.lewp", identity.KindRoute)

	first, err := store.LeaseRoute(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.LeaseRoute(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if first.Port != second.Port {
		t.Fatalf("port changed: %d -> %d", first.Port, second.Port)
	}
	if second.RouteID != first.RouteID {
		t.Fatalf("route changed: %d -> %d", first.RouteID, second.RouteID)
	}

	hosts, err := store.RouteHosts(ctx, first.RouteID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Host != "feature.audit.lewp" || hosts[0].HostType != HostTypePrimary {
		t.Fatalf("primary host not persisted: %+v", hosts)
	}
}

func TestPortLeaseSkipsBusyPortAndHasNoHost(t *testing.T) {
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

	lease, err := store.LeasePort(ctx, ident, PortRange{Start: busy, End: busy + 2})
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
	if len(records) != 1 || records[0].Kind != identity.KindPort || records[0].Host != "" {
		t.Fatalf("bare port listed incorrectly: %+v", records)
	}
}

func TestAddRouteHostExactAliasReusesRoutePort(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ident := testIdentity(t, "app.work.lewp", identity.KindRoute)
	lease, err := store.LeaseRoute(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	host, created, err := store.AddRouteHost(ctx, lease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if !created || host.Host != "tags.app.work.lewp" || host.HostType != HostTypeAlias {
		t.Fatalf("alias add=%+v created=%v", host, created)
	}

	route, ok, err := store.RouteByHost(ctx, "tags.app.work.lewp")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || route.Port != lease.Port || route.HostType != HostTypeAlias {
		t.Fatalf("alias lookup route=%+v ok=%v", route, ok)
	}
}

func TestWildcardRouteHostMatchesOneLabelOnly(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ident := testIdentity(t, "api.work.lewp", identity.KindRoute)
	lease, err := store.LeaseRoute(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, lease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"tags.app.work.lewp", "leads.app.work.lewp"} {
		route, ok, err := store.RouteByHost(ctx, host)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || route.Port != lease.Port || route.MatchedHost != "*.app.work.lewp" {
			t.Fatalf("wildcard lookup %s route=%+v ok=%v", host, route, ok)
		}
	}
	for _, host := range []string{"app.work.lewp", "foo.tags.app.work.lewp"} {
		if route, ok, err := store.RouteByHost(ctx, host); err != nil || ok {
			t.Fatalf("wildcard should not match %s: route=%+v ok=%v err=%v", host, route, ok, err)
		}
	}
}

func TestAddRouteHostIdempotentAndRouteHostsAliasesOnly(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	lease, err := store.LeaseRoute(ctx, testIdentity(t, "app.work.lewp", identity.KindRoute), PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	first, created, err := store.AddRouteHost(ctx, lease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first alias add was not created")
	}
	second, created, err := store.AddRouteHost(ctx, lease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("duplicate alias not idempotent: first=%+v second=%+v created=%v", first, second, created)
	}

	hosts, err := store.RouteHosts(ctx, lease.RouteID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].HostType == HostTypePrimary || hosts[0].Host != "tags.app.work.lewp" {
		t.Fatalf("aliases only listed incorrectly: %+v", hosts)
	}
}

func TestRemoveRouteHostDoesNotRemovePrimary(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	lease, err := store.LeaseRoute(ctx, testIdentity(t, "app.work.lewp", identity.KindRoute), PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, lease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveRouteHost(ctx, lease.RouteID, "app.work.lewp")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed primary host: %d", removed)
	}
	removed, err = store.RemoveRouteHost(ctx, lease.RouteID, "tags.app.work.lewp")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed alias = %d, want 1", removed)
	}

	hosts, err := store.RouteHosts(ctx, lease.RouteID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].HostType != HostTypePrimary {
		t.Fatalf("primary host not retained: %+v", hosts)
	}
}

func TestActiveRouteByPathReturnsPrimaryHost(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ident := testIdentity(t, "app.work.lewp", identity.KindRoute)
	lease, err := store.LeaseRoute(ctx, ident, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, lease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}

	route, ok, err := store.ActiveRouteByPath(ctx, ident.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || route.RouteID != lease.RouteID || route.Host != "app.work.lewp" || route.HostType != HostTypePrimary || route.Port != lease.Port {
		t.Fatalf("active route by path route=%+v ok=%v", route, ok)
	}
}

func TestRouteHostConflictsIncludeOwnerPath(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "tags.app.work.lewp", identity.KindRoute)
	second := testIdentity(t, "app.work.lewp", identity.KindRoute)
	second.Name = "app"
	second.NormalizedName = "app"

	if _, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010}); err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = store.AddRouteHost(ctx, secondLease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli")
	if err == nil {
		t.Fatal("expected wildcard conflict")
	}
	if !strings.Contains(err.Error(), "wildcard *.app.work.lewp would cover tags.app.work.lewp") || !strings.Contains(err.Error(), first.Path) {
		t.Fatalf("conflict missing covered host/path: %v", err)
	}
}

func TestAddRouteHostRejectsPrimaryHostType(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	lease, err := store.LeaseRoute(ctx, testIdentity(t, "app.work.lewp", identity.KindRoute), PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.AddRouteHost(ctx, lease.RouteID, "other.app.work.lewp", HostTypePrimary, "cli"); err == nil {
		t.Fatal("AddRouteHost accepted primary host type")
	}
}

func TestAddRouteHostRejectsExactAliasOwnedByAnotherRoute(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "app.work.lewp", identity.KindRoute)
	second := testIdentity(t, "api.work.lewp", identity.KindRoute)

	if _, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010}); err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = store.AddRouteHost(ctx, secondLease.RouteID, "app.work.lewp", HostTypeAlias, "cli")
	if err == nil {
		t.Fatal("expected exact alias conflict")
	}
	if !strings.Contains(err.Error(), first.Path) {
		t.Fatalf("conflict missing owner path: %v", err)
	}
}

func TestAddRouteHostNormalizesExactAliasBeforeConflictAndLookup(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "app.work.lewp", identity.KindRoute)
	second := testIdentity(t, "api.work.lewp", identity.KindRoute)

	if _, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010}); err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, secondLease.RouteID, "App.Work.Lewp.", HostTypeAlias, "cli"); err == nil {
		t.Fatal("expected normalized exact alias conflict")
	}

	host, created, err := store.AddRouteHost(ctx, secondLease.RouteID, "Tags.App.Work.Lewp.", HostTypeAlias, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if !created || host.Host != "tags.app.work.lewp" {
		t.Fatalf("host not normalized on insert: %+v created=%v", host, created)
	}
	secondHost, created, err := store.AddRouteHost(ctx, secondLease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if created || secondHost.ID != host.ID {
		t.Fatalf("normalized duplicate not idempotent: first=%+v second=%+v created=%v", host, secondHost, created)
	}
	route, ok, err := store.RouteByHost(ctx, "Tags.App.Work.Lewp.")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || route.Host != "tags.app.work.lewp" || route.MatchedHost != "tags.app.work.lewp" {
		t.Fatalf("normalized lookup route=%+v ok=%v", route, ok)
	}
	removed, err := store.RemoveRouteHost(ctx, secondLease.RouteID, "Tags.App.Work.Lewp.")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("normalized remove = %d, want 1", removed)
	}
}

func TestAddRouteHostRejectsExactAliasCoveredByOtherWildcard(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "api.work.lewp", identity.KindRoute)
	second := testIdentity(t, "other.work.lewp", identity.KindRoute)

	firstLease, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, firstLease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = store.AddRouteHost(ctx, secondLease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli")
	if err == nil {
		t.Fatal("expected wildcard-covered exact alias conflict")
	}
	if !strings.Contains(err.Error(), first.Path) {
		t.Fatalf("conflict missing owner path: %v", err)
	}
}

func TestAddRouteHostNormalizesWildcardBeforeConflictAndLookup(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "api.work.lewp", identity.KindRoute)
	second := testIdentity(t, "other.work.lewp", identity.KindRoute)

	firstLease, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	host, created, err := store.AddRouteHost(ctx, firstLease.RouteID, "*.App.Work.Lewp.", HostTypeWildcard, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if !created || host.Host != "*.app.work.lewp" {
		t.Fatalf("wildcard not normalized on insert: %+v created=%v", host, created)
	}
	route, ok, err := store.RouteByHost(ctx, "Tags.App.Work.Lewp.")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || route.MatchedHost != "*.app.work.lewp" {
		t.Fatalf("normalized wildcard lookup route=%+v ok=%v", route, ok)
	}
	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, secondLease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err == nil {
		t.Fatal("expected normalized duplicate wildcard conflict")
	}
}

func TestAddRouteHostRejectsDuplicateWildcardOwnedByAnotherRoute(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "api.work.lewp", identity.KindRoute)
	second := testIdentity(t, "other.work.lewp", identity.KindRoute)

	firstLease, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, firstLease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = store.AddRouteHost(ctx, secondLease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli")
	if err == nil {
		t.Fatal("expected duplicate wildcard conflict")
	}
	if !strings.Contains(err.Error(), first.Path) {
		t.Fatalf("conflict missing owner path: %v", err)
	}
}

func TestConcurrentAddRouteHostDuplicateIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	lease, err := store.LeaseRoute(ctx, testIdentity(t, "app.work.lewp", identity.KindRoute), PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	created := make([]bool, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, made, err := store.AddRouteHost(ctx, lease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli")
			errs[i] = err
			created[i] = made
		}(i)
	}
	wg.Wait()

	createdCount := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("alias add %d failed: %v", i, errs[i])
		}
		if created[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	if got := countRows(t, store.db, `select count(*) from route_hosts where route_id=? and host=?`, lease.RouteID, "tags.app.work.lewp"); got != 1 {
		t.Fatalf("alias row count = %d, want 1", got)
	}
}

func TestLeaseRouteRejectsReleasedAliasNowOwnedByAnotherRoute(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "app.work.lewp", identity.KindRoute)
	second := testIdentity(t, "api.work.lewp", identity.KindRoute)

	firstLease, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, firstLease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, first.Path, identity.KindRoute, first.NormalizedName, false); err != nil {
		t.Fatal(err)
	}

	secondLease, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, secondLease.RouteID, "tags.app.work.lewp", HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}

	_, err = store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010})
	if err == nil {
		t.Fatal("released route reclaimed alias owned by active route")
	}
	if !strings.Contains(err.Error(), "tags.app.work.lewp") || !strings.Contains(err.Error(), second.Path) {
		t.Fatalf("conflict missing alias or owner path: %v", err)
	}
}

func TestRememberRejectsReleasedWildcardNowCoveringAnotherRoute(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := testIdentity(t, "api.work.lewp", identity.KindRoute)
	second := testIdentity(t, "tags.app.work.lewp", identity.KindRoute)

	firstLease, err := store.LeaseRoute(ctx, first, PortRange{Start: 41000, End: 41010})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddRouteHost(ctx, firstLease.RouteID, "*.app.work.lewp", HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, first.Path, identity.KindRoute, first.NormalizedName, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LeaseRoute(ctx, second, PortRange{Start: 41000, End: 41010}); err != nil {
		t.Fatal(err)
	}

	err = store.Remember(ctx, first, 41019)
	if err == nil {
		t.Fatal("remembered route reclaimed wildcard covering active route")
	}
	if !strings.Contains(err.Error(), "*.app.work.lewp") || !strings.Contains(err.Error(), second.Path) {
		t.Fatalf("conflict missing wildcard or owner path: %v", err)
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

// setUserVersionOnDisk rewrites the registry's schema version through a raw
// connection, simulating a schema bump or a stale/newer binary having written
// the file.
func setUserVersionOnDisk(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("pragma user_version=%d", version)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// swapResetLog redirects the package reset warning to w and returns a function
// that restores the previous destination.
func swapResetLog(w io.Writer) func() {
	prev := resetLog
	resetLog = w
	return func() { resetLog = prev }
}

func swapBackupNow(now func() time.Time) func() {
	prev := backupNow
	backupNow = now
	return func() { backupNow = prev }
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

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
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
