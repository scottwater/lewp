package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/scottwater/lewp/internal/identity"
)

type ReleaseSelectorType string

const (
	ReleaseSelectorPath ReleaseSelectorType = "path"
	ReleaseSelectorHost ReleaseSelectorType = "host"
	ReleaseSelectorPort ReleaseSelectorType = "port"
)

type ReleaseScope string

const (
	ReleaseScopeAll   ReleaseScope = "all"
	ReleaseScopeRoute ReleaseScope = "route"
	ReleaseScopeName  ReleaseScope = "name"
)

type ReleaseAction string

const (
	ReleaseActionRelease ReleaseAction = "release"
	ReleaseActionForget  ReleaseAction = "forget"
)

type ReleaseSelector struct {
	Type      ReleaseSelectorType
	Path      string
	Host      string
	Port      int
	Recursive bool
	Scope     ReleaseScope
	Name      string
}

type ReleasePlanHost struct {
	ID   int64
	Host string
	Type string
}

type ReleaseLeaseVersion struct {
	ID    int64
	Port  int
	State string
}

type ReleasePortVersion struct {
	ID             int64
	Name           string
	NormalizedName string
	Port           int
	State          string
}

type ReleasePlanItem struct {
	Kind          identity.Kind
	Path          string
	Name          string
	Host          string
	RegistryState string
	ActivePort    *int
	Ports         []int
	Actions       []ReleaseAction
	RouteID       int64
	Hosts         []ReleasePlanHost
	Leases        []ReleaseLeaseVersion
	PortRows      []ReleasePortVersion
}

type ReleasePlan struct {
	Selector    ReleaseSelector
	Forget      bool
	Items       []ReleasePlanItem
	Fingerprint []ReleasePlanItem
}

type ReleaseApplyResult struct {
	Released  int
	Forgotten int
}

var ErrReleasePlanChanged = errors.New("release plan changed; rerun the release command")

