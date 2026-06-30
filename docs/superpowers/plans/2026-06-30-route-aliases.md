# Route Aliases Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add exact and one-label wildcard route aliases so multiple local hostnames can proxy to one Lewp route and one app port.

**Architecture:** Replace the old shared `identities` registry model with explicit `routes`, `route_hosts`, `leases`, and `ports` tables. Route lookup moves from `identities.host` to `route_hosts`, exact matches win over wildcard matches, and aliases are managed through a new `lewp alias` command group. Existing local registry compatibility is intentionally dropped; migration resets old schema in place.

**Tech Stack:** Go 1.25, stdlib `database/sql`/`flag`/`net/http/httputil`, `modernc.org/sqlite`, existing Lewp identity/suffix/control/CLI/proxy packages.

---

## File Structure

- Modify `internal/registry/registry.go`: fresh schema, route/host/lease/port data access, exact/wildcard conflict checks, route lookup, list/info/release helpers.
- Modify `internal/registry/move.go`: move one route and all route hosts from source path to destination path.
- Modify `internal/registry/registry_test.go`: replace identity-based tests with route/alias/port schema tests.
- Modify `internal/identity/identity.go`: add route host pattern validation and one-label wildcard matching helpers.
- Modify `internal/identity/identity_test.go`: add wildcard validation and matching tests.
- Modify `internal/control/service.go`: add alias add/remove/list methods, update lease/port/release/list/info/move to use fresh registry APIs.
- Modify `internal/control/socket.go`: add `alias-add`, `alias-remove`, and `alias-list` request dispatch.
- Modify `internal/control/service_test.go`: add service-level alias and conflict tests.
- Modify `internal/cli/routes.go`: add `runAlias`, alias output helpers, grouped `info` output, and route/alias table rows.
- Modify `internal/cli/help.go`: add top-level alias help and command help.
- Modify `internal/cli/completion.go`: include alias command completions.
- Modify `internal/cli/cli.go`: dispatch `alias`.
- Modify `internal/cli/route_commands_test.go`: add CLI alias tests and update list/info expectations.
- Modify `internal/proxy/proxy.go`: route through exact or wildcard route hosts and show matched wildcard pattern on debug pages.
- Modify `internal/proxy/proxy_test.go`: add exact alias and wildcard proxy tests.
- Modify `internal/daemon/daemon_test.go`: update route registration helper if it uses old registry shape.
- Modify `README.md` and `DOCUMENTATION.md`: document route aliases, wildcard limits, conflicts, and single-process use case.

---

### Task 1: Fresh Registry Schema And Route Lease API

**Files:**
- Modify: `internal/registry/registry.go`
- Modify: `internal/registry/registry_test.go`

- [ ] **Step 1: Write failing schema test**

Replace `TestMigrateCreatesPlanSchema` in `internal/registry/registry_test.go` with:

```go
func TestMigrateCreatesRouteAliasSchema(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for _, table := range []string{"routes", "route_hosts", "leases", "ports", "events"} {
		var name string
		err := db.QueryRow("select name from sqlite_master where type='table' and name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	assertColumn(t, db, "routes", "normalized_name")
	assertColumn(t, db, "route_hosts", "host_type")
	assertColumn(t, db, "leases", "route_id")
	assertColumn(t, db, "ports", "normalized_name")
	assertColumn(t, db, "ports", "state")

	for _, old := range []string{"identities"} {
		var name string
		err := db.QueryRow("select name from sqlite_master where type='table' and name=?", old).Scan(&name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("old table %s still exists or query failed: name=%q err=%v", old, name, err)
		}
	}
}
```

Add `errors` to imports in that test file.

- [ ] **Step 2: Run schema test to verify failure**

Run: `go test ./internal/registry -run TestMigrateCreatesRouteAliasSchema -count=1`

Expected: FAIL because `routes` and `route_hosts` do not exist.

- [ ] **Step 3: Add core registry types**

In `internal/registry/registry.go`, replace `Lease` and `Record` with route-aware types:

