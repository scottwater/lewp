package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/scottwater/lewp/internal/identity"
)

// ErrNoRouteForPath reports that the source path has no active route to move.
var ErrNoRouteForPath = errors.New("no active route registered for path")

// MovePath reassigns every active route at fromPath to toPath, preserving each
// route host and port. Leases and route_hosts stay attached to their routes, so
// the move never reallocates a port. It fails if source and destination are the
// same, if fromPath has no active route, or if the destination already owns a
// route.
func (s *Store) MovePath(ctx context.Context, fromPath, toPath string, kind identity.Kind) ([]Record, error) {
	if fromPath == toPath {
		return nil, fmt.Errorf("source and destination are the same directory")
	}
	// MovePath relocates active routes (and the ports their leases hold) between
	// paths and clears the destination, so it serializes with every active-port
	// writer (allocMu) and route-host mutator (hostMu). Acquire allocMu before
	// hostMu to match the lock order used by Lease and Remember.
	s.allocMu.Lock()
	defer s.allocMu.Unlock()
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `select r.id, rh.id, l.id, r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.host_type, l.port
from routes r
join route_hosts rh on rh.route_id = r.id and rh.host_type=?
join leases l on l.route_id = r.id
where r.path=? and l.state=?
order by r.normalized_name, rh.id`, HostTypePrimary, fromPath, StateActive)
	if err != nil {
		return nil, err
	}
	var moved []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.RouteID, &r.HostID, &r.LeaseID, &r.Root, &r.Name, &r.NormalizedRoot, &r.NormalizedName, &r.Host, &r.HostType, &r.Port); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.Path = toPath
		r.Kind = kind
		r.State = StateActive
		moved = append(moved, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(moved) == 0 {
		return nil, ErrNoRouteForPath
	}
	var activeDestination int
	err = tx.QueryRowContext(ctx, `select count(*)
from routes r
join leases l on l.route_id = r.id
where r.path=? and l.state=?`, toPath, StateActive).Scan(&activeDestination)
	if err != nil {
		return nil, err
	}
	if activeDestination > 0 {
		return nil, fmt.Errorf("destination already has a %s route; release it first", kind)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `delete from events where route_id in (select id from routes where path=?)`, toPath); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `delete from routes where path=? and not exists (select 1 from leases where leases.route_id=routes.id and leases.state=?)`, toPath, StateActive); err != nil {
		return nil, err
	}
	for _, r := range moved {
		if _, err := tx.ExecContext(ctx, `update routes set path=?, updated_at=? where id=?`, toPath, now, r.RouteID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into events(route_id, event_type, message, created_at) values(?, ?, ?, ?)`,
			r.RouteID, "moved", fmt.Sprintf("moved route from %s to %s", fromPath, toPath), now); err != nil {
			return nil, err
		}
	}
	var result []Record
	for _, r := range moved {
		hosts, err := movedRouteHosts(ctx, tx, r.RouteID, kind)
		if err != nil {
			return nil, err
		}
		result = append(result, hosts...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func movedRouteHosts(ctx context.Context, tx *sql.Tx, routeID int64, kind identity.Kind) ([]Record, error) {
	rows, err := tx.QueryContext(ctx, `select r.id, rh.id, l.id, r.root, r.name, r.normalized_root, r.normalized_name, rh.host, rh.host_type, r.path, l.port, l.state, coalesce(l.last_seen_at, ''), coalesce(l.released_at, '')
from routes r
join route_hosts rh on rh.route_id = r.id
join leases l on l.route_id = r.id
where r.id=? and l.state=?
order by case rh.host_type when 'primary' then 0 when 'alias' then 1 when 'wildcard' then 2 else 3 end, rh.host`, routeID, StateActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.RouteID, &r.HostID, &r.LeaseID, &r.Root, &r.Name, &r.NormalizedRoot, &r.NormalizedName, &r.Host, &r.HostType, &r.Path, &r.Port, &r.State, &r.LastSeenAt, &r.ReleasedAt); err != nil {
			return nil, err
		}
		r.Kind = kind
		r.MatchedHost = r.Host
		records = append(records, r)
	}
	return records, rows.Err()
}
