package control

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
	"github.com/scottwater/lewp/internal/suffix"
	localtls "github.com/scottwater/lewp/internal/tls"
)

type Service struct {
	store           *registry.Store
	portRange       registry.PortRange
	managedSuffixes []string
}

type LeaseRequest struct {
	WorkDir string
	Root    string
	Name    string
	Host    string
	// Env carries the identity environment values (LEWP_ROOT/LEWP_NAME/
	// LEWP_HOST) collected from the *client* process. The daemon never reads its
	// own process environment, so the CLI must forward these explicitly for
	// env-based identity overrides to take effect.
	Env map[string]string
	// AutoSuffix opts an explicit --host into the deterministic-suffix conflict
	// behavior instead of failing when the host is already taken.
	AutoSuffix bool
	// Reset discards any remembered identity override (host/root/name) for this
	// directory and re-resolves from flags, env, config, and inference. The fresh
	// identity is persisted in place, so the active lease (port) and event history
	// survive — unlike release --forget, which deletes the whole route.
	Reset bool
}

// HostConflictError is returned when an explicitly requested host (--host, env,
// or config) is already assigned to a different directory. It carries the
// conflicting path and prints concrete cleanup/override guidance so the user is
// never left guessing why the lease failed.
type HostConflictError struct {
	Host      string
	OwnerPath string
}

func (e *HostConflictError) Error() string {
	return fmt.Sprintf("host %s is already assigned to %s\n"+
		"Free it:        cd %s && lewp release --route --forget\n"+
		"Use another:    lewp lease --host <name>.lewp\n"+
		"Suffix anyway:  lewp lease --host %s --auto-suffix",
		e.Host, e.OwnerPath, e.OwnerPath, e.Host)
}

type PortRequest struct {
	WorkDir string
	Name    string
	// Env carries client-process identity environment values; see LeaseRequest.
	Env map[string]string
}

type ReleaseRequest struct {
	WorkDir   string                `json:"work_dir,omitempty"`
	Path      string                `json:"path,omitempty"`
	Host      string                `json:"host,omitempty"`
	Name      string                `json:"name,omitempty"`
	Port      int                   `json:"port,omitempty"`
	Recursive bool                  `json:"recursive,omitempty"`
	Forget    bool                  `json:"forget,omitempty"`
	DryRun    bool                  `json:"dry_run,omitempty"`
	Implicit  bool                  `json:"implicit,omitempty"`
	Scope     registry.ReleaseScope `json:"scope,omitempty"`

	// Root, Kind, and All are private-protocol compatibility fields used only by
	// the legacy internal release dispatch until the CLI adopts selectors.
	Root string        `json:"root,omitempty"`
	Kind identity.Kind `json:"kind,omitempty"`
	All  bool          `json:"all,omitempty"`
}

type InfoRequest struct {
	WorkDir string
}

type MoveRequest struct {
	WorkDir string
	From    string
}

