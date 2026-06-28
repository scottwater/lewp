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
// route's host and port. Leases stay attached to their identities, so the move
// never reallocates a port. It fails if source and destination are the same, if
// fromPath has no active route, or if the destination already owns an active
// route (the caller must release that route first).
func (s *Store) MovePath(ctx context.Context, fromPath, toPath string, kind identity.Kind) ([]Record, error) {
	if fromPath == toPath {
		return nil, fmt.Errorf("source and destination are the same directory")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `select i.id, l.id, i.root, i.name, i.host, i.kind, i.normalized_name, l.port
from identities i
join leases l on l.identity_id = i.id
where i.path=? and i.kind=? and l.state=?
order by i.normalized_name`, fromPath, kind, StateActive)
	if err != nil {
		return nil, err
	}
	var moved []Record
	var names []string
	for rows.Next() {
		var r Record
		var normalizedName string
		if err := rows.Scan(&r.IdentityID, &r.LeaseID, &r.Root, &r.Name, &r.Host, &r.Kind, &normalizedName, &r.Port); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.Path = toPath
		r.State = StateActive
		moved = append(moved, r)
		names = append(names, normalizedName)
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
	var existingDestination int
	err = tx.QueryRowContext(ctx, `select count(*)
from identities i
join leases l on l.identity_id = i.id
where i.path=? and i.kind=? and l.state=?`, toPath, kind, StateActive).Scan(&existingDestination)
	if err != nil {
		return nil, err
	}
	if existingDestination > 0 {
		return nil, fmt.Errorf("destination already has an active %s route; release it first", kind)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, r := range moved {
		var existing int64
		err := tx.QueryRowContext(ctx, `select id from identities where path=? and kind=? and normalized_name=?`, toPath, kind, names[i]).Scan(&existing)
		if err == nil {
			return nil, fmt.Errorf("destination already has a %s route named %q; release it first", kind, r.Name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `update identities set path=?, updated_at=? where id=?`, toPath, now, r.IdentityID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into events(identity_id, event_type, message, created_at) values(?, ?, ?, ?)`,
			r.IdentityID, "moved", fmt.Sprintf("moved route from %s to %s", fromPath, toPath), now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return moved, nil
}