```go
const (
	StateActive   = "active"
	StateReleased = "released"

	HostTypePrimary  = "primary"
	HostTypeAlias    = "alias"
	HostTypeWildcard = "wildcard"
)

type Route struct {
	ID             int64
	Root           string
	Name           string
	NormalizedRoot string
	NormalizedName string
	Path           string
}

type RouteHost struct {
	ID       int64  `json:"id,omitempty"`
	RouteID  int64  `json:"route_id,omitempty"`
	Host     string `json:"host"`
	HostType string `json:"host_type"`
	Source   string `json:"source,omitempty"`
}

type Lease struct {
	ID      int64
	RouteID int64
	Port    int
	State   string
	Created bool
}

type PortLease struct {
	ID             int64
	Path           string
	Name           string
	NormalizedName string
	Port           int
	State          string
	Created        bool
}

type Record struct {
	RouteID        int64
	HostID         int64
	LeaseID        int64
	Root           string
	Name           string
	NormalizedRoot string
	NormalizedName string
	Host           string
	HostType       string
	MatchedHost    string
	Kind           identity.Kind
	Path           string
	Port           int
	State          string
	LastSeenAt     string
	ReleasedAt     string
}
```

- [ ] **Step 4: Replace migration with fresh schema reset**

In `Store.Migrate`, use a schema version and drop old/current Lewp tables when the version is not current:

```go
const schemaVersion = 2

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, `pragma user_version`).Scan(&version); err != nil {
		return err
	}
	if version != schemaVersion {
		for _, stmt := range []string{
			`drop table if exists events`,
			`drop table if exists leases`,
			`drop table if exists ports`,
			`drop table if exists route_hosts`,
			`drop table if exists routes`,
			`drop table if exists identities`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}

	schema := `
create table if not exists routes (
	id integer primary key autoincrement,
	root text not null,
	name text not null,
	normalized_root text not null,
	normalized_name text not null,
	path text not null,
	created_at text not null,
	updated_at text not null
);
create unique index if not exists routes_path_uq on routes(path);

create table if not exists route_hosts (
	id integer primary key autoincrement,
	route_id integer not null references routes(id) on delete cascade,
	host text not null,
	host_type text not null check (host_type in ('primary','alias','wildcard')),
	source text not null default 'cli',
	created_at text not null,
	updated_at text not null
);
create unique index if not exists route_hosts_route_host_uq on route_hosts(route_id, host);
create index if not exists route_hosts_host_idx on route_hosts(host);

create table if not exists leases (
	id integer primary key autoincrement,
	route_id integer not null references routes(id) on delete cascade,
	port integer not null,
	state text not null,
	last_seen_at text,
	released_at text,
	created_at text not null,
	updated_at text not null
);
create unique index if not exists leases_route_active_uq on leases(route_id) where state = 'active';
create unique index if not exists leases_port_active_uq on leases(port) where state = 'active';

create table if not exists ports (
	id integer primary key autoincrement,
	path text not null,
	name text not null,
	normalized_name text not null,
	port integer not null,
	state text not null,
	released_at text,
	created_at text not null,
	updated_at text not null
);
create unique index if not exists ports_path_name_active_uq on ports(path, normalized_name) where state = 'active';
create unique index if not exists ports_port_active_uq on ports(port) where state = 'active';

create table if not exists events (
	id integer primary key autoincrement,
	route_id integer references routes(id),
	event_type text not null,
	message text not null,
	metadata_json text not null default '{}',
	created_at text not null
);`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`pragma user_version=%d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 5: Run schema test to verify pass**

Run: `go test ./internal/registry -run TestMigrateCreatesRouteAliasSchema -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "refactor: reset registry schema for route hosts"
```

---

### Task 2: Registry Route Hosts, Leases, Ports, And Conflicts

**Files:**
- Modify: `internal/registry/registry.go`
- Modify: `internal/registry/registry_test.go`

- [ ] **Step 1: Replace route lease tests with route-host tests**

