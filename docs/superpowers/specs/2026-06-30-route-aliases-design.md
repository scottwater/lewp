# Route Aliases Design

## Summary

Lewp should support multiple hostnames for one local app process. A route has
one leased port, one primary host, and zero or more alias hosts. Exact aliases
and one-label wildcard aliases all proxy to the same loopback port while
preserving the original request Host header.

Primary workflow:

```sh
cd ~/work/app
lewp add
lewp alias add tags.app.lewp
lewp alias add leads.app.lewp
lewp alias add '*.app.lewp'
```

This supports Rails apps that route behavior by subdomain, and projects where
multiple local URLs point to the same process after codebases are merged.

## Goals

- Add another host to the current directory's active route without allocating a
  new port.
- Keep the CLI anchored to routes, not explicit port numbers.
- Keep one active routed app per directory; multiple hosts for that app are
  route hosts, not additional routes.
- Support exact host aliases and one-label wildcard aliases.
- Preserve the request Host header upstream so app-level subdomain routing
  still works.
- Reject alias conflicts before they can create ambiguous routing.
- Keep custom suffix and domain-mirror safety rules from the current design.
- Keep the design clean; no backward compatibility with existing local
  registry data is required.

## Non-Goals

- Do not start or manage app processes.
- Do not proxy non-loopback targets.
- Do not add multi-service routing such as `vite.app.lewp` pointing to a second
  port.
- Do not let a wildcard match more than one label.
- Do not add a new public-domain confirmation prompt for aliases; managed
  suffix setup and domain-mirror mode remain the risk gate.
- Do not preserve the current registry schema or local data.

## CLI Contract

Add an `alias` command group:

```sh
lewp alias add <host>
lewp alias remove <host>
lewp alias list [--json]
```

`lewp alias add <host>` attaches `<host>` to the current directory's active
route. Lewp supports one active route per directory, so alias targeting is
unambiguous. Alias add requires a route created by `lewp add`:

```text
no active route for this directory
Run: lewp add
```

Examples:

```sh
lewp add
lewp alias add tags.app.lewp
lewp alias add some-url-1.lewp
lewp alias add some-url-2.lewp
lewp alias add '*.app.lewp'
```

Host validation matches `lewp add --host`:

- `.lewp` hosts are always allowed, except the bare `lewp` suffix itself.
- Configured safe-subtree suffix hosts are allowed after `lewp setup --suffix`.
- Domain-mirror suffix hosts are allowed after
  `lewp setup --suffix <domain> --allow-domain-mirror`.
- Hosts outside configured managed suffixes are rejected.

`lewp alias add` output:

```text
HOST=tags.app.lewp
URL=http://tags.app.lewp
HTTPS_URL=https://tags.app.lewp
STATE=new
HOST_KIND=alias
```

Duplicate same-route aliases are idempotent and print `STATE=reused`.
Redundant exact aliases already covered by the same route's wildcard are
accepted and print a warning on stderr.

`lewp alias remove <host>` removes one alias from the current route. It does
not remove the primary host. Removing a missing alias is idempotent and reports
that no alias was removed.

`lewp alias list` shows aliases for the current route. `--json` emits a stable
array shape.

`lewp add --shell` remains unchanged and emits only the primary host. Alias
commands do not affect shell startup flows.

## Data Model

Use a fresh schema. Existing local databases may be deleted or rebuilt.

```text
routes
- id
- root
- name
- normalized_root
- normalized_name
- path
- created_at
- updated_at

route_hosts
- id
- route_id
- host
- host_type: primary | alias | wildcard
- source: cli | config
- created_at
- updated_at

leases
- id
- route_id
- port
- state: active | released
- last_seen_at
- released_at
- created_at
- updated_at

ports
- id
- path
- name
- normalized_name
- port
- state: active | released
- released_at
- created_at
- updated_at
```

Routed apps and bare ports should no longer share a generic identity table.
Routes own route hosts and leases. Bare ports remain separate named port leases
for a path.

Indexes:

- one active route per path
- one active lease per route
- one active bare port per path and normalized port name
- unique active exact host ownership
- unique active wildcard ownership

The primary host is a `route_hosts` row with `host_type=primary`. Aliases are
rows with `host_type=alias` or `host_type=wildcard`.

## Route Lookup

Proxy lookup order:

1. exact active host match in `route_hosts`
2. one-label wildcard match in `route_hosts`

For a request to `tags.app.lewp`, `*.app.lewp` matches only when the request
host has exactly one label before `app.lewp`.

Matches:

```text
tags.app.lewp
leads.app.lewp
```

Does not match:

```text
app.lewp
foo.tags.app.lewp
```