type LeaseResponse struct {
	Port           int               `json:"port"`
	URL            string            `json:"url,omitempty"`
	HTTPSURL       string            `json:"https_url,omitempty"`
	Host           string            `json:"host,omitempty"`
	Root           string            `json:"root"`
	Name           string            `json:"name"`
	NormalizedRoot string            `json:"normalized_root"`
	NormalizedName string            `json:"normalized_name"`
	Path           string            `json:"path"`
	Kind           identity.Kind     `json:"kind"`
	HostKind       identity.HostKind `json:"host_kind,omitempty"`
	RootSource     identity.Source   `json:"root_source"`
	NameSource     identity.Source   `json:"name_source"`
	HostSource     identity.Source   `json:"host_source,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
	// LeaseState is new, reused, or conflict-renamed for this call.
	LeaseState   string `json:"lease_state,omitempty"`
	ReleaseState string `json:"release_state"`
}

// ReleaseSelector is the public, ID-free description of a planned selector.
// Pointer fields make selector variants encode exactly: path-only booleans are
// present even when false, while fields belonging to other variants are absent.
type ReleaseSelector struct {
	Type      registry.ReleaseSelectorType `json:"type"`
	Path      *string                      `json:"path,omitempty"`
	Implicit  *bool                        `json:"implicit,omitempty"`
	Recursive *bool                        `json:"recursive,omitempty"`
	Scope     *registry.ReleaseScope       `json:"scope,omitempty"`
	Name      *string                      `json:"name,omitempty"`
	Host      *string                      `json:"host,omitempty"`
	Port      *int                         `json:"port,omitempty"`
}

type ReleaseHost struct {
	Host string `json:"host"`
	Type string `json:"type"`
}

type ReleaseItem struct {
	Kind    identity.Kind            `json:"kind"`
	Path    string                   `json:"path"`
	State   string                   `json:"state"`
	Port    *int                     `json:"port"`
	Ports   []int                    `json:"ports"`
	Actions []registry.ReleaseAction `json:"actions"`
	Host    string                   `json:"host,omitempty"`
	Hosts   []ReleaseHost            `json:"hosts,omitempty"`
	Name    string                   `json:"name,omitempty"`
}

type ReleaseResponse struct {
	Operation string          `json:"operation"`
	DryRun    bool            `json:"dry_run"`
	Selector  ReleaseSelector `json:"selector"`
	Matched   int             `json:"matched"`
	Released  int             `json:"released"`
	Forgotten int             `json:"forgotten"`
	Items     []ReleaseItem   `json:"items"`
}

type ListEntry struct {
	Host  string        `json:"host,omitempty"`
	Port  int           `json:"port"`
	State string        `json:"state"`
	Path  string        `json:"path"`
	Kind  identity.Kind `json:"kind"`
	Root  string        `json:"root"`
	Name  string        `json:"name"`
}

func NewService(store *registry.Store, portRange registry.PortRange) *Service {
	return &Service{store: store, portRange: portRange, managedSuffixes: canonicalManagedSuffixes(nil)}
}

func (s *Service) SetManagedSuffixes(managed []string) {
	s.managedSuffixes = canonicalManagedSuffixes(managed)
}

func canonicalManagedSuffixes(managed []string) []string {
	seen := map[string]struct{}{suffix.BuiltIn: {}}
	custom := make([]string, 0, len(managed))
	for _, raw := range managed {
		name, err := suffix.Normalize(raw)
		if err != nil {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		custom = append(custom, name)
	}
	sort.Strings(custom)
	return append([]string{suffix.BuiltIn}, custom...)
}

// requestEnv normalizes a request's forwarded environment to a non-nil map.
// Passing an empty map guarantees the daemon never honors its own LEWP_*
// variables; only values the client explicitly forwarded can affect identity.
func requestEnv(env map[string]string) map[string]string {
	if env == nil {
		return map[string]string{}
	}
	return env
}

func (s *Service) Lease(ctx context.Context, req LeaseRequest) (LeaseResponse, error) {
	resolved, err := identity.Resolve(ctx, identity.Options{
		WorkDir:         req.WorkDir,
		Root:            req.Root,
		Name:            req.Name,
		Host:            req.Host,
		Env:             requestEnv(req.Env),
		Kind:            identity.KindRoute,
		ManagedSuffixes: s.managedSuffixes,
	})
	if err != nil {
		return LeaseResponse{}, err
	}
	if req.Reset {
		// --reset re-resolves purely from flags/env/config/inference: skip both
		// remembered-identity restorations below so the persisted override does not
		// come back. Report which override is being cleared so the user sees that
		// the reset took effect (leasing below overwrites the route/host in place).
		if remembered, ok, err := s.store.RememberedPathIdentity(ctx, resolved.Path, identity.KindRoute); err != nil {
			return LeaseResponse{}, err
		} else if ok {
			if warning, changed := describeReset(remembered, resolved); changed {
				resolved.Warnings = append(resolved.Warnings, warning)
			}
		}
	} else {
		if req.Root == "" && req.Name == "" && req.Host == "" &&
			resolved.RootSource == identity.SourceInferred &&
			resolved.NameSource == identity.SourceInferred &&
			resolved.HostSource == identity.SourceInferred {
			if remembered, ok, err := s.store.RememberedPathIdentity(ctx, resolved.Path, identity.KindRoute); err != nil {
				return LeaseResponse{}, err
			} else if ok {
				resolved = remembered
			}
		}
		if resolved.HostSource == identity.SourceInferred {
			if remembered, ok, err := s.store.RememberedIdentity(ctx, resolved.Path, identity.KindRoute, resolved.NormalizedName); err != nil {
				return LeaseResponse{}, err
			} else if ok && remembered.Host != "" && remembered.HostSource != identity.SourceInferred {
				resolved.Host = remembered.Host
				resolved.HostKind = remembered.HostKind
				resolved.HostSource = remembered.HostSource
			}
		}
	}
	conflictRenamed := false
	if owner, ok, err := s.store.FindByHost(ctx, resolved.Host); err != nil {
		return LeaseResponse{}, err
	} else if ok && owner.Path != resolved.Path {
		// An explicit host (CLI/env/config) must not silently change out from
		// under the user: fail with the conflicting path and cleanup guidance
		// unless they opt into suffixing. Inferred hosts keep the deterministic
		// suffix so repeat leases stay stable without intervention.
		explicit := resolved.HostSource != "" && resolved.HostSource != identity.SourceInferred
		if explicit && !req.AutoSuffix {
			return LeaseResponse{}, &HostConflictError{Host: resolved.Host, OwnerPath: owner.Path}
		}
		original := resolved.Host
		resolved.Host = suffixedHost(resolved.Host, resolved.Path, s.managedSuffixes)
		resolved.HostKind = identity.HostKindCustom
		resolved.Warnings = append(resolved.Warnings,
			fmt.Sprintf("warning: %s is already assigned to %s; using %s", original, owner.Path, resolved.Host))
		conflictRenamed = true
	}
	lease, err := s.store.Lease(ctx, resolved, s.portRange)
	if err != nil {
		return LeaseResponse{}, err
	}
	return response(resolved, lease.Port, leaseState(lease, conflictRenamed)), nil
}

func (s *Service) Port(ctx context.Context, req PortRequest) (LeaseResponse, error) {
	resolved, err := identity.Resolve(ctx, identity.Options{
		WorkDir: req.WorkDir,
		Name:    req.Name,
		Env:     requestEnv(req.Env),
		Kind:    identity.KindPort,
	})
	if err != nil {
		return LeaseResponse{}, err
	}
	lease, err := s.store.Lease(ctx, resolved, s.portRange)
	if err != nil {
		return LeaseResponse{}, err
	}
	return response(resolved, lease.Port, leaseState(lease, false)), nil
}

// PlanRelease canonicalizes a public selector and asks the registry for one
// deterministic snapshot. The returned public response intentionally contains
// no registry row IDs or fingerprint data.
func (s *Service) PlanRelease(ctx context.Context, req ReleaseRequest) (ReleaseResponse, registry.ReleasePlan, error) {
	selector, publicSelector, err := canonicalReleaseSelector(req)
	if err != nil {
		return ReleaseResponse{}, registry.ReleasePlan{}, err
	}
	plan, err := s.store.PlanRelease(ctx, selector, req.Forget)
	if err != nil {
		return ReleaseResponse{}, registry.ReleasePlan{}, err
	}
	return releaseResponse(plan, publicSelector, req.DryRun), plan, nil
}

// ApplyRelease maps the validated plan to its public pre-mutation form before
// atomically applying it. A failed registry validation/application never returns
// a successful-looking response.
func (s *Service) ApplyRelease(ctx context.Context, plan registry.ReleasePlan, dryRun bool) (ReleaseResponse, error) {
	publicSelector := publicReleaseSelector(plan.Selector, false)
	result := releaseResponse(plan, publicSelector, dryRun)
	if dryRun {
		return result, nil
	}
	applied, err := s.store.ApplyRelease(ctx, plan)
	if err != nil {
		return ReleaseResponse{}, err
	}
	result.Released = applied.Released
	result.Forgotten = applied.Forgotten
	return result, nil
}

// Release preserves the legacy private command as immediate plan plus atomic
// apply. It does not use the older per-kind mutation methods.
func (s *Service) Release(ctx context.Context, req ReleaseRequest) (ReleaseResponse, error) {
	// The old protocol allowed root/name to identify a route at the current
	// path. Resolve that identity before translating to a path selector so a
	// mismatched name remains a no-op rather than releasing whichever route now
	// owns the path.
	var explicitRouteName string
	if req.Kind != identity.KindPort && (req.Root != "" || req.Name != "") {
		resolved, err := identity.Resolve(ctx, identity.Options{
			WorkDir: req.WorkDir,
			Root:    req.Root,
			Name:    req.Name,
			Env:     requestEnv(nil),
			Kind:    identity.KindRoute,
		})
		if err != nil {
			return ReleaseResponse{}, err
		}
		explicitRouteName = resolved.NormalizedName
	}

	legacy := req
	legacy.Implicit = true
	legacy.Path = ""
	legacy.Host = ""
	legacy.Port = 0
	legacy.Recursive = false
	legacy.Scope = registry.ReleaseScopeRoute
	if req.All {
		legacy.Scope = registry.ReleaseScopeAll
	}
	if req.Kind == identity.KindPort {
		legacy.Scope = registry.ReleaseScopeName
		if legacy.Name == "" {
			legacy.Name = "port"
		}
	} else {
		legacy.Name = ""
	}
	planned, plan, err := s.PlanRelease(ctx, legacy)
	if err != nil {
		return ReleaseResponse{}, err
	}
	if explicitRouteName != "" {
		if len(plan.Items) != 1 || plan.Items[0].Kind != identity.KindRoute || plan.Items[0].Name != explicitRouteName {
			planned.Matched = 0
			planned.Released = 0
			planned.Forgotten = 0
			planned.Items = []ReleaseItem{}
			return planned, nil
		}
	}
	return s.ApplyRelease(ctx, plan, req.DryRun)
}

func canonicalReleaseSelector(req ReleaseRequest) (registry.ReleaseSelector, ReleaseSelector, error) {
	pathSelected := req.Path != "" || req.Implicit
	hostSelected := req.Host != ""
	portSelected := req.Port != 0
	selected := 0
	for _, present := range []bool{pathSelected, hostSelected, portSelected} {
		if present {
			selected++
		}
	}
	if selected != 1 {
		return registry.ReleaseSelector{}, ReleaseSelector{}, errors.New("release requires exactly one path, host, or port selector")
	}
	if req.Path != "" && req.Implicit {
		return registry.ReleaseSelector{}, ReleaseSelector{}, errors.New("release path and implicit current-directory selectors cannot be combined")
	}

	if pathSelected {
		var path string
		var err error
		if req.Implicit {
			path, err = absWorkDir(req.WorkDir)
		} else if filepath.IsAbs(req.Path) {
			path = filepath.Clean(req.Path)
		} else {
			if req.WorkDir == "" {
				return registry.ReleaseSelector{}, ReleaseSelector{}, errors.New("request has no working directory")
			}
			path, err = filepath.Abs(filepath.Join(req.WorkDir, req.Path))
		}
		if err != nil {
			return registry.ReleaseSelector{}, ReleaseSelector{}, fmt.Errorf("resolve release path: %w", err)
		}
		name := req.Name
		if req.Scope == registry.ReleaseScopeName {
			name, _, err = identity.NormalizeLabel(name)
			if err != nil {
				return registry.ReleaseSelector{}, ReleaseSelector{}, fmt.Errorf("invalid release name %q: %w", req.Name, err)
			}
		}
		selector := registry.ReleaseSelector{
			Type:      registry.ReleaseSelectorPath,
			Path:      path,
			Recursive: req.Recursive,
			Scope:     req.Scope,
			Name:      name,
		}
		return selector, publicReleaseSelector(selector, req.Implicit), nil
	}

	if hostSelected {
		if req.Recursive || req.Scope != "" || req.Name != "" {
			return registry.ReleaseSelector{}, ReleaseSelector{}, errors.New("release host selector cannot include recursive, scope, or name values")
		}
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Host)), ".")
		if host == "" {
			return registry.ReleaseSelector{}, ReleaseSelector{}, errors.New("release host selector requires a host")
		}
		selector := registry.ReleaseSelector{Type: registry.ReleaseSelectorHost, Host: host}
		return selector, publicReleaseSelector(selector, false), nil
	}

	if req.Recursive || req.Scope != "" || req.Name != "" {
		return registry.ReleaseSelector{}, ReleaseSelector{}, errors.New("release port selector cannot include recursive, scope, or name values")
	}
	selector := registry.ReleaseSelector{Type: registry.ReleaseSelectorPort, Port: req.Port}
	return selector, publicReleaseSelector(selector, false), nil
}

func publicReleaseSelector(selector registry.ReleaseSelector, implicit bool) ReleaseSelector {
	public := ReleaseSelector{Type: selector.Type}
	switch selector.Type {
	case registry.ReleaseSelectorPath:
		public.Path = stringPointer(selector.Path)
		public.Implicit = boolPointer(implicit)
		public.Recursive = boolPointer(selector.Recursive)
		public.Scope = releaseScopePointer(selector.Scope)
		if selector.Scope == registry.ReleaseScopeName {
			public.Name = stringPointer(selector.Name)
		}
	case registry.ReleaseSelectorHost:
		public.Host = stringPointer(selector.Host)
	case registry.ReleaseSelectorPort:
		public.Port = intPointer(selector.Port)
	}
	return public
}

func releaseResponse(plan registry.ReleasePlan, selector ReleaseSelector, dryRun bool) ReleaseResponse {
	items := make([]ReleaseItem, 0, len(plan.Items))
	response := ReleaseResponse{
		Operation: "release",
		DryRun:    dryRun,
		Selector:  selector,
		Matched:   len(plan.Items),
		Items:     items,
	}
	for _, planned := range plan.Items {
		item := publicReleaseItem(planned)
		response.Items = append(response.Items, item)
		for _, action := range item.Actions {
			switch action {
			case registry.ReleaseActionRelease:
				response.Released++
			case registry.ReleaseActionForget:
				response.Forgotten++
			}
		}
	}
	return response
}

func publicReleaseItem(planned registry.ReleasePlanItem) ReleaseItem {
	ports := append([]int{}, planned.Ports...)
	actions := append([]registry.ReleaseAction{}, planned.Actions...)
	item := ReleaseItem{
		Kind:    planned.Kind,
		Path:    planned.Path,
		State:   releaseItemState(planned),
		Ports:   ports,
		Actions: actions,
	}
	if planned.ActivePort != nil {
		port := *planned.ActivePort
		item.Port = &port
	}
	if planned.Kind == identity.KindRoute {
		item.Host = planned.Host
		item.Hosts = make([]ReleaseHost, 0, len(planned.Hosts))
		seen := make(map[string]struct{}, len(planned.Hosts))
		for _, host := range planned.Hosts {
			key := host.Host + "\x00" + host.Type
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			item.Hosts = append(item.Hosts, ReleaseHost{Host: host.Host, Type: host.Type})
		}
	} else {
		item.Name = planned.Name
	}
	return item
}

func releaseItemState(item registry.ReleasePlanItem) string {
	if item.RegistryState != registry.StateActive {
		return registry.StateReleased
	}
	if item.Path != "" {
		if _, err := os.Stat(item.Path); os.IsNotExist(err) {
			return "stale"
		}
	}
	if item.ActivePort == nil {
		return "down"
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(*item.ActivePort)))
	if err != nil {
		return "down"
	}
	_ = conn.Close()
	return "up"
}

func boolPointer(value bool) *bool                                           { return &value }
func stringPointer(value string) *string                                     { return &value }
func intPointer(value int) *int                                              { return &value }
func releaseScopePointer(value registry.ReleaseScope) *registry.ReleaseScope { return &value }

// describeReset compares the previously remembered identity for a directory
// against the freshly re-resolved one and, when --reset actually changes a
// remembered host/root/name, returns a single warning naming each cleared field
// as "old -> new". It returns changed=false when the reset is a no-op (nothing
// was overridden), so no spurious warning is printed.
func describeReset(remembered, resolved identity.Result) (string, bool) {
	var parts []string
	if remembered.Host != resolved.Host && remembered.Host != "" {
		parts = append(parts, fmt.Sprintf("host %s -> %s", remembered.Host, resolved.Host))
	}
	if remembered.Root != resolved.Root && remembered.Root != "" {
		parts = append(parts, fmt.Sprintf("root %s -> %s", remembered.Root, resolved.Root))
	}
	if remembered.Name != resolved.Name && remembered.Name != "" {
		parts = append(parts, fmt.Sprintf("name %s -> %s", remembered.Name, resolved.Name))
	}
	if len(parts) == 0 {
		return "", false
	}
	return "reset remembered identity: " + strings.Join(parts, ", "), true
}

// leaseState maps a freshly returned lease to the user-facing new/reused state,
// preferring conflict-renamed when the host was suffixed to avoid a collision.
func leaseState(lease registry.Lease, conflictRenamed bool) string {
	if conflictRenamed {
		return "conflict-renamed"
	}
	if lease.Created {
		return "new"
	}
	return "reused"
}

func (s *Service) List(ctx context.Context, all bool) ([]ListEntry, error) {
	records, err := s.store.List(ctx, all)
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(records))
	for _, record := range records {
		if !all && record.State != registry.StateActive {
			continue
		}
		entries = append(entries, routeRecordEntry(record))
	}
	return entries, nil
}

func (s *Service) Info(ctx context.Context, req InfoRequest) ([]ListEntry, error) {
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return nil, err
	}
	records, err := s.store.List(ctx, false)
	if err != nil {
		return nil, err
	}
	var entries []ListEntry
	for _, record := range records {
		if record.State != registry.StateActive || record.Path != abs {
			continue
		}
		entries = append(entries, routeRecordEntry(record))
	}
	return entries, nil
}

func (s *Service) Move(ctx context.Context, req MoveRequest) ([]ListEntry, error) {
	dest, err := absWorkDir(req.WorkDir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.From) == "" {
		return nil, fmt.Errorf("move requires a --from source path")
	}
	src, err := filepath.Abs(req.From)
	if err != nil {
		return nil, err
	}
	records, err := s.store.MovePath(ctx, src, dest, identity.KindRoute)
	if err != nil {
		if errors.Is(err, registry.ErrNoRouteForPath) {
			return nil, fmt.Errorf("no active Lewp route is registered for %s; nothing to move", src)
		}
		return nil, err
	}
	entries := make([]ListEntry, 0, len(records))
	for _, record := range records {
		entries = append(entries, routeRecordEntry(record))
	}
	return entries, nil
}

// absWorkDir absolutizes a request's working directory. An empty WorkDir is
// rejected rather than defaulted to os.Getwd(): the daemon runs under launchd
// with cwd `/`, so substituting its own directory would resolve requests against
// the wrong path (info/move/release against `/`). The client always forwards its
// directory, so an empty value here means a broken request.
func absWorkDir(workDir string) (string, error) {
	if workDir == "" {
		return "", errors.New("request has no working directory")
	}
	return filepath.Abs(workDir)
}

func (s *Service) Doctor(ctx context.Context) []string {
	checks := []string{
		"daemon: ok",
		"control socket: ok",
	}
	if _, err := s.store.List(ctx, true); err != nil {
		checks = append(checks, fmt.Sprintf("registry: unreadable (%v)", err))
	} else {
		checks = append(checks, "registry: readable")
	}
	https := "https: not configured (run lewp setup)"
	caPath, caPathErr := localtls.DefaultCAPath()
	caKeyPath, caKeyErr := localtls.DefaultCAKeyPath()
	if caPathErr == nil && caKeyErr == nil {
		if _, err := localtls.LoadCA(caPath, caKeyPath); err == nil {
			https = "https: configured (local CA present)"
		}
	}
	checks = append(checks, https)
	return checks
}

func response(resolved identity.Result, port int, state string) LeaseResponse {
	url := ""
	httpsURL := ""
	if resolved.Host != "" {
		url = "http://" + resolved.Host
		httpsURL = "https://" + resolved.Host
	}
	return LeaseResponse{
		Port:           port,
		URL:            url,
		HTTPSURL:       httpsURL,
		Host:           resolved.Host,
		Root:           resolved.Root,
		Name:           resolved.Name,
		NormalizedRoot: resolved.NormalizedRoot,
		NormalizedName: resolved.NormalizedName,
		Path:           resolved.Path,
		Kind:           resolved.Kind,
		HostKind:       resolved.HostKind,
		RootSource:     resolved.RootSource,
		NameSource:     resolved.NameSource,
		HostSource:     resolved.HostSource,
		Warnings:       resolved.Warnings,
		LeaseState:     state,
		ReleaseState:   registry.StateActive,
	}
}

func suffixedHost(host, path string, managed []string) string {
	parts := strings.Split(host, ".")
	hash := sha1.Sum([]byte(path))
	first := parts[0] + "-" + hex.EncodeToString(hash[:])[:4]
	normalizedHost, err := suffix.Normalize(host)
	if err == nil {
		for _, raw := range managed {
			managedSuffix, err := suffix.Normalize(raw)
			if err != nil || managedSuffix == suffix.BuiltIn {
				continue
			}
			if normalizedHost == managedSuffix {
				return first + "." + managedSuffix
			}
		}
	}
	parts[0] = first
	return strings.Join(parts, ".")
}

func recordState(record registry.Record) string {
	if record.Path != "" {
		if _, err := os.Stat(record.Path); os.IsNotExist(err) {
			return "stale"
		}
	}
	if record.State != registry.StateActive {
		return record.State
	}
	if record.Port == 0 {
		return "down"
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(record.Port)))
	if err != nil {
		return "down"
	}
	_ = conn.Close()
	return "up"
}

func DefaultRegistryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "lewp", "registry.sqlite"), nil
}

func DefaultSocketPath() (string, error) {
	registryPath, err := DefaultRegistryPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(registryPath), "control.sock"), nil
}
