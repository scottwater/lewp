# Custom Suffixes Design

## Purpose

Lewp keeps `.lewp` as the default local routing suffix while allowing explicit
opt-in routing for owned public-domain subtrees, such as
`local.todoordie.com`.

This supports OAuth providers that reject private TLDs like `.lewp` but accept
HTTPS callbacks on domains the user or organization owns.

## Non-Goals

- Do not make Lewp a general DNS forwarder.
- Do not infer or install suffixes from `lewp add --host`.
- Do not support arbitrary TLD hijacking, such as `.jp`.
- Do not add a risky override in the first version.
- Do not change the default inferred host format.

## User Model

`lewp setup` always installs and maintains `.lewp`.

Users add custom suffixes explicitly:

```sh
lewp setup --suffix local.todoordie.com
lewp setup --suffix local.todoordie.com --suffix local.other.com
```

Suffix setup is additive. Running setup with a new suffix does not remove old
suffixes.

Users inspect and remove custom suffixes with:

```sh
lewp suffix list
lewp suffix remove local.todoordie.com
```

`lewp system uninstall` removes all Lewp-managed resolver files, including
custom suffix resolver files.

## Host Behavior

Default inferred hosts remain:

```text
<name>.<root>.lewp
```

Custom suffixes are used only through explicit host selection:

- `lewp add --host feature-1.local.todoordie.com`
- `LEWP_HOST=feature-1.local.todoordie.com`
- `host = "feature-1.local.todoordie.com"` in `.lewp.local.toml`

`lewp add --host` accepts a host only when it is inside one of the configured
managed suffixes. `.lewp` is always configured.

Examples:

- `feature-1.local.todoordie.com` is valid after
  `lewp setup --suffix local.todoordie.com`.
- `feature-1.todoordie.com` is rejected because `todoordie.com` is not a valid
  managed suffix under this design.
- `feature-1.local.unconfigured.com` is rejected until that suffix is installed.

## Suffix Validation

Use `golang.org/x/net/publicsuffix`, already present through the existing
`golang.org/x/net` dependency.

Custom suffix validation:

- Normalize lowercase.
- Trim one trailing dot.
- Require valid DNS labels.
- Reject empty labels and labels longer than 63 bytes.
- Reject leftmost label `www`.
- Reject reserved or special suffixes:
  - `local`
  - `localhost`
  - `test`
  - `invalid`
- Reject suffixes equal to their registrable domain as reported by
  `publicsuffix.EffectiveTLDPlusOne`.
- Reject suffixes when `publicsuffix.EffectiveTLDPlusOne` cannot compute a
  registrable domain.
- Allow suffixes below a registrable domain.

Examples:

- `local.todoordie.com` is allowed because the registrable domain is
  `todoordie.com`.
- `todoordie.com` is rejected because it equals its registrable domain.
- `local.todoordie.co.uk` is allowed because the registrable domain is
  `todoordie.co.uk`.
- `todoordie.co.uk` is rejected because it equals its registrable domain.
- `www.todoordie.com` is rejected because the leftmost label is `www`.
- `jp` and `com` are rejected because they are public suffix / bare TLD shapes.

## DNS Behavior

Lewp installs one resolver file per managed suffix:

```text
/etc/resolver/lewp
/etc/resolver/local.todoordie.com
```

The resolver file for `local.todoordie.com` scopes system DNS interception to
that subtree. It does not affect `todoordie.com`, `www.todoordie.com`, or
unrelated `*.todoordie.com` names.

The DNS responder answers A and AAAA queries for configured managed suffixes
with loopback addresses.

The DNS responder answers both the custom suffix apex and subdomains:

- `local.todoordie.com`
- `feature-1.local.todoordie.com`

This is acceptable because proxy routing remains exact-host based. If the apex
resolves but has no registered route, the proxy returns the normal unknown-host
response.

## Proxy Behavior

The proxy continues to route by exact registered host.

No wildcard proxy routing is introduced. Registering
`feature-1.local.todoordie.com` does not register
`api.feature-1.local.todoordie.com`.

Unknown hosts under configured custom suffixes get the same developer debug
behavior as unknown `.lewp` hosts, rather than a generic non-Lewp 404.

## Storage

Store managed suffixes in a global Lewp-owned config file:

```text
~/Library/Application Support/lewp/suffixes.toml
```

The file is read by CLI commands and the daemon.

The built-in suffix `.lewp` is implicit and is not stored.

The stored custom suffix list is:

- normalized
- unique
- sorted for stable output

Removing a suffix deletes its resolver file and removes it from the global
config. It does not delete existing route identities from the registry. Those
routes become unusable until the suffix is re-added or the route is released.

## CLI Output

`lewp setup --suffix local.todoordie.com` prints all relevant resolver paths or
a concise summary that includes the added suffix.

`lewp suffix list` shows `.lewp` plus configured custom suffixes, with `.lewp`
marked as built-in.

`lewp suffix remove local.todoordie.com` reports the removed resolver file and
the suffix removed from config.

If removal is blocked by a non-Lewp-owned resolver file mismatch, fail with the
same style of inspect-and-retry guidance used by setup.

## Doctor

`lewp doctor` continues to check `.lewp`.

It also reports configured custom suffixes and whether their resolver files
point at Lewp's DNS port.

DNS lookup checks include one generated hostname under each configured suffix,
for example `doctor.local.todoordie.com`.

## Testing

Add focused tests for:

- suffix validation, including `co.uk` cases through `publicsuffix`
- setup adding multiple suffixes without removing old suffixes
- suffix list output
- suffix remove output and config/resolver deletion
- host validation accepting only `.lewp` and configured suffixes
- DNS responder matching configured suffixes
- proxy unknown-host debug page for configured custom suffixes
- system uninstall removing all Lewp-managed custom resolver files

## Open Risks

macOS resolver behavior for non-IANA/private suffixes may vary by release. This
feature reduces OAuth friction by supporting owned public-domain subtrees, but
it does not remove the need to verify resolver behavior during end-to-end
testing.

OAuth provider behavior still varies. Lewp can provide owned-domain local
routing, but users must still configure provider-side redirect URIs and domain
verification as required by each provider.
