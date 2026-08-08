package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/scottwater/lewp/internal/identity"
	_ "modernc.org/sqlite"
)

const (
	StateActive   = "active"
	StateReleased = "released"

	HostTypePrimary  = "primary"
	HostTypeAlias    = "alias"
	HostTypeWildcard = "wildcard"
)

const schemaVersion = 2

// resetLog receives the loud warning emitted before Migrate wipes a
// schema-mismatched registry. It defaults to stderr (which the launchd daemon
// captures in daemon.err.log) and is overridable in tests.
var resetLog io.Writer = os.Stderr
var backupNow = func() time.Time { return time.Now().UTC() }

type Store struct {
	db *sql.DB
	// path is the on-disk location of the SQLite registry, retained so Migrate
	// can copy it to a timestamped backup before a destructive schema reset.
	path string
	// allocMu serializes every writer that claims or relocates an active port so
	// concurrent callers cannot SELECT the same free port (or both pass the
	// cross-table active-port check) before either commits and then collide on
	// the leases_port_active_uq / ports_port_active_uq unique indexes. Active
	// port uniqueness spans the leases and ports tables and is enforced in
	// application logic (activePortCount), not by a single SQLite constraint, so
	// busy_timeout cannot rescue it. Every active-port writer - Lease, Remember,
	// and MovePath - takes it.
	allocMu sync.Mutex
	// hostMu serializes route host conflict checks with host inserts/updates.
	// Exact/wildcard overlap is enforced in application logic, not by a single
	// SQLite constraint, so the check and write must happen under one lock.
	// Acquire allocMu before hostMu when both are held to avoid deadlock.
	hostMu sync.Mutex
}

var migrateMu sync.Mutex