// PlanRelease builds a deterministic logical-allocation plan from one registry
// snapshot. It does not mutate the registry.
func (s *Store) PlanRelease(ctx context.Context, selector ReleaseSelector, forget bool) (plan ReleasePlan, err error) {
	canonical, err := validateReleaseSelector(selector)
	if err != nil {
		return ReleasePlan{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReleasePlan{}, err
	}
	committed := false
	defer func() {
		err = joinReleaseRollbackError(err, tx.Rollback(), committed, "plan release")
	}()
	plan, err = buildReleasePlan(ctx, tx, canonical, forget)
	if err != nil {
		return ReleasePlan{}, err
	}
	if err = tx.Commit(); err != nil {
		return ReleasePlan{}, err
	}
	committed = true
	return plan, nil
}

// ApplyRelease validates and applies a previously built plan atomically. The
// DSN's _txlock=immediate setting makes this transaction take SQLite's writer
// lock before rebuilding the plan, serializing validation with later writers.
func (s *Store) ApplyRelease(ctx context.Context, plan ReleasePlan) (result ReleaseApplyResult, err error) {
	selector, err := validateReleaseSelector(plan.Selector)
	if err != nil {
		return ReleaseApplyResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReleaseApplyResult{}, err
	}
	committed := false
	defer func() {
		err = joinReleaseRollbackError(err, tx.Rollback(), committed, "apply release")
	}()

	current, err := buildReleasePlan(ctx, tx, selector, plan.Forget)
	if err != nil {
		return ReleaseApplyResult{}, err
	}
	if !reflect.DeepEqual(plan.Selector, current.Selector) || plan.Forget != current.Forget || !reflect.DeepEqual(plan.Fingerprint, current.Fingerprint) {
		return ReleaseApplyResult{}, ErrReleasePlanChanged
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range current.Items {
		for _, action := range item.Actions {
			switch action {
			case ReleaseActionRelease:
				if err := applyLogicalRelease(ctx, tx, item, now); err != nil {
					return ReleaseApplyResult{}, err
				}
				result.Released++
			case ReleaseActionForget:
				if err := applyLogicalForget(ctx, tx, item); err != nil {
					return ReleaseApplyResult{}, err
				}
				result.Forgotten++
			default:
				return ReleaseApplyResult{}, fmt.Errorf("invalid release action %q", action)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return ReleaseApplyResult{}, err
	}
	committed = true
	return result, nil
}

func joinReleaseRollbackError(original, rollbackErr error, committed bool, operation string) error {
	if rollbackErr == nil || committed && errors.Is(rollbackErr, sql.ErrTxDone) {
		return original
	}
	rollbackErr = fmt.Errorf("%s transaction rollback: %w", operation, rollbackErr)
	if original == nil {
		return rollbackErr
	}
	return errors.Join(original, rollbackErr)
}

func validateReleaseSelector(selector ReleaseSelector) (ReleaseSelector, error) {
	switch selector.Type {
	case ReleaseSelectorPath:
		if selector.Path == "" {
			return ReleaseSelector{}, errors.New("path release selector requires a path")
		}
		if selector.Host != "" || selector.Port != 0 {
			return ReleaseSelector{}, errors.New("path release selector cannot include a host or port")
		}
		switch selector.Scope {
		case ReleaseScopeAll, ReleaseScopeRoute:
			if selector.Name != "" {
				return ReleaseSelector{}, fmt.Errorf("%s path release scope cannot include a name", selector.Scope)
			}
		case ReleaseScopeName:
			if selector.Name == "" {
				return ReleaseSelector{}, errors.New("name release scope requires a name")
			}
			normalizedName, _, err := identity.NormalizeLabel(selector.Name)
			if err != nil {
				return ReleaseSelector{}, fmt.Errorf("invalid release name %q: %w", selector.Name, err)
			}
			selector.Name = normalizedName
		default:
			return ReleaseSelector{}, fmt.Errorf("invalid path release scope %q", selector.Scope)
		}
		absolute, err := filepath.Abs(selector.Path)
		if err != nil {
			return ReleaseSelector{}, fmt.Errorf("resolve release path: %w", err)
		}
		selector.Path = filepath.Clean(absolute)
		return selector, nil
	case ReleaseSelectorHost:
		if selector.Path != "" || selector.Port != 0 || selector.Recursive || selector.Scope != "" || selector.Name != "" {
			return ReleaseSelector{}, errors.New("host release selector cannot include path, port, recursive, scope, or name values")
		}
		selector.Host = normalizeRouteHost(selector.Host)
		if selector.Host == "" {
			return ReleaseSelector{}, errors.New("host release selector requires a host")
		}
		return selector, nil
	case ReleaseSelectorPort:
		if selector.Path != "" || selector.Host != "" || selector.Recursive || selector.Scope != "" || selector.Name != "" {
			return ReleaseSelector{}, errors.New("port release selector cannot include path, host, recursive, scope, or name values")
		}
		if selector.Port < 1 || selector.Port > 65535 {
			return ReleaseSelector{}, fmt.Errorf("invalid release port %d", selector.Port)
		}
		return selector, nil
	default:
		return ReleaseSelector{}, fmt.Errorf("invalid release selector type %q", selector.Type)
	}
}

func buildReleasePlan(ctx context.Context, tx *sql.Tx, selector ReleaseSelector, forget bool) (ReleasePlan, error) {
	routes, err := readReleaseRoutes(ctx, tx)
	if err != nil {
		return ReleasePlan{}, err
	}
	ports, err := readReleasePorts(ctx, tx)
	if err != nil {
		return ReleasePlan{}, err
	}

	var fingerprint []ReleasePlanItem
	switch selector.Type {
	case ReleaseSelectorPath:
		for _, item := range routes {
			if selector.Scope != ReleaseScopeName && releasePathMatches(item.Path, selector.Path, selector.Recursive) {
				fingerprint = append(fingerprint, item)
			}
		}
		for _, item := range ports {
			if selector.Scope != ReleaseScopeRoute && releasePathMatches(item.Path, selector.Path, selector.Recursive) &&
				(selector.Scope != ReleaseScopeName || item.Name == selector.Name) {
				fingerprint = append(fingerprint, item)
			}
		}
	case ReleaseSelectorHost:
		for _, item := range routes {
			if item.RegistryState != StateActive {
				continue
			}
			for _, host := range item.Hosts {
				if host.Host == selector.Host {
					fingerprint = append(fingerprint, item)
					break
				}
			}
		}
	case ReleaseSelectorPort:
		for _, item := range routes {
			if item.ActivePort != nil && *item.ActivePort == selector.Port {
				fingerprint = append(fingerprint, item)
			}
		}
		for _, item := range ports {
			if item.ActivePort != nil && *item.ActivePort == selector.Port {
				fingerprint = append(fingerprint, item)
			}
		}
	}
	sortReleaseItems(fingerprint)

	items := make([]ReleasePlanItem, 0, len(fingerprint))
	for _, item := range fingerprint {
		item.Actions = releaseActions(item.RegistryState, forget)
		if len(item.Actions) != 0 {
			items = append(items, item)
		}
	}
	return ReleasePlan{Selector: selector, Forget: forget, Items: items, Fingerprint: fingerprint}, nil
}

func readReleaseRoutes(ctx context.Context, tx *sql.Tx) ([]ReleasePlanItem, error) {
	rows, err := tx.QueryContext(ctx, `select id, path, normalized_name from routes`)
	if err != nil {
		return nil, err
	}
	var items []ReleasePlanItem
	for rows.Next() {
		var item ReleasePlanItem
		if err := rows.Scan(&item.RouteID, &item.Path, &item.Name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.Kind = identity.KindRoute
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range items {
		item := &items[i]
		hostRows, err := tx.QueryContext(ctx, `select id, host, host_type from route_hosts where route_id=?
order by case host_type when 'primary' then 0 when 'alias' then 1 when 'wildcard' then 2 else 3 end, host, id`, item.RouteID)
		if err != nil {
			return nil, err
		}
		for hostRows.Next() {
			var host ReleasePlanHost
			if err := hostRows.Scan(&host.ID, &host.Host, &host.Type); err != nil {
				_ = hostRows.Close()
				return nil, err
			}
			item.Hosts = append(item.Hosts, host)
			if host.Type == HostTypePrimary {
				item.Host = host.Host
			}
		}
		if err := hostRows.Err(); err != nil {
			_ = hostRows.Close()
			return nil, err
		}
		if err := hostRows.Close(); err != nil {
			return nil, err
		}

		leaseRows, err := tx.QueryContext(ctx, `select id, port, state from leases where route_id=? order by id`, item.RouteID)
		if err != nil {
			return nil, err
		}
		for leaseRows.Next() {
			var lease ReleaseLeaseVersion
			if err := leaseRows.Scan(&lease.ID, &lease.Port, &lease.State); err != nil {
				_ = leaseRows.Close()
				return nil, err
			}
			switch lease.State {
			case StateActive:
				port := lease.Port
				item.ActivePort = &port
			case StateReleased:
			default:
				_ = leaseRows.Close()
				return nil, fmt.Errorf("route %q at path %q lease row %d has invalid state %q", item.Name, item.Path, lease.ID, lease.State)
			}
			item.Leases = append(item.Leases, lease)
		}
		if err := leaseRows.Err(); err != nil {
			_ = leaseRows.Close()
			return nil, err
		}
		if err := leaseRows.Close(); err != nil {
			return nil, err
		}
		item.Ports = uniqueReleasePortsFromLeases(item.Leases)
		item.RegistryState = StateReleased
		if item.ActivePort != nil {
			item.RegistryState = StateActive
		}
	}
	return items, nil
}

func readReleasePorts(ctx context.Context, tx *sql.Tx) ([]ReleasePlanItem, error) {
	rows, err := tx.QueryContext(ctx, `select id, path, name, normalized_name, port, state from ports order by path, normalized_name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byIdentity := make(map[string]int)
	var items []ReleasePlanItem
	for rows.Next() {
		var row ReleasePortVersion
		var path string
		if err := rows.Scan(&row.ID, &path, &row.Name, &row.NormalizedName, &row.Port, &row.State); err != nil {
			return nil, err
		}
		switch row.State {
		case StateActive, StateReleased:
		default:
			return nil, fmt.Errorf("port %q at path %q row %d has invalid state %q", row.NormalizedName, path, row.ID, row.State)
		}
		key := path + "\x00" + row.NormalizedName
		index, ok := byIdentity[key]
		if !ok {
			index = len(items)
			byIdentity[key] = index
			items = append(items, ReleasePlanItem{Kind: identity.KindPort, Path: path, Name: row.NormalizedName, RegistryState: StateReleased})
		}
		item := &items[index]
		item.PortRows = append(item.PortRows, row)
		if row.State == StateActive {
			port := row.Port
			item.ActivePort = &port
			item.RegistryState = StateActive
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Ports = uniqueReleasePortsFromRows(items[i].PortRows)
	}
	return items, nil
}

func releasePathMatches(path, root string, recursive bool) bool {
	if path == root {
		return true
	}
	if !recursive {
		return false
	}
	if root == string(filepath.Separator) {
		return filepath.IsAbs(path)
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func releaseActions(state string, forget bool) []ReleaseAction {
	if forget {
		if state == StateActive {
			return []ReleaseAction{ReleaseActionRelease, ReleaseActionForget}
		}
		return []ReleaseAction{ReleaseActionForget}
	}
	if state == StateActive {
		return []ReleaseAction{ReleaseActionRelease}
	}
	return nil
}

func sortReleaseItems(items []ReleasePlanItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == identity.KindRoute
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].RouteID < items[j].RouteID
	})
}

func uniqueReleasePortsFromLeases(rows []ReleaseLeaseVersion) []int {
	ports := make([]int, 0, len(rows))
	seen := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.Port]; ok {
			continue
		}
		seen[row.Port] = struct{}{}
		ports = append(ports, row.Port)
	}
	sort.Ints(ports)
	return ports
}

func uniqueReleasePortsFromRows(rows []ReleasePortVersion) []int {
	ports := make([]int, 0, len(rows))
	seen := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.Port]; ok {
			continue
		}
		seen[row.Port] = struct{}{}
		ports = append(ports, row.Port)
	}
	sort.Ints(ports)
	return ports
}

func applyLogicalRelease(ctx context.Context, tx *sql.Tx, item ReleasePlanItem, now string) error {
	logicalIdentity := releaseLogicalIdentity(item)
	switch item.Kind {
	case identity.KindRoute:
		res, err := tx.ExecContext(ctx, `update leases set state=?, released_at=?, updated_at=? where route_id=? and state=?`, StateReleased, now, now, item.RouteID, StateActive)
		if err != nil {
			return fmt.Errorf("release %s: update active lease: %w", logicalIdentity, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("release %s: count updated leases: %w", logicalIdentity, err)
		}
		if affected == 0 {
			return fmt.Errorf("release %s: %w", logicalIdentity, ErrReleasePlanChanged)
		}
		if _, err := tx.ExecContext(ctx, `insert into events(route_id, event_type, message, created_at) values(?, ?, ?, ?)`, item.RouteID, "released", "released lease", now); err != nil {
			return fmt.Errorf("release %s: record event: %w", logicalIdentity, err)
		}
		return nil
	case identity.KindPort:
		res, err := tx.ExecContext(ctx, `update ports set state=?, released_at=?, updated_at=? where path=? and normalized_name=? and state=?`, StateReleased, now, now, item.Path, item.Name, StateActive)
		if err != nil {
			return fmt.Errorf("release %s: update active row: %w", logicalIdentity, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("release %s: count updated rows: %w", logicalIdentity, err)
		}
		if affected == 0 {
			return fmt.Errorf("release %s: %w", logicalIdentity, ErrReleasePlanChanged)
		}
		return nil
	default:
		return fmt.Errorf("release %s: invalid item kind %q", logicalIdentity, item.Kind)
	}
}

func applyLogicalForget(ctx context.Context, tx *sql.Tx, item ReleasePlanItem) error {
	logicalIdentity := releaseLogicalIdentity(item)
	switch item.Kind {
	case identity.KindRoute:
		if _, err := tx.ExecContext(ctx, `delete from events where route_id=?`, item.RouteID); err != nil {
			return fmt.Errorf("forget %s: delete events: %w", logicalIdentity, err)
		}
		res, err := tx.ExecContext(ctx, `delete from routes where id=?`, item.RouteID)
		if err != nil {
			return fmt.Errorf("forget %s: delete route history: %w", logicalIdentity, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("forget %s: count deleted routes: %w", logicalIdentity, err)
		}
		if affected == 0 {
			return fmt.Errorf("forget %s: %w", logicalIdentity, ErrReleasePlanChanged)
		}
		return nil
	case identity.KindPort:
		res, err := tx.ExecContext(ctx, `delete from ports where path=? and normalized_name=?`, item.Path, item.Name)
		if err != nil {
			return fmt.Errorf("forget %s: delete port history: %w", logicalIdentity, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("forget %s: count deleted rows: %w", logicalIdentity, err)
		}
		if affected == 0 {
			return fmt.Errorf("forget %s: %w", logicalIdentity, ErrReleasePlanChanged)
		}
		return nil
	default:
		return fmt.Errorf("forget %s: invalid item kind %q", logicalIdentity, item.Kind)
	}
}

func releaseLogicalIdentity(item ReleasePlanItem) string {
	if item.Kind == identity.KindRoute {
		return fmt.Sprintf("route %q at path %q (row %d)", item.Name, item.Path, item.RouteID)
	}
	return fmt.Sprintf("port %q at path %q", item.Name, item.Path)
}
