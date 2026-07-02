# Custom-suffix TLS with a name-constrained local CA

Date: 2026-07-01
Status: awaiting user approval

## Problem

Two issues, one mechanism:

1. **Security.** The Lewp CA is trusted in the login keychain but carries no
   X.509 name constraints. The `.lewp`-only rule lives solely in
   `Manager.GetCertificate` — application code. Any process running as the
   user can read `ca-key.pem` and sign a browser-trusted certificate for any
   domain. Name constraints move the rule into the certificate, where the
   browser enforces it regardless of who holds the key.
2. **Feature.** Custom suffixes are HTTP-only in V1, but OAuth providers
   often require HTTPS on a real TLD (e.g. `https://app.local.mycompany.com`).
   Safe-subtree suffixes should get browser-trusted TLS.

## Decision

One CA whose critical name constraints are derived from the suffix config:
`lewp` plus every configured **safe-subtree** suffix. Domain-mirror suffixes
are never constrained-in, never minted for — minting real-domain apex certs
is the one thing Lewp must remain cryptographically unable to do. When the
constraint set drifts from the config, `lewp setup` rotates the CA
automatically (announce → untrust old → regenerate → re-trust; one keychain
prompt).

Alternatives rejected:

- **One CA per suffix** — no rotation churn, but N keychain entries/prompts
  and per-SNI signer selection; too many moving parts for a rare event.
- **Constraints only (custom stays HTTP-only)** — closes the stolen-key hole
  but not the OAuth use case.

## Design

### 1. CA generation (`internal/tls/tls.go`)

`NewCA(commonName string, permittedDNSDomains []string)` adds to the
template:

- `PermittedDNSDomains: permittedDNSDomains`, `PermittedDNSDomainsCritical: true`
  — entries are bare suffixes (`lewp`, `local.mycompany.com`); an entry
  permits the name itself and all subdomains.
- `ExcludedIPRanges`: `0.0.0.0/0` and `::/0` — no IP-SAN certificates.
- `MaxPathLenZero: true` — no sub-CAs.

`EnsureCA` gains the same parameter and keeps its refuse-to-overwrite
semantics (create only from a clean slate). A helper
`ConstraintsMatch(ca *CA, desired []string) bool` compares the CA's
permitted-DNS set (sorted) with the desired set and requires the exclusions
to be present; used by setup and doctor.

### 2. Minting policy (`Manager`)

`NewManager(ca *CA, allowedSuffixes []string)`. `GetCertificate` replaces the
hardcoded `.lewp` check with:

1. Host must fall inside `allowedSuffixes` (via `suffix.HostInManagedSuffix`).
   The daemon passes `lewp` + safe-subtree suffix names — never mirrors, so
   mirrors and unmanaged names are refused exactly as today.
2. If the CA has name constraints and they do not cover the host, refuse with
   an error directing to `lewp setup` — a stale CA yields a clear handshake
   failure, not a cert the browser rejects with a scarier error.
3. A legacy unconstrained CA still mints (feature keeps working during
   migration); doctor flags the posture problem instead.

### 3. Plumbing

- `runDaemon` (internal/cli/cli.go) already loads the suffix config; it also
  computes `tlsSuffixes` = `lewp` + entries with `Mode == ModeSafeSubtree`
  and passes `daemon.Config.TLSSuffixes`.
- `daemon.Serve` builds `NewManager(ca, cfg.TLSSuffixes)`. Its `EnsureCA`
  fallback passes the same constraint list but only ever **creates** a
  missing CA; rotation is setup-only because only setup owns keychain
  prompts.

### 4. Setup auto-rotation (`runSetup`)

After merging `--suffix` flags into the config, compute the desired
constraint set. At the existing EnsureCA step:

- CA missing → create with constraints (current behavior + constraints).
- CA present and `ConstraintsMatch` → keep.
- Mismatch (including today's unconstrained CA) → rotate:
  1. Print what changed and why a keychain prompt is coming.
  2. Run `UntrustCommand` (`security delete-certificate -c`, already in
     tls.go) via `cfg.RunCommand`; a failure is a warning, not fatal (the
     cert may already be absent).
  3. Remove `ca.pem` and `ca-key.pem`, regenerate via `EnsureCA` with the
     new constraints.
  4. The existing trust step later in setup re-adds the new cert — net one
     keychain prompt.
- Output: `CA=<path> (rotated: constraints now <list>)` plus the existing
  restart reminder — the daemon re-mints leaves after restart.

`lewp suffix remove` stays config-only but prints `Next: lewp setup` so the
CA gets narrowed on the next setup run.

### 5. Doctor (`internal/cli/doctor.go`)

Extend the `local CA` check:

- **fail**: CA loads but has no name constraints (legacy) — "CA has no name
  constraints; re-run `lewp setup` to rotate it".
- **warn**: constraints don't match the current suffix config — missing
  suffix means HTTPS for it won't mint; extra suffix means the CA is broader
  than the config. Both say "re-run `lewp setup`".

### 6. Docs

- README `HTTPS` section: safe-subtree suffixes now get trusted HTTPS;
  domain mirrors remain HTTP-only.
- DOCUMENTATION.md: replace "certificate issuance is limited to `.lewp`
  hosts" with the new rule; note the rotation behavior of `setup` and the
  doctor checks.
- docs/plan.md: record the name-constraint decision.

### 7. Testing

- Fresh CA carries critical `PermittedDNSDomains`, IP exclusions, and
  `MaxPathLenZero` (regression test for the security finding).
- End-to-end `x509.Verify`: constrained root validates an in-scope leaf and
  **rejects** an out-of-scope leaf signed with the same key (the stolen-key
  forgery, simulated).
- Manager: safe-subtree host minted; domain-mirror and unmanaged hosts
  refused; stale-CA (constraints missing a configured suffix) refused with
  the setup-pointing error; legacy unconstrained CA still mints.
- Setup rotation: with the injectable `RunCommand`, assert the
  untrust → regenerate → trust sequence fires on mismatch and does not fire
  on match; assert CA files are replaced and constraints updated.
- Doctor: legacy-CA fail, mismatch warn, healthy pass.
- Manual (post-implementation): in Safari and Chrome, confirm an in-scope
  host gets the padlock and a forged out-of-scope cert is rejected.

## Out of scope

- TLS for domain-mirror suffixes (stays HTTP-only, deliberately).
- Firefox trust (NSS store) — unchanged V1 limitation.
- Migrating existing minted leaves — the daemon's in-memory cache clears on
  the restart setup already requires for suffix changes.

## Open items decided by default (user was away)

- **Auto-rotate in setup** chosen over an explicit `--rotate-ca` flag or
  manual instructions. Revisit if unwanted.