type PortRange struct {
	Start int
	End   int
}

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

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, path: path}
	migrateMu.Lock()
	defer migrateMu.Unlock()
	if err := store.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func dataSourceName(path string) string {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout(5000)")
	values.Add("_pragma", "foreign_keys(ON)")
	values.Add("_pragma", "journal_mode(WAL)")
	// _txlock=immediate makes every read-write transaction BEGIN IMMEDIATE so it
	// takes the write lock up front instead of upgrading a deferred read
	// snapshot mid-transaction. Release-plan apply relies on that lock to keep
	// validation and mutation serializable with concurrent registry writers.
	// Without it, WAL read-then-write paths can fail with SQLITE_BUSY_SNAPSHOT,
	// which busy_timeout does not retry.
	values.Add("_txlock", "immediate")
	return (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `pragma user_version`).Scan(&version); err != nil {
		return err
	}

	// Refuse to open a registry written by a newer binary. Wiping on mismatch is
	// acceptable pre-release policy for an *upgrade* (old data, new binary), but a
	// downgrade - a stale launchd binary pointed at a newer DB - would silently
	// destroy routes and leases the running binary simply doesn't understand yet.
	// Fail loudly instead so the operator can upgrade or move the registry aside.
	if version > schemaVersion {
		return fmt.Errorf(
			"lewp registry %s has schema version %d, newer than this binary supports (%d); refusing to open so a downgrade cannot wipe your routes and leases - upgrade lewp or move the registry aside to start fresh",
			s.path, version, schemaVersion,
		)
	}

	// A version below the current schema means the on-disk data cannot be used as
	// is. Pre-release policy is to reset rather than migrate, but never silently:
	// back the registry up and log the wipe loudly first so it is recoverable and
	// observable. A brand-new registry reports version 0 and has nothing to save.
	reset := version != schemaVersion
	if reset && version != 0 {
		backupPath, err := s.backupBeforeReset()
		if err != nil {
			return fmt.Errorf(
				"lewp registry %s needs a schema reset (on-disk version %d, binary version %d) but backing it up first failed: %w",
				s.path, version, schemaVersion, err,
			)
		}
		fmt.Fprintf(resetLog,
			"lewp registry: on-disk schema version %d does not match this binary's version %d; RESETTING the registry at %s - all pinned ports, hostnames, leases, and events will be dropped. A backup of the previous registry was saved to %s\n",
			version, schemaVersion, s.path, backupPath,
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if reset {
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

// backupBeforeReset copies the current registry to a timestamped ".bak" file
// next to it and returns the backup path. It first folds the write-ahead log
// into the main database file so a copy of that single file is a complete,
// independently openable snapshot of the pre-reset registry.
func (s *Store) backupBeforeReset() (string, error) {
	if _, err := s.db.Exec(`pragma wal_checkpoint(TRUNCATE)`); err != nil {
		return "", fmt.Errorf("checkpoint before backup: %w", err)
	}
	stamp := backupNow().UTC().Format("20060102T150405Z")
	for attempt := 0; ; attempt++ {
		backupPath := backupPathForAttempt(s.path, stamp, attempt)
		if err := copyFile(s.path, backupPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		return backupPath, nil
	}
}

func backupPathForAttempt(path, stamp string, attempt int) string {
	if attempt == 0 {
		return fmt.Sprintf("%s.%s.bak", path, stamp)
	}
	return fmt.Sprintf("%s.%s.%d.bak", path, stamp, attempt)
}

// copyFile copies src to dst, refusing to overwrite an existing dst so a backup
// can never clobber an earlier one.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func (s *Store) Lease(ctx context.Context, ident identity.Result, portRange PortRange) (Lease, error) {
	if portRange.Start <= 0 || portRange.End < portRange.Start {
		return Lease{}, errors.New("invalid port range")
	}
	if ident.Kind == identity.KindPort {
		lease, err := s.LeasePort(ctx, ident, portRange)
		if err != nil {
			return Lease{}, err
		}
		return Lease{ID: lease.ID, Port: lease.Port, State: lease.State, Created: lease.Created}, nil
	}
	return s.LeaseRoute(ctx, ident, portRange)
}

func (s *Store) LeaseRoute(ctx context.Context, ident identity.Result, portRange PortRange) (Lease, error) {
	if portRange.Start <= 0 || portRange.End < portRange.Start {
		return Lease{}, errors.New("invalid port range")
	}
	if ident.Kind != identity.KindRoute {
		return Lease{}, fmt.Errorf("lease route requires %s identity", identity.KindRoute)
	}
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()

	routeID, err := upsertRoute(ctx, tx, ident, now)
	if err != nil {
		return Lease{}, err
	}
	if err := s.ensureRouteHostAvailable(ctx, tx, routeID, ident.Host, HostTypePrimary); err != nil {
		return Lease{}, err
	}
	if err := upsertPrimaryHost(ctx, tx, routeID, ident, now); err != nil {
		return Lease{}, err
	}
	if err := s.ensureExistingRouteHostsAvailable(ctx, tx, routeID); err != nil {
		return Lease{}, err
	}
	if lease, ok, err := activeRouteLease(ctx, tx, routeID); err != nil {
		return Lease{}, err
	} else if ok {
		return lease, tx.Commit()
	}

	port, err := preferredReleasedRoutePort(ctx, tx, routeID, portRange)
	if err != nil {
		return Lease{}, err
	}
	if port == 0 {
		port, err = s.nextPort(ctx, tx, portRange)
	}
	if err != nil {
		return Lease{}, err
	}
	if err := ensurePortAvailable(ctx, tx, port); err != nil {
		return Lease{}, err
	}
	res, err := tx.ExecContext(ctx, `insert into leases(route_id, port, state, created_at, updated_at) values(?, ?, ?, ?, ?)`,
		routeID, port, StateActive, now, now)
	if err != nil {
		return Lease{}, err
	}
	leaseID, err := res.LastInsertId()
	if err != nil {
		return Lease{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into events(route_id, event_type, message, created_at) values(?, ?, ?, ?)`,
		routeID, "lease_created", fmt.Sprintf("leased port %d", port), now); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{ID: leaseID, RouteID: routeID, Port: port, State: StateActive, Created: true}, nil
}

func (s *Store) LeasePort(ctx context.Context, ident identity.Result, portRange PortRange) (PortLease, error) {
	if portRange.Start <= 0 || portRange.End < portRange.Start {
		return PortLease{}, errors.New("invalid port range")
	}
	if ident.Kind != identity.KindPort {
		return PortLease{}, fmt.Errorf("lease port requires %s identity", identity.KindPort)
	}
	return s.leasePort(ctx, ident, portRange)
}

func (s *Store) Identity(ctx context.Context, id int64) (identity.Result, error) {
	var got identity.Result
	var source string
	err := s.db.QueryRowContext(ctx, `select r.root, r.name, r.normalized_root, r.normalized_name, coalesce(rh.host, ''), coalesce(rh.source, ''), r.path
from routes r
left join route_hosts rh on rh.route_id = r.id and rh.host_type=?
where r.id=?`, HostTypePrimary, id).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &source, &got.Path)
	if err != nil {
		return identity.Result{}, err
	}
	got.Kind = identity.KindRoute
	got.HostSource = identity.Source(source)
	got.HostKind = hostKind(got.Host, got.NormalizedRoot, got.NormalizedName)
	return got, nil
}

func (s *Store) Remember(ctx context.Context, ident identity.Result, port int) error {
	if ident.Kind == identity.KindPort {
		return s.rememberPort(ctx, ident, port)
	}
	// Remember claims an active port for the route, so it serializes with every
	// other active-port writer via allocMu (acquired before hostMu).
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	routeID, err := upsertRoute(ctx, tx, ident, now)
	if err != nil {
		return err
	}
	if err := s.ensureRouteHostAvailable(ctx, tx, routeID, ident.Host, HostTypePrimary); err != nil {
		return err
	}
	if err := upsertPrimaryHost(ctx, tx, routeID, ident, now); err != nil {
		return err
	}
	if err := s.ensureExistingRouteHostsAvailable(ctx, tx, routeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where route_id=? and state=?`, StateReleased, now, now, routeID, StateActive); err != nil {
		return err
	}
	if err := ensurePortAvailable(ctx, tx, port); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into leases(route_id, port, state, created_at, updated_at) values(?, ?, ?, ?, ?)`, routeID, port, StateActive, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindByHost(ctx context.Context, host string) (identity.Result, bool, error) {
	host = normalizeRouteHost(host)
	var got identity.Result
	var source string
	err := s.db.QueryRowContext(ctx, `select r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.source, r.path
from route_hosts rh
join routes r on r.id = rh.route_id
join leases l on l.route_id = r.id
where rh.host=? and l.state=?
limit 1`, host, StateActive).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &source, &got.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Result{}, false, nil
	}
	if err != nil {
		return identity.Result{}, false, err
	}
	got.Kind = identity.KindRoute
	got.HostSource = identity.Source(source)
	got.HostKind = hostKind(got.Host, got.NormalizedRoot, got.NormalizedName)
	return got, err == nil, err
}

func (s *Store) RememberedIdentity(ctx context.Context, path string, kind identity.Kind, normalizedName string) (identity.Result, bool, error) {
	var got identity.Result
	var err error
	if kind == identity.KindPort {
		err = s.db.QueryRowContext(ctx, `select name, normalized_name, path
from ports
where path=? and normalized_name=?
order by updated_at desc, id desc
limit 1`, path, normalizedName).
			Scan(&got.Name, &got.NormalizedName, &got.Path)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.Result{}, false, nil
		}
		got.Kind = identity.KindPort
		return got, err == nil, err
	}
	var source string
	err = s.db.QueryRowContext(ctx, `select r.root, r.name, r.normalized_root, r.normalized_name, coalesce(rh.host, ''), coalesce(rh.source, ''), r.path
from routes r
left join route_hosts rh on rh.route_id = r.id and rh.host_type=?
where r.path=? and r.normalized_name=?
order by r.updated_at desc, r.id desc
limit 1`, HostTypePrimary, path, normalizedName).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &source, &got.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Result{}, false, nil
	}
	if err != nil {
		return identity.Result{}, false, err
	}
	got.Kind = identity.KindRoute
	got.HostSource = identity.Source(source)
	got.HostKind = hostKind(got.Host, got.NormalizedRoot, got.NormalizedName)
	return got, err == nil, err
}

func (s *Store) RememberedPathIdentity(ctx context.Context, path string, kind identity.Kind) (identity.Result, bool, error) {
	var got identity.Result
	var err error
	if kind == identity.KindPort {
		err = s.db.QueryRowContext(ctx, `select name, normalized_name, path
from ports
where path=?
order by updated_at desc, id desc
limit 1`, path).
			Scan(&got.Name, &got.NormalizedName, &got.Path)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.Result{}, false, nil
		}
		got.Kind = identity.KindPort
		return got, err == nil, err
	}
	var source string
	err = s.db.QueryRowContext(ctx, `select r.root, r.name, r.normalized_root, r.normalized_name, coalesce(rh.host, ''), coalesce(rh.source, ''), r.path
from routes r
left join route_hosts rh on rh.route_id = r.id and rh.host_type=?
where r.path=?
order by r.updated_at desc, r.id desc
limit 1`, HostTypePrimary, path).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &source, &got.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Result{}, false, nil
	}
	if err != nil {
		return identity.Result{}, false, err
	}
	got.Kind = identity.KindRoute
	got.HostSource = identity.Source(source)
	got.HostKind = hostKind(got.Host, got.NormalizedRoot, got.NormalizedName)
	return got, err == nil, err
}

func (s *Store) RouteByHost(ctx context.Context, host string) (Record, bool, error) {
	host = normalizeRouteHost(host)
	var r Record
	err := s.db.QueryRowContext(ctx, `select r.id, rh.id, l.id, r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.host_type, r.path, l.port, l.state, coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from route_hosts rh
join routes r on r.id = rh.route_id
join leases l on l.route_id = r.id
where rh.host=? and l.state=?
order by case rh.host_type when 'primary' then 0 else 1 end, rh.id
limit 1`, host, StateActive).
		Scan(&r.RouteID, &r.HostID, &r.LeaseID, &r.Root, &r.Name, &r.NormalizedRoot, &r.NormalizedName, &r.Host, &r.HostType, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s.wildcardRouteByHost(ctx, host)
	}
	r.MatchedHost = r.Host
	r.Kind = identity.KindRoute
	return r, err == nil, err
}

func (s *Store) wildcardRouteByHost(ctx context.Context, host string) (Record, bool, error) {
	host = normalizeRouteHost(host)
	rows, err := s.db.QueryContext(ctx, `select r.id, rh.id, l.id, r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.host_type, r.path, l.port, l.state, coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from route_hosts rh
join routes r on r.id = rh.route_id
join leases l on l.route_id = r.id
where rh.host_type=? and l.state=?
order by rh.id`, HostTypeWildcard, StateActive)
	if err != nil {
		return Record{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.RouteID, &r.HostID, &r.LeaseID, &r.Root, &r.Name, &r.NormalizedRoot, &r.NormalizedName, &r.Host, &r.HostType, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt); err != nil {
			return Record{}, false, err
		}
		if !wildcardMatches(r.Host, host) {
			continue
		}
		r.MatchedHost = r.Host
		r.Host = host
		r.Kind = identity.KindRoute
		return r, true, nil
	}
	if err := rows.Err(); err != nil {
		return Record{}, false, err
	}
	return Record{}, false, nil
}

func (s *Store) ActiveRouteByPath(ctx context.Context, path string) (Record, bool, error) {
	var r Record
	err := s.db.QueryRowContext(ctx, `select r.id, rh.id, l.id, r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.host_type, r.path, l.port, l.state, coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from routes r
join leases l on l.route_id = r.id
join route_hosts rh on rh.route_id = r.id and rh.host_type=?
where r.path=? and l.state=?
order by l.id desc
limit 1`, HostTypePrimary, path, StateActive).
		Scan(&r.RouteID, &r.HostID, &r.LeaseID, &r.Root, &r.Name, &r.NormalizedRoot, &r.NormalizedName, &r.Host, &r.HostType, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	r.MatchedHost = r.Host
	r.Kind = identity.KindRoute
	return r, true, nil
}

func (s *Store) AddRouteHost(ctx context.Context, routeID int64, host, hostType, source string) (RouteHost, bool, error) {
	host = normalizeRouteHost(host)
	if source == "" {
		source = string(identity.SourceCLI)
	}
	switch hostType {
	case HostTypeAlias:
		if isWildcardHost(host) {
			return RouteHost{}, false, fmt.Errorf("alias host %q cannot be a wildcard", host)
		}
	case HostTypeWildcard:
		if !isWildcardHost(host) {
			return RouteHost{}, false, fmt.Errorf("wildcard host %q must start with *.", host)
		}
	default:
		return RouteHost{}, false, fmt.Errorf("invalid host type %q", hostType)
	}
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RouteHost{}, false, err
	}
	defer tx.Rollback()

	if existing, ok, err := routeHost(ctx, tx, routeID, host); err != nil {
		return RouteHost{}, false, err
	} else if ok {
		return existing, false, tx.Commit()
	}
	if err := s.ensureRouteHostAvailable(ctx, tx, routeID, host, hostType); err != nil {
		return RouteHost{}, false, err
	}
	res, err := tx.ExecContext(ctx, `insert into route_hosts(route_id, host, host_type, source, created_at, updated_at) values(?, ?, ?, ?, ?, ?)`,
		routeID, host, hostType, source, now, now)
	if err != nil {
		return RouteHost{}, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return RouteHost{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return RouteHost{}, false, err
	}
	return RouteHost{ID: id, RouteID: routeID, Host: host, HostType: hostType, Source: source}, true, nil
}

func (s *Store) RemoveRouteHost(ctx context.Context, routeID int64, host string) (int, error) {
	host = normalizeRouteHost(host)
	res, err := s.db.ExecContext(ctx, `delete from route_hosts where route_id=? and host=? and host_type<>?`, routeID, host, HostTypePrimary)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *Store) RouteHosts(ctx context.Context, routeID int64, aliasesOnly bool) ([]RouteHost, error) {
	query := `select id, route_id, host, host_type, source from route_hosts where route_id=?`
	args := []any{routeID}
	if aliasesOnly {
		query += ` and host_type<>?`
		args = append(args, HostTypePrimary)
	}
	query += ` order by case host_type when 'primary' then 0 when 'alias' then 1 else 2 end, host`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []RouteHost
	for rows.Next() {
		var host RouteHost
		if err := rows.Scan(&host.ID, &host.RouteID, &host.Host, &host.HostType, &host.Source); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

func (s *Store) TouchLastSeen(ctx context.Context, leaseID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update leases set last_seen_at=?, updated_at=? where id=?`, now, now, leaseID)
	return err
}

func (s *Store) List(ctx context.Context, all bool) ([]Record, error) {
	query := `select route_id, host_id, lease_id, root, name, normalized_root, normalized_name, host, host_type, kind, path, port, state, last_seen_at, released_at
from (
select r.id route_id, rh.id host_id, coalesce(l.id, 0) lease_id, r.root root, r.name name, r.normalized_root normalized_root, r.normalized_name normalized_name, rh.host host, rh.host_type host_type, 'route' kind, r.path path, coalesce(l.port, 0) port, coalesce(l.state, '') state, coalesce(l.last_seen_at, '') last_seen_at, coalesce(l.released_at, '') released_at
from routes r
join route_hosts rh on rh.route_id = r.id
left join leases l on l.route_id = r.id
where (? or l.state = ?)
union all
select p.id route_id, 0 host_id, p.id lease_id, '' root, p.name name, '' normalized_root, p.normalized_name normalized_name, '' host, '' host_type, 'port' kind, p.path path, p.port port, p.state state, '' last_seen_at, coalesce(p.released_at, '') released_at
from ports p
where (? or p.state = ?)
)
order by path, case when kind='route' and host_type='primary' then 0 when kind='route' and host_type='alias' then 1 when kind='route' and host_type='wildcard' then 2 when kind='route' then 3 when kind='port' then 4 else 5 end, name, host`
	rows, err := s.db.QueryContext(ctx, query, all, StateActive, all, StateActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.RouteID, &r.HostID, &r.LeaseID, &r.Root, &r.Name, &r.NormalizedRoot, &r.NormalizedName, &r.Host, &r.HostType, &r.Kind, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Release releases the active route or bare-port lease for a single
// path+kind+name and reports how many active leases were freed.
func (s *Store) Release(ctx context.Context, path string, kind identity.Kind, normalizedName string, forget bool) (int, error) {
	if kind == identity.KindPort {
		return s.releasePort(ctx, path, normalizedName, forget)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `select id from routes where path=? and normalized_name=?`, path, normalizedName).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where route_id=? and state=?`, StateReleased, now, now, id, StateActive)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into events(route_id, event_type, message, created_at) values(?, ?, ?, ?)`, id, "released", "released lease", now); err != nil {
		return 0, err
	}
	if forget {
		if _, err := tx.ExecContext(ctx, `delete from events where route_id=?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `delete from routes where id=?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

// ReleasePath releases the active leases for every route or bare port at path
// of the given kind and reports how many active leases were freed.
func (s *Store) ReleasePath(ctx context.Context, path string, kind identity.Kind, forget bool) (int, error) {
	if kind == identity.KindPort {
		return s.releasePortPath(ctx, path, forget)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id from routes where path=?`, path)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	released := 0
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where route_id=? and state=?`, StateReleased, now, now, id, StateActive)
		if err != nil {
			return 0, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		released += int(affected)
		if _, err := tx.ExecContext(ctx, `insert into events(route_id, event_type, message, created_at) values(?, ?, ?, ?)`, id, "released", "released lease", now); err != nil {
			return 0, err
		}
		if forget {
			if _, err := tx.ExecContext(ctx, `delete from events where route_id=?`, id); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `delete from routes where id=?`, id); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return released, nil
}

func (s *Store) leasePort(ctx context.Context, ident identity.Result, portRange PortRange) (PortLease, error) {
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PortLease{}, err
	}
	defer tx.Rollback()
	if lease, ok, err := activePortLease(ctx, tx, ident.Path, ident.NormalizedName); err != nil {
		return PortLease{}, err
	} else if ok {
		return lease, tx.Commit()
	}
	port, err := preferredReleasedBarePort(ctx, tx, ident.Path, ident.NormalizedName, portRange)
	if err != nil {
		return PortLease{}, err
	}
	if port == 0 {
		port, err = s.nextPort(ctx, tx, portRange)
	}
	if err != nil {
		return PortLease{}, err
	}
	if err := ensurePortAvailable(ctx, tx, port); err != nil {
		return PortLease{}, err
	}
	res, err := tx.ExecContext(ctx, `insert into ports(path, name, normalized_name, port, state, created_at, updated_at) values(?, ?, ?, ?, ?, ?, ?)`,
		ident.Path, ident.Name, ident.NormalizedName, port, StateActive, now, now)
	if err != nil {
		return PortLease{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return PortLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return PortLease{}, err
	}
	return PortLease{ID: id, Path: ident.Path, Name: ident.Name, NormalizedName: ident.NormalizedName, Port: port, State: StateActive, Created: true}, nil
}

func (s *Store) rememberPort(ctx context.Context, ident identity.Result, port int) error {
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `update ports set state=?, released_at=?, updated_at=? where path=? and normalized_name=? and state=?`,
		StateReleased, now, now, ident.Path, ident.NormalizedName, StateActive); err != nil {
		return err
	}
	if err := ensurePortAvailable(ctx, tx, port); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into ports(path, name, normalized_name, port, state, created_at, updated_at) values(?, ?, ?, ?, ?, ?, ?)`,
		ident.Path, ident.Name, ident.NormalizedName, port, StateActive, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) releasePort(ctx context.Context, path, normalizedName string, forget bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update ports set state=?, released_at=?, updated_at=? where path=? and normalized_name=? and state=?`,
		StateReleased, now, now, path, normalizedName, StateActive)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if forget {
		if _, err := tx.ExecContext(ctx, `delete from ports where path=? and normalized_name=?`, path, normalizedName); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *Store) releasePortPath(ctx context.Context, path string, forget bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update ports set state=?, released_at=?, updated_at=? where path=? and state=?`,
		StateReleased, now, now, path, StateActive)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if forget {
		if _, err := tx.ExecContext(ctx, `delete from ports where path=?`, path); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

func upsertRoute(ctx context.Context, tx *sql.Tx, ident identity.Result, now string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `select id from routes where path=?`, ident.Path).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `update routes set root=?, name=?, normalized_root=?, normalized_name=?, updated_at=? where id=?`,
			ident.Root, ident.Name, ident.NormalizedRoot, ident.NormalizedName, now, id)
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `insert into routes(root, name, normalized_root, normalized_name, path, created_at, updated_at) values(?, ?, ?, ?, ?, ?, ?)`,
		ident.Root, ident.Name, ident.NormalizedRoot, ident.NormalizedName, ident.Path, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func upsertPrimaryHost(ctx context.Context, tx *sql.Tx, routeID int64, ident identity.Result, now string) error {
	host := normalizeRouteHost(ident.Host)
	if host == "" {
		return nil
	}
	source := string(ident.HostSource)
	if source == "" {
		source = string(identity.SourceInferred)
	}
	var id int64
	err := tx.QueryRowContext(ctx, `select id from route_hosts where route_id=? and host_type=?`, routeID, HostTypePrimary).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `update route_hosts set host=?, source=?, updated_at=? where id=?`, host, source, now, id)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into route_hosts(route_id, host, host_type, source, created_at, updated_at) values(?, ?, ?, ?, ?, ?)`,
		routeID, host, HostTypePrimary, source, now, now)
	return err
}

func activeRouteLease(ctx context.Context, tx *sql.Tx, routeID int64) (Lease, bool, error) {
	var lease Lease
	err := tx.QueryRowContext(ctx, `select id, route_id, port, state from leases where route_id=? and state=?`, routeID, StateActive).
		Scan(&lease.ID, &lease.RouteID, &lease.Port, &lease.State)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	return lease, err == nil, err
}

func activePortLease(ctx context.Context, tx *sql.Tx, path, normalizedName string) (PortLease, bool, error) {
	var lease PortLease
	err := tx.QueryRowContext(ctx, `select id, path, name, normalized_name, port, state from ports where path=? and normalized_name=? and state=?`, path, normalizedName, StateActive).
		Scan(&lease.ID, &lease.Path, &lease.Name, &lease.NormalizedName, &lease.Port, &lease.State)
	if errors.Is(err, sql.ErrNoRows) {
		return PortLease{}, false, nil
	}
	return lease, err == nil, err
}

func routeHost(ctx context.Context, tx *sql.Tx, routeID int64, host string) (RouteHost, bool, error) {
	host = normalizeRouteHost(host)
	var got RouteHost
	err := tx.QueryRowContext(ctx, `select id, route_id, host, host_type, source from route_hosts where route_id=? and host=?`, routeID, host).
		Scan(&got.ID, &got.RouteID, &got.Host, &got.HostType, &got.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteHost{}, false, nil
	}
	return got, err == nil, err
}

func (s *Store) ensureRouteHostAvailable(ctx context.Context, tx *sql.Tx, routeID int64, host, hostType string) error {
	host = normalizeRouteHost(host)
	if host == "" {
		return nil
	}
	wildcard := hostType == HostTypeWildcard || isWildcardHost(host)
	rows, err := tx.QueryContext(ctx, `select r.path, rh.host, rh.host_type
from route_hosts rh
join routes r on r.id = rh.route_id
join leases l on l.route_id = r.id
where l.state=? and r.id<>?`, StateActive, routeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ownerPath, otherHost, otherType string
		if err := rows.Scan(&ownerPath, &otherHost, &otherType); err != nil {
			return err
		}
		otherHost = normalizeRouteHost(otherHost)
		otherWildcard := otherType == HostTypeWildcard || isWildcardHost(otherHost)
		if !wildcard {
			if otherHost == host || (otherWildcard && wildcardMatches(otherHost, host)) {
				return fmt.Errorf("host %s conflicts with route owned by %s", host, ownerPath)
			}
			continue
		}
		if otherWildcard {
			if otherHost == host {
				return fmt.Errorf("host %s conflicts with route owned by %s", host, ownerPath)
			}
			continue
		}
		if wildcardMatches(host, otherHost) {
			return fmt.Errorf("wildcard %s would cover %s owned by %s", host, otherHost, ownerPath)
		}
	}
	return rows.Err()
}

func (s *Store) ensureExistingRouteHostsAvailable(ctx context.Context, tx *sql.Tx, routeID int64) error {
	rows, err := tx.QueryContext(ctx, `select host, host_type from route_hosts where route_id=?`, routeID)
	if err != nil {
		return err
	}
	var hosts []RouteHost
	for rows.Next() {
		var host RouteHost
		if err := rows.Scan(&host.Host, &host.HostType); err != nil {
			_ = rows.Close()
			return err
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, host := range hosts {
		if err := s.ensureRouteHostAvailable(ctx, tx, routeID, host.Host, host.HostType); err != nil {
			return err
		}
	}
	return nil
}

func preferredReleasedRoutePort(ctx context.Context, tx *sql.Tx, routeID int64, portRange PortRange) (int, error) {
	var port int
	err := tx.QueryRowContext(ctx, `select port from leases where route_id=? and state=? order by released_at desc, id desc limit 1`, routeID, StateReleased).Scan(&port)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if port < portRange.Start || port > portRange.End || !isPortFree(port) {
		return 0, nil
	}
	active, err := activePortCount(ctx, tx, port)
	if err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, nil
	}
	return port, nil
}

func preferredReleasedBarePort(ctx context.Context, tx *sql.Tx, path, normalizedName string, portRange PortRange) (int, error) {
	var port int
	err := tx.QueryRowContext(ctx, `select port from ports where path=? and normalized_name=? and state=? order by released_at desc, id desc limit 1`, path, normalizedName, StateReleased).Scan(&port)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if port < portRange.Start || port > portRange.End || !isPortFree(port) {
		return 0, nil
	}
	active, err := activePortCount(ctx, tx, port)
	if err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, nil
	}
	return port, nil
}

func (s *Store) nextPort(ctx context.Context, tx *sql.Tx, portRange PortRange) (int, error) {
	rows, err := tx.QueryContext(ctx, `select port from leases where state=?
union
select port from ports where state=?`, StateActive, StateActive)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return 0, err
		}
		used[port] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for port := portRange.Start; port <= portRange.End; port++ {
		if used[port] || !isPortFree(port) {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d", portRange.Start, portRange.End)
}

func activePortCount(ctx context.Context, tx *sql.Tx, port int) (int, error) {
	var active int
	err := tx.QueryRowContext(ctx, `select
	(select count(*) from leases where port=? and state=?) +
	(select count(*) from ports where port=? and state=?)`, port, StateActive, port, StateActive).Scan(&active)
	if err != nil {
		return 0, err
	}
	return active, nil
}

func ensurePortAvailable(ctx context.Context, tx *sql.Tx, port int) error {
	active, err := activePortCount(ctx, tx, port)
	if err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("port %d is already active", port)
	}
	return nil
}

func hostKind(host, normalizedRoot, normalizedName string) identity.HostKind {
	switch host {
	case "":
		return ""
	case normalizedName + "." + normalizedRoot + ".lewp":
		return identity.HostKindInstance
	case normalizedRoot + ".lewp":
		return identity.HostKindApex
	default:
		return identity.HostKindCustom
	}
}

func wildcardMatches(pattern, host string) bool {
	pattern = normalizeRouteHost(pattern)
	host = normalizeRouteHost(host)
	if !isWildcardHost(pattern) {
		return false
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	needle := "." + suffix
	if !strings.HasSuffix(host, needle) {
		return false
	}
	label := strings.TrimSuffix(host, needle)
	return label != "" && !strings.Contains(label, ".")
}

func isWildcardHost(host string) bool {
	host = normalizeRouteHost(host)
	return strings.HasPrefix(host, "*.") && len(host) > 2
}

func normalizeRouteHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