In `internal/registry/registry_test.go`, replace `TestLeaseRouteReusesStablePort` with:

```go
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
```

Replace `TestLeaseSkipsBusyPortAndBarePortHasNoHost` with:

```go
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
```

Add tests:

```go
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
	ident := testIdentity(t, "app.work.lewp", identity.KindRoute)
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
```

- [ ] **Step 2: Run registry tests to verify failure**

Run: `go test ./internal/registry -count=1`

Expected: FAIL with missing `LeaseRoute`, `LeasePort`, `AddRouteHost`, and `RouteHosts`.

- [ ] **Step 3: Implement route and port lease APIs**

In `internal/registry/registry.go`, delete the old identity-based `Lease`, `Record`, `Lease`, `Remember`, `Identity`, `FindByHost`, `RememberedIdentity`, and `RememberedPathIdentity` implementations after their replacements below compile. Add these route-first APIs:

```go
func (s *Store) LeaseRoute(ctx context.Context, ident identity.Result, portRange PortRange) (Lease, error)
func (s *Store) LeasePort(ctx context.Context, ident identity.Result, portRange PortRange) (PortLease, error)
func (s *Store) AddRouteHost(ctx context.Context, routeID int64, host, hostType, source string) (RouteHost, bool, error)
func (s *Store) RemoveRouteHost(ctx context.Context, routeID int64, host string) (int, error)
func (s *Store) RouteHosts(ctx context.Context, routeID int64, aliasesOnly bool) ([]RouteHost, error)
func (s *Store) ActiveRouteByPath(ctx context.Context, path string) (Record, bool, error)
```

Required behavior:

- `LeaseRoute` upserts `routes` by `path`, updates root/name/normalized fields, upserts `route_hosts` primary host, then creates or reuses an active lease.
- `LeasePort` upserts/reuses an active row in `ports` by `path + normalized_name`.
- `AddRouteHost` is idempotent for the same route and host.
- `AddRouteHost` calls conflict checks before insert.
- `RouteHosts(..., true)` excludes `host_type=primary`.
- `ActiveRouteByPath` returns the primary host row for the active route at a path.

Use the existing `allocMu`, `preferredReleasedPort`, `nextPort`, and `isPortFree` patterns. Split old helpers into route and bare-port variants:

```go
func preferredReleasedRoutePort(ctx context.Context, tx *sql.Tx, routeID int64, portRange PortRange) (int, error)
func preferredReleasedBarePort(ctx context.Context, tx *sql.Tx, path, normalizedName string, portRange PortRange) (int, error)
func activeRouteLease(ctx context.Context, tx *sql.Tx, routeID int64) (Lease, bool, error)
func activePortLease(ctx context.Context, tx *sql.Tx, path, normalizedName string) (PortLease, bool, error)
```

- [ ] **Step 4: Implement exact/wildcard conflict checks in registry**

Add helpers:

```go
func (s *Store) ensureRouteHostAvailable(ctx context.Context, tx *sql.Tx, routeID int64, host, hostType string) error
func wildcardMatches(pattern, host string) bool
func isWildcardHost(host string) bool
```

Conflict rules:

- exact host conflicts with another active route's exact host.
- exact host conflicts when another active route's wildcard matches it.
- wildcard host conflicts with another active route's same wildcard.
- wildcard host conflicts when it matches another active route's exact host.
- same-route duplicates return existing row with `created=false`.

Use errors that include owner path:

```go
return fmt.Errorf("host %s conflicts with route owned by %s", host, ownerPath)
return fmt.Errorf("wildcard %s would cover %s owned by %s", pattern, coveredHost, ownerPath)
```

- [ ] **Step 5: Run registry tests**

Run: `go test ./internal/registry -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat: store route hosts and aliases"
```

---

### Task 3: Route Host Pattern Validation

**Files:**
- Modify: `internal/identity/identity.go`
- Modify: `internal/identity/identity_test.go`

- [ ] **Step 1: Add failing validation tests**

Add to `internal/identity/identity_test.go`:

