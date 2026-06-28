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
)

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

type Lease struct {
	ID         int64
	IdentityID int64
	Port       int
	State      string
	// Created reports whether this call allocated a fresh active lease (true)
	// or returned an existing remembered one (false). Callers use it to surface
	// new vs reused lease state without a second query.
	Created bool
}

type Record struct {
	IdentityID int64
	LeaseID    int64
	Root       string
	Name       string
	Host       string
	Kind       identity.Kind
	Path       string
	Port       int
	State      string
	LastSeenAt string
	ReleasedAt string
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
	schema := `
create table if not exists identities (
	id integer primary key autoincrement,
	root text not null,
	name text not null,
	normalized_root text not null,
	normalized_name text not null,
	host text not null default '',
	host_kind text not null default '',
	host_source text not null default '',
	path text not null,
	kind text not null check (kind in ('route','port')),
	created_at text not null,
	updated_at text not null
);
create unique index if not exists identities_path_kind_name_uq on identities(path, kind, normalized_name);
drop index if exists identities_active_host_uq;

create table if not exists leases (
	id integer primary key autoincrement,
	identity_id integer not null references identities(id),
	port integer not null,
	state text not null,
	last_seen_at text,
	released_at text,
	created_at text not null,
	updated_at text not null
);
create unique index if not exists leases_identity_active_uq on leases(identity_id) where state = 'active';
create unique index if not exists leases_port_active_uq on leases(port) where state = 'active';

create table if not exists events (
	id integer primary key autoincrement,
	identity_id integer references identities(id),
	event_type text not null,
	message text not null,
	metadata_json text not null default '{}',
	created_at text not null
);`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) Lease(ctx context.Context, ident identity.Result, portRange PortRange) (Lease, error) {
	if portRange.Start <= 0 || portRange.End < portRange.Start {
		return Lease{}, errors.New("invalid port range")
	}
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()

	identityID, err := upsertIdentity(ctx, tx, ident, now)
	if err != nil {
		return Lease{}, err
	}
	if err := ensureHostAvailable(ctx, tx, ident.Host, identityID); err != nil {
		return Lease{}, err
	}
	if lease, ok, err := activeLease(ctx, tx, identityID); err != nil {
		return Lease{}, err
	} else if ok {
		return lease, tx.Commit()
	}

	port, err := preferredReleasedPort(ctx, tx, identityID, portRange)
	if err != nil {
		return Lease{}, err
	}
	if port == 0 {
		port, err = s.nextPort(ctx, tx, portRange)
	}
	if err != nil {
		return Lease{}, err
	}
	res, err := tx.ExecContext(ctx, `insert into leases(identity_id, port, state, created_at, updated_at) values(?, ?, ?, ?, ?)`,
		identityID, port, StateActive, now, now)
	if err != nil {
		return Lease{}, err
	}
	leaseID, err := res.LastInsertId()
	if err != nil {
		return Lease{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into events(identity_id, event_type, message, created_at) values(?, ?, ?, ?)`,
		identityID, "lease_created", fmt.Sprintf("leased port %d", port), now); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{ID: leaseID, IdentityID: identityID, Port: port, State: StateActive, Created: true}, nil
}

func (s *Store) Identity(ctx context.Context, id int64) (identity.Result, error) {
	var got identity.Result
	err := s.db.QueryRowContext(ctx, `select root, name, normalized_root, normalized_name, host, host_kind, host_source, path, kind from identities where id=?`, id).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &got.HostKind, &got.HostSource, &got.Path, &got.Kind)
	return got, err
}

func (s *Store) Remember(ctx context.Context, ident identity.Result, port int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	identityID, err := upsertIdentity(ctx, tx, ident, now)
	if err != nil {
		return err
	}
	if err := ensureHostAvailable(ctx, tx, ident.Host, identityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where identity_id=? and state=?`, StateReleased, now, now, identityID, StateActive); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into leases(identity_id, port, state, created_at, updated_at) values(?, ?, ?, ?, ?)`, identityID, port, StateActive, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindByHost(ctx context.Context, host string) (identity.Result, bool, error) {
	var got identity.Result
	err := s.db.QueryRowContext(ctx, `select i.root, i.name, i.normalized_root, i.normalized_name, i.host, i.host_kind, i.host_source, i.path, i.kind
from identities i
join leases l on l.identity_id = i.id
where i.host=? and l.state=?`, host, StateActive).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &got.HostKind, &got.HostSource, &got.Path, &got.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Result{}, false, nil
	}
	return got, err == nil, err
}

func (s *Store) RememberedIdentity(ctx context.Context, path string, kind identity.Kind, normalizedName string) (identity.Result, bool, error) {
	var got identity.Result
	err := s.db.QueryRowContext(ctx, `select root, name, normalized_root, normalized_name, host, host_kind, host_source, path, kind
from identities
where path=? and kind=? and normalized_name=?`, path, kind, normalizedName).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &got.HostKind, &got.HostSource, &got.Path, &got.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Result{}, false, nil
	}
	return got, err == nil, err
}

func (s *Store) RememberedPathIdentity(ctx context.Context, path string, kind identity.Kind) (identity.Result, bool, error) {
	var got identity.Result
	err := s.db.QueryRowContext(ctx, `select root, name, normalized_root, normalized_name, host, host_kind, host_source, path, kind
from identities
where path=? and kind=?
order by updated_at desc, id desc
limit 1`, path, kind).
		Scan(&got.Root, &got.Name, &got.NormalizedRoot, &got.NormalizedName, &got.Host, &got.HostKind, &got.HostSource, &got.Path, &got.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Result{}, false, nil
	}
	return got, err == nil, err
}

func (s *Store) RouteByHost(ctx context.Context, host string) (Record, bool, error) {
	var r Record
	err := s.db.QueryRowContext(ctx, `select i.id, l.id, i.root, i.name, i.host, i.kind, i.path, l.port, l.state, coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from identities i
join leases l on l.identity_id = i.id
where i.host=? and l.state=?`, host, StateActive).
		Scan(&r.IdentityID, &r.LeaseID, &r.Root, &r.Name, &r.Host, &r.Kind, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) TouchLastSeen(ctx context.Context, leaseID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update leases set last_seen_at=?, updated_at=? where id=?`, now, now, leaseID)
	return err
}

