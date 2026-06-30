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
		"Free it:        cd %s && lewp release --forget\n"+
		"Use another:    lewp add --host <name>.lewp\n"+
		"Suffix anyway:  lewp add --host %s --auto-suffix",
		e.Host, e.OwnerPath, e.OwnerPath, e.Host)
}

type PortRequest struct {
	WorkDir string
	Name    string
	// Env carries client-process identity environment values; see LeaseRequest.
	Env map[string]string
}

type ReleaseRequest struct {
	WorkDir string
	Root    string
	Name    string
	Forget  bool
	// Env carries client-process identity environment values; see LeaseRequest.
	Env map[string]string
	// Kind selects what to release; empty means route. KindPort releases a bare
	// port lease (by Name, defaulting to "port") for the current directory.
	Kind identity.Kind
	// All releases the route and every bare port for the current directory.
	All bool
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

// ReleaseResponse reports how many active leases a release call freed, split by
// kind so the CLI can report no-op cases and counts.
type ReleaseResponse struct {
	Routes int `json:"routes"`
	Ports  int `json:"ports"`
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
// identity.Resolve falls back to the *daemon* process environment when handed a
// nil map; passing an empty (non-nil) map instead guarantees the daemon never
// honors its own LEWP_* variables — only values the client explicitly forwarded.
func requestEnv(env map[string]string) map[string]string {
	if env == nil {
		return map[string]string{}
	}
	return env
}

func (s *Service) Lease(ctx context.Context, req LeaseRequest) (LeaseResponse, error) {
	resolved, err := identity.Resolve(identity.Options{
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
	resolved, err := identity.Resolve(identity.Options{
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

func (s *Service) Release(ctx context.Context, req ReleaseRequest) (ReleaseResponse, error) {
	kind := req.Kind
	if kind == "" {
		kind = identity.KindRoute
	}
	// Bare-port release targets a single named port lease for this directory.
	if kind == identity.KindPort {
		name := req.Name
		if name == "" {
			name = "port"
		}
		resolved, err := identity.Resolve(identity.Options{
			WorkDir: req.WorkDir,
			Name:    name,
			Env:     requestEnv(req.Env),
			Kind:    identity.KindPort,
		})
		if err != nil {
			return ReleaseResponse{}, err
		}
		n, err := s.store.Release(ctx, resolved.Path, identity.KindPort, resolved.NormalizedName, req.Forget)
		return ReleaseResponse{Ports: n}, err
	}
	// Explicit root/name releases a single named route.
	if req.Root != "" || req.Name != "" {
		resolved, err := identity.Resolve(identity.Options{
			WorkDir: req.WorkDir,
			Root:    req.Root,
			Name:    req.Name,
			Env:     requestEnv(req.Env),
			Kind:    identity.KindRoute,
		})
		if err != nil {
			return ReleaseResponse{}, err
		}
		n, err := s.store.Release(ctx, resolved.Path, identity.KindRoute, resolved.NormalizedName, req.Forget)
		return ReleaseResponse{Routes: n}, err
	}
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return ReleaseResponse{}, err
	}
	routes, err := s.store.ReleasePath(ctx, abs, identity.KindRoute, req.Forget)
	if err != nil {
		return ReleaseResponse{}, err
	}
	if !req.All {
		return ReleaseResponse{Routes: routes}, nil
	}
	ports, err := s.store.ReleasePath(ctx, abs, identity.KindPort, req.Forget)
	if err != nil {
		return ReleaseResponse{}, err
	}
	return ReleaseResponse{Routes: routes, Ports: ports}, nil
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

func absWorkDir(workDir string) (string, error) {
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
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
	if _, err := localtls.LoadCA(localtls.DefaultCAPath(), localtls.DefaultCAKeyPath()); err == nil {
		https = "https: configured (local CA present)"
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

func DefaultRegistryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "registry.sqlite"
	}
	return filepath.Join(home, "Library", "Application Support", "lewp", "registry.sqlite")
}

func DefaultSocketPath() string {
	return filepath.Join(filepath.Dir(DefaultRegistryPath()), "control.sock")
}