```go
func TestValidateRouteHostPatternAcceptsExactAndWildcard(t *testing.T) {
	managed := []string{"lewp", "local.todoordie.com"}
	for _, host := range []string{
		"tags.app.lewp",
		"*.app.lewp",
		"tags.app.local.todoordie.com",
		"*.app.local.todoordie.com",
	} {
		if err := ValidateRouteHostPatternForSuffixes(host, managed); err != nil {
			t.Fatalf("ValidateRouteHostPatternForSuffixes(%q) error: %v", host, err)
		}
	}
}

func TestValidateRouteHostPatternRejectsBadWildcards(t *testing.T) {
	managed := []string{"lewp"}
	for _, host := range []string{"*", "*.", "*.*.app.lewp", "foo.*.app.lewp", "*.lewp", "*.bad.com"} {
		if err := ValidateRouteHostPatternForSuffixes(host, managed); err == nil {
			t.Fatalf("ValidateRouteHostPatternForSuffixes(%q) succeeded", host)
		}
	}
}

func TestWildcardRouteHostMatchesOneLabel(t *testing.T) {
	if !WildcardRouteHostMatches("*.app.lewp", "tags.app.lewp") {
		t.Fatal("expected wildcard to match one label")
	}
	for _, host := range []string{"app.lewp", "foo.tags.app.lewp"} {
		if WildcardRouteHostMatches("*.app.lewp", host) {
			t.Fatalf("wildcard should not match %s", host)
		}
	}
}
```

- [ ] **Step 2: Run validation tests to verify failure**

Run: `go test ./internal/identity -run 'TestValidateRouteHostPattern|TestWildcardRouteHost' -count=1`

Expected: FAIL with undefined functions.

- [ ] **Step 3: Implement validation helpers**

Add to `internal/identity/identity.go`:

```go
func NormalizeHostPattern(host string) string {
	return normalizeHost(host)
}

func IsWildcardRouteHost(host string) bool {
	host = NormalizeHostPattern(host)
	return strings.HasPrefix(host, "*.")
}

func ValidateRouteHostPatternForSuffixes(host string, managed []string) error {
	host = NormalizeHostPattern(host)
	if IsWildcardRouteHost(host) {
		suffixHost := strings.TrimPrefix(host, "*.")
		if suffixHost == "" || strings.Contains(suffixHost, "*") {
			return fmt.Errorf("wildcard host %q must use exactly one leading * label", host)
		}
		return ValidateHostForSuffixes(suffixHost, managed)
	}
	if strings.Contains(host, "*") {
		return fmt.Errorf("host %q contains invalid wildcard placement", host)
	}
	return ValidateHostForSuffixes(host, managed)
}

func WildcardRouteHostMatches(pattern, host string) bool {
	pattern = NormalizeHostPattern(pattern)
	host = NormalizeHostPattern(host)
	if !IsWildcardRouteHost(pattern) {
		return false
	}
	suffixHost := strings.TrimPrefix(pattern, "*.")
	suffix := "." + suffixHost
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	left := strings.TrimSuffix(host, suffix)
	return left != "" && !strings.Contains(left, ".")
}
```

- [ ] **Step 4: Run validation tests**

Run: `go test ./internal/identity -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go
git commit -m "feat: validate route host wildcards"
```

---

### Task 4: Control Service Alias API

**Files:**
- Modify: `internal/control/service.go`
- Modify: `internal/control/socket.go`
- Modify: `internal/control/service_test.go`

- [ ] **Step 1: Add failing service tests**

Add to `internal/control/service_test.go`:

```go
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

func TestServiceAliasAddReusesRoutePort(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{WorkDir: dir, Root: "work", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := svc.AliasAdd(ctx, AliasRequest{WorkDir: dir, Host: "tags.app.work.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if alias.Port != lease.Port || alias.Host != "tags.app.work.lewp" || alias.HostKind != identity.HostKind("alias") {
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
```

- [ ] **Step 2: Run service tests to verify failure**

