package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

type Store struct {
	db *sql.DB
	// allocMu serializes lease allocation so concurrent Lease calls cannot
	// SELECT the same free port before either inserts and then collide on the
	// leases_port_active_uq unique index (busy_timeout does not retry
	// constraint violations).
	allocMu sync.Mutex
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
	store := &Store{db: db}
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
	return (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

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

func (s *Store) Lease(ctx context.Context, ident identity.Result, portRange PortRange) (Lease, error) {
	if portRange.Start <= 0 || portRange.End < portRange.Start {
		return Lease{}, errors.New("invalid port range")
	}
	if ident.Kind == identity.KindPort {
		return s.leasePort(ctx, ident, portRange)
	}
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
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
	if err := ensureHostAvailable(ctx, tx, ident.Host, routeID); err != nil {
		return Lease{}, err
	}
	if err := upsertPrimaryHost(ctx, tx, routeID, ident, now); err != nil {
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
	if err := ensureHostAvailable(ctx, tx, ident.Host, routeID); err != nil {
		return err
	}
	if err := upsertPrimaryHost(ctx, tx, routeID, ident, now); err != nil {
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
		return Record{}, false, nil
	}
	r.MatchedHost = r.Host
	r.Kind = identity.KindRoute
	return r, err == nil, err
}

func (s *Store) TouchLastSeen(ctx context.Context, leaseID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update leases set last_seen_at=?, updated_at=? where id=?`, now, now, leaseID)
	return err
}

func (s *Store) List(ctx context.Context, all bool) ([]Record, error) {
	query := `select r.id, rh.id, coalesce(l.id, 0), r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.host_type, 'route', r.path, coalesce(l.port, 0), coalesce(l.state, ''), coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from routes r
join route_hosts rh on rh.route_id = r.id and rh.host_type=?
left join leases l on l.route_id = r.id
where (? or l.state = ?)
union all
select p.id, 0, p.id, '', p.name, '', p.normalized_name, '', '', 'port', p.path, p.port, p.state, '', coalesce(p.released_at, '')
from ports p
where (? or p.state = ?)
order by 8, 11, 5`
	rows, err := s.db.QueryContext(ctx, query, HostTypePrimary, all, StateActive, all, StateActive)
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

func (s *Store) leasePort(ctx context.Context, ident identity.Result, portRange PortRange) (Lease, error) {
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	if lease, ok, err := activePortLease(ctx, tx, ident.Path, ident.NormalizedName); err != nil {
		return Lease{}, err
	} else if ok {
		return lease, tx.Commit()
	}
	port, err := preferredReleasedBarePort(ctx, tx, ident.Path, ident.NormalizedName, portRange)
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
	res, err := tx.ExecContext(ctx, `insert into ports(path, name, normalized_name, port, state, created_at, updated_at) values(?, ?, ?, ?, ?, ?, ?)`,
		ident.Path, ident.Name, ident.NormalizedName, port, StateActive, now, now)
	if err != nil {
		return Lease{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{ID: id, Port: port, State: StateActive, Created: true}, nil
}

func (s *Store) rememberPort(ctx context.Context, ident identity.Result, port int) error {
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
	if ident.Host == "" {
		return nil
	}
	source := string(ident.HostSource)
	if source == "" {
		source = string(identity.SourceInferred)
	}
	var id int64
	err := tx.QueryRowContext(ctx, `select id from route_hosts where route_id=? and host_type=?`, routeID, HostTypePrimary).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `update route_hosts set host=?, source=?, updated_at=? where id=?`, ident.Host, source, now, id)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into route_hosts(route_id, host, host_type, source, created_at, updated_at) values(?, ?, ?, ?, ?, ?)`,
		routeID, ident.Host, HostTypePrimary, source, now, now)
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

func activePortLease(ctx context.Context, tx *sql.Tx, path, normalizedName string) (Lease, bool, error) {
	var lease Lease
	err := tx.QueryRowContext(ctx, `select id, port, state from ports where path=? and normalized_name=? and state=?`, path, normalizedName, StateActive).
		Scan(&lease.ID, &lease.Port, &lease.State)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	return lease, err == nil, err
}

func ensureHostAvailable(ctx context.Context, tx *sql.Tx, host string, routeID int64) error {
	if host == "" {
		return nil
	}
	var ownerPath string
	err := tx.QueryRowContext(ctx, `select r.path
from route_hosts rh
join routes r on r.id = rh.route_id
join leases l on l.route_id = r.id
where rh.host=? and l.state=? and r.id<>?
limit 1`, host, StateActive, routeID).Scan(&ownerPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("host %s is already active: %s", host, ownerPath)
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
	if activePortCount(ctx, tx, port) > 0 {
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
	if activePortCount(ctx, tx, port) > 0 {
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

func activePortCount(ctx context.Context, tx *sql.Tx, port int) int {
	var active int
	err := tx.QueryRowContext(ctx, `select
	(select count(*) from leases where port=? and state=?) +
	(select count(*) from ports where port=? and state=?)`, port, StateActive, port, StateActive).Scan(&active)
	if err != nil {
		return 1
	}
	return active
}

func ensurePortAvailable(ctx context.Context, tx *sql.Tx, port int) error {
	if activePortCount(ctx, tx, port) > 0 {
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

func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
