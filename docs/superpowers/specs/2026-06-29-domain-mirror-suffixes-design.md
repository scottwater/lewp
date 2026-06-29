# Domain Mirror Suffixes Design

## Summary

Lewp's current custom suffix support is intentionally safe: custom suffixes must
sit below a registrable domain, such as `local.todoordie.com`, and route hosts
must include at least one label before the managed suffix.

That safe default stays. Add an explicit domain-mirror mode for users who want
Lewp to mirror a real production domain tree locally, including the registrable
apex and `www` hosts.

Primary example:

```sh
lewp setup --suffix localkickofflabs.com --allow-domain-mirror
lewp add --host localkickofflabs.com
lewp add --host app.localkickofflabs.com
lewp add --host leads.localkickofflabs.com
```

This lets a team standardize on realistic local URLs without adding special
development routing code to the application.

## Goals

- Keep safe custom suffix behavior as the default.
- Add an explicit opt-in for mirroring a real domain tree locally.
- Allow exact routes for a managed suffix apex, such as
  `localkickofflabs.com`.
- Allow `www.*` suffixes only when domain-mirror mode is explicitly requested.
- Keep proxy routing exact-host based. No wildcard route registration.
- Warn clearly that domain mirroring shadows public DNS for that suffix on the
  local Mac while the resolver file exists.

## Non-Goals

- Do not infer custom suffixes from `lewp add --host`.
- Do not route arbitrary public domains unless they were configured through
  setup.
- Do not add wildcard proxy routes.
- Do not preserve backward compatibility with the current pre-use
  `suffixes = [...]` config shape.
- Do not broaden reserved-name or public-suffix validation.

## CLI Contract

Safe subtree mode remains the default:

```sh
lewp setup --suffix local.todoordie.com
```

Domain mirror mode requires an explicit flag:

```sh
lewp setup --suffix localkickofflabs.com --allow-domain-mirror
```

The flag applies to each suffix in that setup invocation. If the command accepts
multiple suffixes, they are all stored with domain-mirror mode when the flag is
present.

Rejected without `--allow-domain-mirror`:

- `todoordie.com`
- `www.todoordie.com`

Accepted with `--allow-domain-mirror`:

- `todoordie.com`
- `www.todoordie.com`
- `localkickofflabs.com`

Rejected in both modes:

- public suffixes and bare TLDs, such as `com`
- reserved local names: `local`, `localhost`, `test`, `invalid`
- syntactically invalid DNS suffixes
- the built-in `lewp` suffix

When a domain-mirror suffix is newly added, setup prints a warning:

```text
# warning: domain mirror localkickofflabs.com shadows public DNS for this suffix and its subdomains on this Mac
```

## Storage

Replace the current suffix config shape with explicit records:

```toml
[[suffixes]]
name = "localkickofflabs.com"
mode = "domain-mirror"

[[suffixes]]
name = "local.todoordie.com"
mode = "safe-subtree"
```

Mode values:

- `safe-subtree`: current default validation. Must be below a registrable
  domain and must not start with `www`.
- `domain-mirror`: explicit mirror mode. May equal the registrable domain and
  may start with `www`.

Old config shape handling:

```toml
suffixes = ["local.todoordie.com"]
```

Fail loudly with a cleanup message instead of migrating silently:

```text
read suffix config: old suffix config format is no longer supported
Next: remove ~/Library/Application Support/lewp/suffixes.toml, then re-run: lewp setup --suffix ...
```

This repo is still pre-use, so simpler code and explicit failure are preferred
over compatibility code.

## Route Behavior

Lewp should allow exact-host routes for any configured managed suffix, including
the suffix apex:

```sh
lewp add --host local.todoordie.com
lewp add --host localkickofflabs.com
```

This applies to safe-subtree and domain-mirror suffixes. DNS already answers
apex and subdomain queries under managed suffixes; host validation should match
that contract.

Proxy routing remains exact-host only:

- Registering `localkickofflabs.com` does not register
  `app.localkickofflabs.com`.