Run: `go test ./internal/control -run 'TestServiceAlias' -count=1`

Expected: FAIL with undefined `AliasRequest` and `AliasAdd`.

- [ ] **Step 3: Add control request/response types**

In `internal/control/service.go`, add:

```go
type AliasRequest struct {
	WorkDir string
	Host    string
}

type AliasRemoveResponse struct {
	Removed int `json:"removed"`
}
```

In `internal/control/socket.go`, add fields:

```go
Alias AliasRequest `json:"alias,omitempty"`
```

Add response field:

```go
AliasRemove *AliasRemoveResponse `json:"alias_remove,omitempty"`
```

- [ ] **Step 4: Implement service alias methods**

Add to `internal/control/service.go`:

```go
func (s *Service) AliasAdd(ctx context.Context, req AliasRequest) (LeaseResponse, error) {
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return LeaseResponse{}, err
	}
	host := identity.NormalizeHostPattern(req.Host)
	if err := identity.ValidateRouteHostPatternForSuffixes(host, s.managedSuffixes); err != nil {
		return LeaseResponse{}, err
	}
	route, ok, err := s.store.ActiveRouteByPath(ctx, abs)
	if err != nil {
		return LeaseResponse{}, err
	}
	if !ok {
		return LeaseResponse{}, fmt.Errorf("no active route for this directory\nRun: lewp add")
	}
	hostType := registry.HostTypeAlias
	if identity.IsWildcardRouteHost(host) {
		hostType = registry.HostTypeWildcard
	}
	_, created, err := s.store.AddRouteHost(ctx, route.RouteID, host, hostType, "cli")
	if err != nil {
		return LeaseResponse{}, err
	}
	resp := listEntryResponse(route)
	resp.Host = host
	resp.URL = "http://" + host
	resp.HTTPSURL = "https://" + host
	resp.HostKind = identity.HostKind("alias")
	if hostType == registry.HostTypeWildcard {
		resp.HostKind = identity.HostKind("wildcard")
	}
	resp.LeaseState = "reused"
	if created {
		resp.LeaseState = "new"
	}
	return resp, nil
}

func (s *Service) AliasRemove(ctx context.Context, req AliasRequest) (AliasRemoveResponse, error) {
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return AliasRemoveResponse{}, err
	}
	host := identity.NormalizeHostPattern(req.Host)
	route, ok, err := s.store.ActiveRouteByPath(ctx, abs)
	if err != nil {
		return AliasRemoveResponse{}, err
	}
	if !ok {
		return AliasRemoveResponse{}, fmt.Errorf("no active route for this directory\nRun: lewp add")
	}
	n, err := s.store.RemoveRouteHost(ctx, route.RouteID, host)
	return AliasRemoveResponse{Removed: n}, err
}

func (s *Service) AliasList(ctx context.Context, req AliasRequest) ([]ListEntry, error) {
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return nil, err
	}
	route, ok, err := s.store.ActiveRouteByPath(ctx, abs)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	hosts, err := s.store.RouteHosts(ctx, route.RouteID, true)
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(hosts))
	for _, h := range hosts {
		entry := routeRecordEntry(route)
		entry.Host = h.Host
		entry.Kind = identity.Kind("alias")
		entries = append(entries, entry)
	}
	return entries, nil
}
```

Add helpers `listEntryResponse` and `routeRecordEntry` near existing `response`.

- [ ] **Step 5: Wire socket dispatch**

In `dispatch`, add:

```go
case "alias-add":
	lease, err := svc.AliasAdd(ctx, req.Alias)
	return Response{Lease: &lease}, err
case "alias-remove":
	res, err := svc.AliasRemove(ctx, req.Alias)
	return Response{AliasRemove: &res}, err
case "alias-list":
	entries, err := svc.AliasList(ctx, req.Alias)
	return Response{Entries: entries}, err
```

- [ ] **Step 6: Run control tests**

Run: `go test ./internal/control -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/control/service.go internal/control/socket.go internal/control/service_test.go
git commit -m "feat: add route alias control API"
```