The proxy target is the matched route's active lease port. The upstream request
Host remains the original request host, not the stored wildcard pattern.

TLS does not need wildcard certificate issuance for this feature. The browser
requests the concrete SNI host such as `tags.app.lewp`, and Lewp can mint the
normal per-host leaf certificate.

## Conflict Rules

Alias registration must fail before storing any ambiguous host.

Reject an exact alias when:

- the exact host belongs to another active route
- the exact host is covered by another active route's wildcard

Reject a wildcard alias when:

- it overlaps another active wildcard owned by another route
- it would cover any exact host owned by another active route
- it would cover another route's primary host or alias

Allow when:

- the exact host already exists on the same route
- a wildcard covers exact hosts on the same route
- an exact alias is under a same-route wildcard

Example errors:

```text
host tags.app.lewp conflicts with route owned by /Users/scott/work/other
Release it: cd /Users/scott/work/other && lewp release --forget
```

```text
wildcard *.app.lewp would cover tags.app.lewp owned by /Users/scott/work/other
```

Conflict checks compare active routes only. Released routes do not reserve host
names unless they are reused by the same route through normal `lewp add`
behavior.

## Command Behavior

`lewp add` creates or reuses the current directory's single route:

- one route
- one primary route host
- one active route lease

If flags or config change root, name, or primary host for a directory that
already has an active route, `lewp add` updates that route rather than creating
a second route for the same path. Normal host conflict rules still apply.

`lewp release` releases the current route lease and all active route hosts for
that route. With `--forget`, it deletes the route, hosts, and route lease
history. Bare ports are still released only by `lewp port release` or
`lewp release --all`.

`lewp move --from <path>` moves the route and all route hosts together while
preserving the port.

`lewp list` should make aliases visible. Default human output can use one row
per host, with the same port repeated:

```text
HOST             NAME  KIND   PORT   STATE  PATH
app.lewp         app   route  42137  up     /Users/scott/work/app
tags.app.lewp    app   alias  42137  up     /Users/scott/work/app
*.app.lewp       app   alias  42137  up     /Users/scott/work/app
```

`lewp info` should group route data for the current directory:

```text
ROUTE
PORT=42137
HOST=app.lewp
URL=http://app.lewp
HTTPS_URL=https://app.lewp

ALIASES
HOST=tags.app.lewp
URL=http://tags.app.lewp
HTTPS_URL=https://tags.app.lewp
HOST=*.app.lewp
URL=http://*.app.lewp
HTTPS_URL=https://*.app.lewp
```

For wildcard aliases, `HTTPS_URL=https://*.app.lewp` is informational only. A
browser should visit concrete hosts such as `https://tags.app.lewp`.

## Config

Config-driven aliases are optional for the first implementation. The CLI design
does not require them.

If added later, `.lewp.local.toml` can support:

```toml
root = "work"
name = "app"
host = "app.lewp"
aliases = ["tags.app.lewp", "leads.app.lewp", "*.app.lewp"]
```

`lewp add` would then sync configured aliases after creating or reusing the
primary route. This should use the same validation and conflict rules as
`lewp alias add`.

## Error Pages And Doctor

Unknown managed hosts still receive the Lewp unknown-route page.

For wildcard-only routes, a concrete request host such as `tags.app.lewp`
should show normal down-target diagnostics if the app process is not listening.
The debug page should display:

- requested host
- matched host pattern when the match came from a wildcard
- loopback target
- project path
- root/name
- last seen
- release state
- suggested start command

`lewp doctor` should include alias conflicts when it can infer or inspect the
current directory's active route. It should not probe HTTP endpoints.

## Tests

Add focused coverage for:

- `lewp alias add` requires an active route
- exact alias reuses the current route port
- exact alias conflict with another route
- exact alias conflict with another route's wildcard
- wildcard matches exactly one label
- wildcard does not match the apex
- wildcard does not match nested labels
- wildcard rejects overlap with another route's exact host
- wildcard rejects overlap with another wildcard
- same-route duplicate alias is idempotent
- same-route exact alias under wildcard is allowed with a warning
- proxy preserves original Host for exact aliases
- proxy preserves original Host for wildcard matches
- route release includes aliases
- route move includes aliases
- list and info expose aliases clearly
- bare port behavior remains independent

## Documentation

Update `README.md` and `DOCUMENTATION.md` with:

- route alias use cases
- `lewp alias add/remove/list`
- wildcard one-label behavior
- conflict examples
- domain-mirror reminder for public-domain aliases
- note that one app process can serve multiple route hosts through the same
  port

## Open Decisions

No open product decisions remain for the first implementation.
