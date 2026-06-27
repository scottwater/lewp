# Domains Technical Appendix

## Reference Direction

The project should be a new Go binary, not a direct fork.

Useful reference behavior:

- `dot-test`: simple DNS server, reverse proxy, stable local names, debug error
  pages.
- `puma-dev`: good precedent for `.test`, HTTPS, and macOS setup, but app
  auto-booting is out of scope because it makes failures hard to debug.
- `outport`: useful ideas around deterministic ports and instances, but V1
  should avoid its broader service orchestration, dashboard, sharing, and env
  generation scope.
- `caddy`: strong TLS/proxy model, but raw Caddy configuration management is
  not the desired user experience.

## Registry Sketch

Suggested logical tables:

```text
identities
- id
- root
- name
- normalized_root
- normalized_name
- host
- path
- created_at
- updated_at

leases
- id
- identity_id
- port
- state
- last_seen_at
- expires_at
- released_at
- created_at
- updated_at

events
- id
- identity_id
- event_type
- message
- metadata_json
- created_at
```

Events should capture useful debugging breadcrumbs:

- lease created
- conflict renamed
- route hit
- proxy target down
- released
- forgotten
- normalized name changed

## HTTPS Investigation Paths

Possible implementation directions:

- Go-managed local CA similar to puma-dev or mkcert behavior
- `mkcert` integration if dependency tradeoff is acceptable
- CertMagic/local CA if it meaningfully reduces custom TLS code

The selected implementation should optimize for reliable macOS setup, clear
uninstall, and no per-project certificate work.

## Doctor Checks

`domains doctor` should inspect:

- daemon running
- control socket reachable
- DNS resolver file exists
- `.test` resolves to loopback
- proxy can bind or is bound on port 80
- HTTPS CA state when enabled
- registry readable
- current folder identity can be inferred
- current lease target port state
- conflicting hostnames

## Open Implementation Questions

These are implementation decisions, not unresolved product requirements:

- exact Go router/proxy libraries
- whether to use raw `net/http/httputil`, Caddy internals, or another proxy
  package
- exact SQLite schema migrations mechanism
- launchd plist installation details
- HTTPS implementation path
- whether `domains init` belongs in V1 or V1.1
- whether requested-port flags belong in V1