func (s *Store) List(ctx context.Context, all bool) ([]Record, error) {
	query := `select i.id, coalesce(l.id, 0), i.root, i.name, i.host, i.kind, i.path, coalesce(l.port, 0), coalesce(l.state, ''), coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from identities i
left join leases l on l.identity_id = i.id
where (? or coalesce(l.state, 'active') = ?)
order by i.host, i.path, i.name`
	rows, err := s.db.QueryContext(ctx, query, all, StateActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.IdentityID, &r.LeaseID, &r.Root, &r.Name, &r.Host, &r.Kind, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Release releases the active lease for a single identity (path+kind+name) and
// reports how many active leases were freed (0 if the identity is unknown or
// already released). With forget it also deletes the remembered identity.
func (s *Store) Release(ctx context.Context, path string, kind identity.Kind, normalizedName string, forget bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `select id from identities where path=? and kind=? and normalized_name=?`, path, kind, normalizedName).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where identity_id=? and state=?`, StateReleased, now, now, id, StateActive)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into events(identity_id, event_type, message, created_at) values(?, ?, ?, ?)`, id, "released", "released lease", now); err != nil {
		return 0, err
	}
	if forget {
		if _, err := tx.ExecContext(ctx, `delete from leases where identity_id=?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `delete from events where identity_id=?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `delete from identities where id=?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

// ReleasePath releases the active leases for every identity at path of the given
// kind and reports how many active leases were freed. With forget it also
// deletes the remembered identities.
func (s *Store) ReleasePath(ctx context.Context, path string, kind identity.Kind, forget bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id from identities where path=? and kind=?`, path, kind)
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
		res, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where identity_id=? and state=?`, StateReleased, now, now, id, StateActive)
		if err != nil {
			return 0, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		released += int(affected)
		if _, err := tx.ExecContext(ctx, `insert into events(identity_id, event_type, message, created_at) values(?, ?, ?, ?)`, id, "released", "released lease", now); err != nil {
			return 0, err
		}
		if forget {
			if _, err := tx.ExecContext(ctx, `delete from leases where identity_id=?`, id); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `delete from events where identity_id=?`, id); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `delete from identities where id=?`, id); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return released, nil
}

func upsertIdentity(ctx context.Context, tx *sql.Tx, ident identity.Result, now string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `select id from identities where path=? and kind=? and normalized_name=?`, ident.Path, ident.Kind, ident.NormalizedName).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `update identities set root=?, name=?, normalized_root=?, normalized_name=?, host=?, host_kind=?, host_source=?, updated_at=? where id=?`,
			ident.Root, ident.Name, ident.NormalizedRoot, ident.NormalizedName, ident.Host, ident.HostKind, ident.HostSource, now, id)
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `insert into identities(root, name, normalized_root, normalized_name, host, host_kind, host_source, path, kind, created_at, updated_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ident.Root, ident.Name, ident.NormalizedRoot, ident.NormalizedName, ident.Host, ident.HostKind, ident.HostSource, ident.Path, ident.Kind, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func activeLease(ctx context.Context, tx *sql.Tx, identityID int64) (Lease, bool, error) {
	var lease Lease
	err := tx.QueryRowContext(ctx, `select id, identity_id, port, state from leases where identity_id=? and state=?`, identityID, StateActive).
		Scan(&lease.ID, &lease.IdentityID, &lease.Port, &lease.State)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	return lease, err == nil, err
}

func ensureHostAvailable(ctx context.Context, tx *sql.Tx, host string, identityID int64) error {
	if host == "" {
		return nil
	}
	var ownerID int64
	err := tx.QueryRowContext(ctx, `select i.id
from identities i
join leases l on l.identity_id = i.id
where i.host=? and l.state=? and i.id<>?
limit 1`, host, StateActive, identityID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("host %s is already active", host)
}

func preferredReleasedPort(ctx context.Context, tx *sql.Tx, identityID int64, portRange PortRange) (int, error) {
	var port int
	err := tx.QueryRowContext(ctx, `select port from leases where identity_id=? and state=? order by released_at desc, id desc limit 1`, identityID, StateReleased).Scan(&port)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if port < portRange.Start || port > portRange.End || !isPortFree(port) {
		return 0, nil
	}
	var active int
	err = tx.QueryRowContext(ctx, `select count(*) from leases where port=? and state=?`, port, StateActive).Scan(&active)
	if err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, nil
	}
	return port, nil
}

func (s *Store) nextPort(ctx context.Context, tx *sql.Tx, portRange PortRange) (int, error) {
	rows, err := tx.QueryContext(ctx, `select port from leases where state=?`, StateActive)
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

func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
