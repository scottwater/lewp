package control

import (
	"context"
	"fmt"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

type AliasRequest struct {
	WorkDir string
	Host    string
}

type AliasRemoveResponse struct {
	Removed int `json:"removed"`
}

const (
	KindAlias        identity.Kind     = "alias"
	HostKindAlias    identity.HostKind = "alias"
	HostKindWildcard identity.HostKind = "wildcard"
)

func (s *Service) AliasAdd(ctx context.Context, req AliasRequest) (LeaseResponse, error) {
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return LeaseResponse{}, err
	}
	host := identity.NormalizeHostPattern(req.Host)
	if err := identity.ValidateRouteHostPatternForSuffixes(req.Host, s.managedSuffixes); err != nil {
		return LeaseResponse{}, err
	}
	route, ok, err := s.store.ActiveRouteByPath(ctx, abs)
	if err != nil {
		return LeaseResponse{}, err
	}
	if !ok {
		return LeaseResponse{}, fmt.Errorf("no active route for this directory\nRun: lewp lease")
	}
	hostType := registry.HostTypeAlias
	if identity.IsWildcardRouteHost(host) {
		hostType = registry.HostTypeWildcard
	}
	existingWarnings, err := aliasWarnings(ctx, s, route.RouteID, host, hostType)
	if err != nil {
		return LeaseResponse{}, err
	}
	routeHost, created, err := s.store.AddRouteHost(ctx, route.RouteID, host, hostType, string(identity.SourceCLI))
	if err != nil {
		return LeaseResponse{}, err
	}
	resp := listEntryResponse(route)
	resp.Host = host
	resp.URL = "http://" + host
	resp.HTTPSURL = "https://" + host
	resp.Warnings = existingWarnings
	switch routeHost.HostType {
	case registry.HostTypePrimary:
		resp.HostKind = hostKindForRecord(route)
	case registry.HostTypeWildcard:
		resp.HostKind = HostKindWildcard
	default:
		resp.HostKind = HostKindAlias
	}
	if created {
		resp.LeaseState = "new"
	} else {
		resp.LeaseState = "reused"
	}
	return resp, nil
}

func (s *Service) AliasRemove(ctx context.Context, req AliasRequest) (AliasRemoveResponse, error) {
	abs, err := absWorkDir(req.WorkDir)
	if err != nil {
		return AliasRemoveResponse{}, err
	}
	if err := identity.ValidateRouteHostPatternForSuffixes(req.Host, s.managedSuffixes); err != nil {
		return AliasRemoveResponse{}, err
	}
	host := identity.NormalizeHostPattern(req.Host)
	route, ok, err := s.store.ActiveRouteByPath(ctx, abs)
	if err != nil {
		return AliasRemoveResponse{}, err
	}
	if !ok {
		return AliasRemoveResponse{}, fmt.Errorf("no active route for this directory\nRun: lewp lease")
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
		return []ListEntry{}, nil
	}
	hosts, err := s.store.RouteHosts(ctx, route.RouteID, true)
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(hosts))
	for _, host := range hosts {
		entry := routeRecordEntry(route)
		entry.Host = host.Host
		entry.Kind = KindAlias
		entries = append(entries, entry)
	}
	return entries, nil
}

func aliasWarnings(ctx context.Context, s *Service, routeID int64, host, hostType string) ([]string, error) {
	if hostType != registry.HostTypeAlias {
		return nil, nil
	}
	hosts, err := s.store.RouteHosts(ctx, routeID, true)
	if err != nil {
		return nil, err
	}
	var warnings []string
	for _, existing := range hosts {
		if existing.HostType == registry.HostTypeWildcard && identity.WildcardRouteHostMatches(existing.Host, host) {
			warnings = append(warnings, fmt.Sprintf("warning: %s is already covered by wildcard %s", host, existing.Host))
		}
	}
	return warnings, nil
}

func listEntryResponse(record registry.Record) LeaseResponse {
	hostKind := identity.HostKind("")
	if record.HostType == registry.HostTypePrimary {
		hostKind = hostKindForRecord(record)
	}
	return LeaseResponse{
		Port:           record.Port,
		URL:            "http://" + record.Host,
		HTTPSURL:       "https://" + record.Host,
		Host:           record.Host,
		Root:           record.Root,
		Name:           record.Name,
		NormalizedRoot: record.NormalizedRoot,
		NormalizedName: record.NormalizedName,
		Path:           record.Path,
		Kind:           record.Kind,
		HostKind:       hostKind,
		ReleaseState:   registry.StateActive,
	}
}

func routeRecordEntry(record registry.Record) ListEntry {
	kind := record.Kind
	if record.HostType != "" && record.HostType != registry.HostTypePrimary {
		kind = KindAlias
	}
	return ListEntry{
		Host:  record.Host,
		Port:  record.Port,
		State: recordState(record),
		Path:  record.Path,
		Kind:  kind,
		Root:  record.Root,
		Name:  record.Name,
	}
}

func hostKindForRecord(record registry.Record) identity.HostKind {
	switch record.Host {
	case record.NormalizedName + "." + record.NormalizedRoot + ".lewp":
		return identity.HostKindInstance
	case record.NormalizedRoot + ".lewp":
		return identity.HostKindApex
	default:
		return identity.HostKindCustom
	}
}