---

### Task 5: CLI Alias Command

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/routes.go`
- Modify: `internal/cli/help.go`
- Modify: `internal/cli/completion.go`
- Modify: `internal/cli/route_commands_test.go`

- [ ] **Step 1: Add failing CLI tests**

Add to `internal/cli/route_commands_test.go`:

```go
func TestRunAliasAddListRemove(t *testing.T) {
	socketPath := startTestDaemon(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := Run(Config{Args: []string{"add", "--root", "work", "--name", "app"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("add code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "add", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "HOST=tags.app.work.lewp") || !strings.Contains(got, "STATE=new") || !strings.Contains(got, "HOST_KIND=alias") {
		t.Fatalf("alias add output unexpected: %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "list"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias list code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HOST=tags.app.work.lewp") {
		t.Fatalf("alias list missing host: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(Config{Args: []string{"alias", "remove", "tags.app.work.lewp"}, WorkDir: dir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("alias remove code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "removed alias tags.app.work.lewp") {
		t.Fatalf("alias remove output unexpected: %q", stdout.String())
	}
}
```

Add:

```go
func TestRunAliasAddRequiresHostArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"alias", "add"}, WorkDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "lewp alias add <host>") {
		t.Fatalf("stderr missing usage: %q", stderr.String())
	}
}
```

- [ ] **Step 2: Run CLI tests to verify failure**

Run: `go test ./internal/cli -run 'TestRunAlias' -count=1`

Expected: FAIL because `alias` command is unknown.

- [ ] **Step 3: Dispatch alias command**

In `internal/cli/cli.go`, add:

```go
case "alias":
	if helpRequested(cfg.Args[1:]) {
		fmt.Fprint(cfg.Stdout, aliasHelp)
		return 0
	}
	return runAlias(cfg)
```

Add `alias` to top-level help command list.

- [ ] **Step 4: Implement `runAlias`**

Add to `internal/cli/routes.go`:

```go
func runAlias(cfg Config) int {
	if len(cfg.Args) < 2 {
		fmt.Fprint(cfg.Stderr, aliasHelp)
		return 2
	}
	switch cfg.Args[1] {
	case "add":
		return runAliasAdd(cfg)
	case "remove":
		return runAliasRemove(cfg)
	case "list":
		return runAliasList(cfg)
	default:
		fmt.Fprintf(cfg.Stderr, "lewp alias: unknown action %q\nRun: lewp alias --help\n", cfg.Args[1])
		return 2
	}
}

func runAliasAdd(cfg Config) int {
	fs := flag.NewFlagSet("alias add", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	jsonOut := fs.Bool("json", false, "")
	if fs.Parse(cfg.Args[2:]) != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(cfg.Stderr, "lewp alias add <host>")
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "alias-add", Alias: control.AliasRequest{WorkDir: cfg.WorkDir, Host: fs.Arg(0)}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, cfg.Stderr, *resp.Lease, *jsonOut, false)
	return 0
}
```

Implement `runAliasRemove` and `runAliasList` with the same argument style. `runAliasRemove` prints `removed alias <host>` or `no alias <host> for this directory`. `runAliasList --json` uses `writeRoutes(..., true)`; human output uses `writeRoutes(..., false)`.

- [ ] **Step 5: Add help and completion entries**

In `internal/cli/help.go`, add:

```go
aliasHelp = `lewp alias — manage extra hostnames for the current route

Usage:
  lewp alias add <host> [--json]
  lewp alias remove <host>
  lewp alias list [--json]

Examples:
  lewp alias add tags.app.lewp
  lewp alias add '*.app.lewp'
  lewp alias list
`
```

In `internal/cli/completion.go`, include `alias` as a top-level command and `add remove list` as alias subcommands.

- [ ] **Step 6: Run CLI tests**

Run: `go test ./internal/cli -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/cli.go internal/cli/routes.go internal/cli/help.go internal/cli/completion.go internal/cli/route_commands_test.go
git commit -m "feat: add route alias CLI"
```

---

### Task 6: Proxy Exact Alias And Wildcard Routing

**Files:**
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Add failing proxy tests**

Add to `internal/proxy/proxy_test.go`:

```go
func TestProxyRoutesExactAliasAndPreservesRequestedHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "tags.app.work.lewp" {
			t.Fatalf("Host=%q", r.Host)
		}
		_, _ = w.Write([]byte("alias ok"))
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	routeID := registerRoute(t, store, "app.work.lewp", port)
	if _, _, err := store.AddRouteHost(context.Background(), routeID, "tags.app.work.lewp", registry.HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://tags.app.work.lewp/widgets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "alias ok" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestProxyRoutesWildcardAliasAndPreservesRequestedHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "leads.app.work.lewp" {
			t.Fatalf("Host=%q", r.Host)
		}
		_, _ = w.Write([]byte("wildcard ok"))
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	routeID := registerRoute(t, store, "app.work.lewp", port)
	if _, _, err := store.AddRouteHost(context.Background(), routeID, "*.app.work.lewp", registry.HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://leads.app.work.lewp/widgets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "wildcard ok" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}
```

Update `registerRoute` to return `routeID`:

```go
func registerRoute(t *testing.T, store *registry.Store, host string, port int) int64
```

- [ ] **Step 2: Run proxy tests to verify failure**

Run: `go test ./internal/proxy -run 'TestProxyRoutesExactAlias|TestProxyRoutesWildcard' -count=1`

Expected: FAIL until registry lookup returns aliases/wildcards.

- [ ] **Step 3: Update proxy debug page for wildcard match**

In `writeDebugPage`, display `route.MatchedHost` when it differs from `route.Host`:

```go
if route.MatchedHost != "" && route.MatchedHost != route.Host {
	fmt.Fprintf(w, "<dt>Matched route</dt><dd><code>%s</code></dd>", html.EscapeString(route.MatchedHost))
}
```

Keep `target`, path, root/name, last seen, release state, and start command unchanged.

- [ ] **Step 4: Run proxy tests**

Run: `go test ./internal/proxy -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat: route proxy traffic through aliases"
```

---

### Task 7: Release, Move, List, And Info Alias Behavior

**Files:**
- Modify: `internal/registry/move.go`
- Modify: `internal/control/service.go`
- Modify: `internal/control/service_test.go`
- Modify: `internal/cli/routes.go`
- Modify: `internal/cli/route_commands_test.go`

- [ ] **Step 1: Add failing release/move/info tests**

Add to `internal/control/service_test.go`:

```go
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
```

Add this block to `TestRunAddInfoMoveRoundTrip` immediately after the initial `add` assertion and before the bare `port` command:

```go
stdout.Reset()
stderr.Reset()
code = Run(Config{Args: []string{"alias", "add", "tags.feature-1.audit.lewp"}, WorkDir: srcDir, SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr})
if code != 0 {
	t.Fatalf("alias add code=%d stderr=%q", code, stderr.String())
}
if !strings.Contains(stdout.String(), "HOST=tags.feature-1.audit.lewp") {
	t.Fatalf("alias add output missing host: %q", stdout.String())
}
```

Extend the later `infoOut` route assertions with:

```go
if !strings.Contains(infoOut, "ALIASES\n") || !strings.Contains(infoOut, "HOST=tags.feature-1.audit.lewp") || !strings.Contains(infoOut, "URL=http://tags.feature-1.audit.lewp") {
	t.Fatalf("info output missing alias fields: %q", infoOut)
}
```

- [ ] **Step 2: Run target tests to verify failure**

Run: `go test ./internal/control ./internal/cli -run 'TestServiceReleaseHidesAliases|TestServiceMovePreservesAliases|TestRunAddInfoMoveRoundTrip' -count=1`

Expected: FAIL until list/info/move output includes aliases.

- [ ] **Step 3: Update move**

In `internal/registry/move.go`, simplify `MovePath`: select one active route by `fromPath`, reject active route at `toPath`, update `routes.path`, and return all active host records for the moved route. Do not update `route_hosts`; they remain attached by `route_id`.

- [ ] **Step 4: Update list/info entries**

In `control.List`, return one entry for the primary route host with `Kind=route`, and one entry for each alias/wildcard with `Kind=identity.Kind("alias")`.

In `control.Info`, include primary route, aliases, and ports for the current path.

In `cli.writeInfo`, group:

```text
ROUTE
...

ALIASES
...

PORTS
...
```

Keep JSON output as the raw entries array.

- [ ] **Step 5: Run control and CLI tests**

Run: `go test ./internal/control ./internal/cli -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/registry/move.go internal/control/service.go internal/control/service_test.go internal/cli/routes.go internal/cli/route_commands_test.go
git commit -m "feat: include aliases in route lifecycle"
```

---

### Task 8: Docs And End-To-End Verification

**Files:**
- Modify: `README.md`
- Modify: `DOCUMENTATION.md`
- Modify: `internal/cli/help.go`

- [ ] **Step 1: Update README route docs**

In `README.md`, add a route aliases section after the route setup/custom suffix sections:

````markdown
## Route aliases

One app process can serve multiple local hostnames through the same Lewp route.
Create the route once, start the app on that port, then add aliases:

```sh
lewp add
lewp alias add tags.app.lewp
lewp alias add leads.app.lewp
lewp alias add '*.app.lewp'
```

Aliases reuse the current directory's active route and port. Wildcards match
exactly one label: `*.app.lewp` matches `tags.app.lewp`, but not `app.lewp` or
`foo.tags.app.lewp`.

For public-domain aliases, first configure the managed suffix with `lewp setup
--suffix ...`; use `--allow-domain-mirror` only when you intentionally want to
shadow that domain locally.
````

- [ ] **Step 2: Update DOCUMENTATION command reference**

In `DOCUMENTATION.md`, add `lewp alias add|remove|list` to the command list and add a `## lewp alias` section:

````markdown
## `lewp alias`

Manage extra hostnames for the current directory's active route.

```sh
lewp alias add tags.app.lewp
lewp alias add '*.app.lewp'
lewp alias list
lewp alias remove tags.app.lewp
```

Aliases reuse the same route port as `lewp add`; they never allocate a second
app port. `alias add` fails when the current directory has no active route.

Wildcard aliases match exactly one label. `*.app.lewp` matches
`tags.app.lewp` and `leads.app.lewp`; it does not match `app.lewp` or
`foo.tags.app.lewp`.

Lewp rejects aliases that conflict with another active route's exact host or
wildcard. The error includes the owning path and release guidance.
````

- [ ] **Step 3: Run full verification**

Run:

```bash
go test ./...
go test ./... -count=1
```

Expected: both commands PASS.

- [ ] **Step 4: Run CLI smoke test through in-process tests**

Run:

```bash
go test ./internal/cli -run 'TestRunAliasAddListRemove|TestRunAddInfoMoveRoundTrip|TestRunListJSONEmitsEntries' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit docs and final fixes**

```bash
git add README.md DOCUMENTATION.md internal/cli/help.go
git commit -m "docs: document route aliases"
```

If step 3 required code fixes, include those files in the same commit only when they are directly required for docs/help/tests consistency; otherwise create a separate `fix:` commit before this docs commit.

---

## Self-Review Checklist

- Spec coverage: CLI aliases, exact aliases, one-label wildcard aliases, same-port routing, conflict rejection, release/move/list/info, proxy Host preservation, public suffix safety, fresh schema reset, and docs all map to tasks above.
- Placeholder scan: no deferred product decisions; no unspecified command behavior; no compatibility work.
- Type consistency: route hosts use `registry.HostTypePrimary`, `registry.HostTypeAlias`, `registry.HostTypeWildcard`; user-facing list rows use `route`, `alias`, or `port`.
- Verification: final task runs `go test ./...` and targeted CLI smoke tests.