- Registering `app.localkickofflabs.com` does not register
  `leads.localkickofflabs.com`.
- Unknown hosts under a managed suffix still return Lewp's unknown-route debug
  page.

## DNS Behavior

Resolver file behavior stays one file per managed suffix:

```text
/etc/resolver/lewp
/etc/resolver/local.todoordie.com
/etc/resolver/localkickofflabs.com
```

For a domain-mirror suffix, macOS sends queries for the suffix and its
subdomains to Lewp's DNS responder. Lewp answers A and AAAA queries with
loopback addresses, same as current custom suffix behavior.

This means `localkickofflabs.com`, `app.localkickofflabs.com`, and
`leads.localkickofflabs.com` can all resolve locally while the resolver file is
installed.

## Commands And Output

`lewp suffix list` shows mode:

```text
lewp	built-in
local.todoordie.com	safe-subtree
localkickofflabs.com	domain-mirror
```

`lewp suffix remove <suffix>` remains unchanged from the user perspective. It
removes the suffix record and the matching resolver file after confirming the
resolver file is Lewp-owned.

`lewp doctor` should include mode in custom suffix resolver checks where it is
useful, but the pass/fail logic remains the same: suffix config readable,
resolver file exists, resolver points at Lewp DNS, daemon DNS answers.

## Implementation Notes

Update `internal/suffix` from string-only config to typed records:

```go
type Mode string

const (
	ModeSafeSubtree  Mode = "safe-subtree"
	ModeDomainMirror Mode = "domain-mirror"
)

type Entry struct {
	Name string `toml:"name"`
	Mode Mode   `toml:"mode"`
}

type Config struct {
	Suffixes []Entry `toml:"suffixes"`
}
```

Validation should be mode-aware:

- Normalize DNS names before validation and storage.
- `safe-subtree` uses the current `ValidateCustom` behavior.
- `domain-mirror` reuses label, reserved-name, public-suffix, and built-in
  checks, but allows registrable apexes and `www.*`.
- `Managed` should return normalized names for DNS/proxy/identity consumers.
- Provide helpers for adding/removing/listing entries without leaking TOML
  structure to CLI code.

Update host validation:

- A host must be inside a configured Lewp suffix.
- A host may equal a configured Lewp suffix.
- Invalid labels are still rejected.

The built-in `lewp` suffix remains implicit and built-in. Do not allow `lewp`
to be configured as a custom suffix.

## Tests

Add or update tests for:

- safe-subtree accepts `local.todoordie.com`
- safe-subtree rejects `todoordie.com`
- safe-subtree rejects `www.todoordie.com`
- domain-mirror accepts `todoordie.com`
- domain-mirror accepts `www.todoordie.com`
- both modes reject `com`, `local`, `localhost`, `test`, `invalid`, `lewp`,
  and invalid DNS labels
- suffix config writes explicit `[[suffixes]]` records with `name` and `mode`
- old `suffixes = [...]` config fails with a clear cleanup message
- setup writes resolver files for both modes
- setup prints the domain-mirror DNS shadowing warning when adding a mirror
  suffix
- `lewp suffix list` includes mode
- host validation accepts exact managed suffix hosts
- DNS answers apex and subdomain queries under managed suffixes
- proxy continues exact-host routing and unknown-host debug behavior
- doctor reports configured suffix resolver checks with enough mode context

## Documentation

Update `README.md` and `DOCUMENTATION.md`:

- Explain safe subtree mode as the default.
- Add `--allow-domain-mirror`.
- Show the KickoffLabs-style domain mirror example.
- State that domain mirroring shadows public DNS for the suffix and all
  subdomains on the local Mac until the resolver file is removed.
- State that proxy routing remains exact-host based, so each hostname still
  needs its own route.

## Open Decisions

None. Design choices approved:

- flag name: `--allow-domain-mirror`
- `www.*` allowed only with the override
- exact suffix apex routes allowed
- no config backward compatibility required
