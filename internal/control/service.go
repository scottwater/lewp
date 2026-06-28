package control

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
	localtls "github.com/scottwater/lewp/internal/tls"
)

type Service struct {
	store     *registry.Store
	portRange registry.PortRange
}

type LeaseRequest struct {
	WorkDir string
	Root    string
	Name    string
	Host    string
}

type PortRequest struct {
	WorkDir string
	Name    string
}

type ReleaseRequest struct {
	WorkDir string
	Root    string
	Name    string
	Forget  bool
}

type LeaseResponse struct {
	Port           int             `json:"port"`
	URL            string          `json:"url,omitempty"`
	Host           string          `json:"host,omitempty"`
	Root           string          `json:"root"`
	Name           string          `json:"name"`
	NormalizedRoot string          `json:"normalized_root"`
	NormalizedName string          `json:"normalized_name"`
	Path           string          `json:"path"`
	Kind           identity.Kind   `json:"kind"`
	RootSource     identity.Source `json:"root_source"`
	NameSource     identity.Source `json:"name_source"`
	HostSource     identity.Source `json:"host_source,omitempty"`
	Warnings       []string        `json:"warnings,omitempty"`
	ReleaseState   string          `json:"release_state"`
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
	return &Service{store: store, portRange: portRange}
}

func (s *Service) Lease(ctx context.Context, req LeaseRequest) (LeaseResponse, error) {
	resolved, err := identity.Resolve(identity.Options{
		WorkDir: req.WorkDir,
		Root:    req.Root,
		Name:    req.Name,
		Host:    req.Host,
		Kind:    identity.KindRoute,
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
	if owner, ok, err := s.store.FindByHost(ctx, resolved.Host); err != nil {
		return LeaseResponse{}, err
	} else if ok && owner.Path != resolved.Path {
		original := resolved.Host
		resolved.Host = suffixedHost(resolved.Host, resolved.Path)
		resolved.HostKind = identity.HostKindCustom
		resolved.Warnings = append(resolved.Warnings,
			fmt.Sprintf("warning: %s is already assigned to %s; using %s", original, owner.Path, resolved.Host))
	}
	lease, err := s.store.Lease(ctx, resolved, s.portRange)
	if err != nil {
		return LeaseResponse{}, err
	}
	return response(resolved, lease.Port), nil
}

func (s *Service) Port(ctx context.Context, req PortRequest) (LeaseResponse, error) {
	resolved, err := identity.Resolve(identity.Options{
		WorkDir: req.WorkDir,
		Name:    req.Name,
		Kind:    identity.KindPort,
	})
	if err != nil {
		return LeaseResponse{}, err
	}
	lease, err := s.store.Lease(ctx, resolved, s.portRange)
	if err != nil {
		return LeaseResponse{}, err
	}
	return response(resolved, lease.Port), nil
}

func (s *Service) Release(ctx context.Context, req ReleaseRequest) error {
	if req.Root == "" && req.Name == "" {
		workDir := req.WorkDir
		if workDir == "" {
			var err error
			workDir, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return err
		}
		return s.store.ReleasePath(ctx, abs, identity.KindRoute, req.Forget)
	}
	resolved, err := identity.Resolve(identity.Options{
		WorkDir: req.WorkDir,
		Root:    req.Root,
		Name:    req.Name,
		Kind:    identity.KindRoute,
	})
	if err != nil {
		return err
	}
	return s.store.Release(ctx, resolved.Path, identity.KindRoute, resolved.NormalizedName, req.Forget)
}

func (s *Service) List(ctx context.Context, all bool) ([]ListEntry, error) {
	records, err := s.store.List(ctx, all)
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(records))
	for _, record := range records {
		state := recordState(record)
		if !all && record.State != registry.StateActive {
			continue
		}
		entries = append(entries, ListEntry{
			Host:  record.Host,
			Port:  record.Port,
			State: state,
			Path:  record.Path,
			Kind:  record.Kind,
			Root:  record.Root,
			Name:  record.Name,
		})
	}
	return entries, nil
}

func (s *Service) Doctor() []string {
	https := "https: not configured (run lewp setup)"
	if _, err := localtls.LoadCA(localtls.DefaultCAPath(), localtls.DefaultCAKeyPath()); err == nil {
		https = "https: configured (local CA present)"
	}
	return []string{
		"daemon: ok",
		"control socket: ok",
		https,
	}
}

func response(resolved identity.Result, port int) LeaseResponse {
	url := ""
	if resolved.Host != "" {
		url = "http://" + resolved.Host
	}
	return LeaseResponse{
		Port:           port,
		URL:            url,
		Host:           resolved.Host,
		Root:           resolved.Root,
		Name:           resolved.Name,
		NormalizedRoot: resolved.NormalizedRoot,
		NormalizedName: resolved.NormalizedName,
		Path:           resolved.Path,
		Kind:           resolved.Kind,
		RootSource:     resolved.RootSource,
		NameSource:     resolved.NameSource,
		HostSource:     resolved.HostSource,
		Warnings:       resolved.Warnings,
		ReleaseState:   registry.StateActive,
	}
}

func suffixedHost(host, path string) string {
	parts := strings.Split(host, ".")
	hash := sha1.Sum([]byte(path))
	parts[0] = parts[0] + "-" + hex.EncodeToString(hash[:])[:4]
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
