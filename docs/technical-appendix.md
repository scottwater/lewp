# Domains Technical Appendix

## Reference Direction

The project should be a new Go binary, not a direct fork.

Useful reference behavior:

- `dot-test`: simple DNS server, reverse proxy, stable local names, debug error
  pages.
- `puma-dev`: good precedent for `.lewp`, HTTPS, and macOS setup, but app
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
- route hit summary
- proxy target down
- released
- forgotten
- normalized name changed

## HTTPS Direction

Implementation direction selected in the plan:

- Go-managed local CA using stdlib `crypto/x509`
- keychain trust during `lewp setup`
- SNI leaf certificates issued automatically for local `.lewp` hosts
- no per-project certificate work

Rejected for V1: `mkcert`, CertMagic/local CA machinery, and per-project TLS config.

## Doctor Checks

`lewp doctor` should inspect:

- daemon running
- control socket reachable
- DNS resolver file exists
- `.lewp` resolves to loopback
- proxy can bind or is bound on port 80
- HTTPS CA state when enabled
- registry readable
- current folder identity can be inferred
- current lease target port state
- conflicting hostnames

## Resolved And Open Implementation Questions

Resolved by the plan:

- proxy: raw `net/http/httputil`
- HTTPS: stdlib CA + macOS keychain trust
- `lewp init`: out of V1
- requested routed-app ports (`lewp add --port`): out of V1

Still open:

- exact SQLite schema migrations mechanism
- launchd plist installation details
