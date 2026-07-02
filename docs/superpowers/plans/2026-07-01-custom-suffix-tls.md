# Custom-Suffix TLS with Name-Constrained CA Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mint browser-trusted certificates for safe-subtree custom suffixes while adding critical X.509 name constraints to the local CA, so a stolen CA key cannot sign certificates outside the configured suffixes.

**Architecture:** One CA whose `PermittedDNSDomains` (critical) is derived from the suffix config: `lewp` + every safe-subtree suffix; domain mirrors are never included. The TLS manager's minting allowlist mirrors the same list. `lewp setup` auto-rotates the CA (untrust → regenerate → re-trust) when constraints drift from config; doctor fails on an unconstrained CA and warns on drift.

**Tech Stack:** Go stdlib (`crypto/x509`, `crypto/tls`), existing internal packages (`internal/tls`, `internal/suffix`, `internal/daemon`, `internal/cli`).

**Spec:** `docs/superpowers/specs/2026-07-01-custom-suffix-tls-design.md`

## Global Constraints

- Never mint or permit certificates for domain-mirror suffixes — the CA must remain cryptographically unable to sign a real registrable apex domain.
- All binds stay loopback-only; no behavior change to routing/DNS in this plan.
- CA common name stays exactly `Lewp Local Development CA`.
- Only `lewp setup` touches the keychain; the daemon may create a missing CA but never rotates one.
- Run `gofmt` on every touched file; `go vet ./...` and `go test ./...` must pass at every commit.
- Follow the repo's existing test style: stdlib `testing` only, no new dependencies.

---

### Task 1: Name-constrained CA generation

**Files:**
- Modify: `internal/tls/tls.go` (NewCA ~line 37, EnsureCA ~line 66, add const + helpers)
- Modify: `internal/daemon/daemon.go:53-56` (call sites)
- Modify: `internal/cli/cli.go:360` (call site)
- Test: `internal/tls/tls_test.go`

**Interfaces:**
- Produces: `tls.CACommonName` (const, `"Lewp Local Development CA"`), `tls.NewCA(commonName string, permittedDNSDomains []string) (*CA, error)`, `tls.EnsureCA(certPath, keyPath, commonName string, permittedDNSDomains []string) (*CA, error)`, `(c *CA) HasNameConstraints() bool`, `tls.ConstraintsMatch(ca *CA, desired []string) bool`. `NewCA`/`EnsureCA` with a nil/empty domain list create an **unconstrained** CA (production callers always pass a non-empty list; tests use nil to simulate a legacy CA).

- [ ] **Step 1: Write the failing tests**

Append to `internal/tls/tls_test.go` (also add `"net"`, `"sort"`, `"time"` to imports if missing):

```go
// TestNewCASetsNameConstraints is the regression test for the 2026-07 security
// review finding: the trusted root must be cryptographically limited to its
// configured suffixes so a stolen CA key cannot sign arbitrary domains.
func TestNewCASetsNameConstraints(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	cert := ca.Certificate
	want := []string{"lewp", "local.todoordie.com"}
	got := append([]string(nil), cert.PermittedDNSDomains...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PermittedDNSDomains=%v want %v", got, want)
	}
	if !cert.PermittedDNSDomainsCritical {
		t.Fatal("name constraints are not critical")
	}
	if len(cert.ExcludedIPRanges) != 2 {
		t.Fatalf("ExcludedIPRanges=%v, want all of IPv4+IPv6 excluded", cert.ExcludedIPRanges)
	}
	if !cert.MaxPathLenZero {
		t.Fatal("CA can mint sub-CAs (MaxPathLenZero unset)")
	}
	if !ca.HasNameConstraints() {
		t.Fatal("HasNameConstraints=false for constrained CA")
	}
}

func TestNewCANilDomainsIsUnconstrained(t *testing.T) {
	ca, err := NewCA(CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ca.HasNameConstraints() {
		t.Fatal("nil domain list produced constraints")
	}
}

// TestConstrainedCARejectsOutOfScopeLeaf simulates the stolen-key forgery:
// a leaf for a public domain signed with the CA key must fail chain
// verification against the constrained root, while an in-scope leaf passes.
func TestConstrainedCARejectsOutOfScopeLeaf(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Certificate)
	verify := func(host string) error {
		leaf, err := ca.Leaf(host)
		if err != nil {
			return err
		}
		cert, err := x509.ParseCertificate(leaf.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: time.Now()})
		return err
	}
	for _, host := range []string{"feature-1.audit.lewp", "app.local.todoordie.com"} {
		if err := verify(host); err != nil {
			t.Fatalf("in-scope host %q failed verification: %v", host, err)
		}
	}
	for _, host := range []string{"github.com", "login.example.com"} {
		if err := verify(host); err == nil {
			t.Fatalf("out-of-scope forged leaf for %q verified against constrained root", host)
		}
	}
}

func TestConstraintsMatch(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !ConstraintsMatch(ca, []string{"local.todoordie.com", "lewp"}) {
		t.Fatal("order-insensitive match failed")
	}
	if ConstraintsMatch(ca, []string{"lewp"}) {
		t.Fatal("matched despite extra constraint")
	}
	if ConstraintsMatch(ca, []string{"lewp", "local.todoordie.com", "local.other.com"}) {
		t.Fatal("matched despite missing constraint")
	}
	legacy, err := NewCA(CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ConstraintsMatch(legacy, []string{"lewp"}) {
		t.Fatal("legacy unconstrained CA reported as matching")
	}
}
```

Update every existing `NewCA(...)`/`EnsureCA(...)` call in `internal/tls/tls_test.go` to the new signatures by adding `[]string{"lewp"}` as the domain-list argument (e.g. `NewCA("Lewp Local Development CA", []string{"lewp"})`, `EnsureCA(certPath, keyPath, "Lewp Local Development CA", []string{"lewp"})`). Add `"reflect"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tls/ -run 'TestNewCA|TestConstrainedCA|TestConstraintsMatch' -v`
Expected: compile FAIL — "too many arguments in call to NewCA" / "undefined: CACommonName".

- [ ] **Step 3: Implement**

In `internal/tls/tls.go`, add `"net"` and `"sort"` to imports, add the const, and change `NewCA`:

```go
// CACommonName is the subject and keychain identity of the Lewp local CA.
const CACommonName = "Lewp Local Development CA"

// NewCA mints a self-signed root. permittedDNSDomains become critical X.509
// name constraints: browsers refuse any leaf outside them no matter who holds
// the CA key, so a stolen key cannot forge certificates for public domains.
// An empty list creates an unconstrained CA and is reserved for tests that
// simulate pre-constraint CAs.
func NewCA(commonName string, permittedDNSDomains []string) (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	sn, err := serial(rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if len(permittedDNSDomains) > 0 {
		tmpl.PermittedDNSDomains = append([]string(nil), permittedDNSDomains...)
		tmpl.PermittedDNSDomainsCritical = true
		// No IP-SAN leaves ever: exclude the entire IPv4 and IPv6 space.
		tmpl.ExcludedIPRanges = []*net.IPNet{
			{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		}
		// No sub-CAs: constraints cannot be laundered through an intermediate.
		tmpl.MaxPathLenZero = true
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Certificate: cert, Key: key, CertDER: der}, nil
}

// HasNameConstraints reports whether the CA certificate carries DNS name
// constraints. False means a legacy (pre-constraint) CA that can sign any
// domain; doctor treats that as a failure.
func (c *CA) HasNameConstraints() bool {
	return len(c.Certificate.PermittedDNSDomains) > 0
}

// ConstraintsMatch reports whether the CA's permitted-DNS set is exactly the
// desired suffix set (order-insensitive) with the constraint marked critical.
// setup uses it to decide rotation; doctor uses it to flag drift.
func ConstraintsMatch(ca *CA, desired []string) bool {
	cert := ca.Certificate
	if !cert.PermittedDNSDomainsCritical || len(cert.ExcludedIPRanges) == 0 {
		return false
	}
	if len(cert.PermittedDNSDomains) != len(desired) {
		return false
	}
	got := append([]string(nil), cert.PermittedDNSDomains...)
	want := append([]string(nil), desired...)
	sort.Strings(got)
	sort.Strings(want)
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
```

Change `EnsureCA`'s signature and its one `NewCA` call:

```go
func EnsureCA(certPath, keyPath, commonName string, permittedDNSDomains []string) (*CA, error) {
```

and inside it: `ca, err = NewCA(commonName, permittedDNSDomains)`.

Update the two non-test call sites so the build stays green (real suffix plumbing arrives in Task 3):
- `internal/daemon/daemon.go:53`: `ca, err = localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, localtls.CACommonName, []string{"lewp"})`
- `internal/daemon/daemon.go:55`: `ca, err = localtls.NewCA(localtls.CACommonName, []string{"lewp"})`
- `internal/cli/cli.go:360`: `if _, err := localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, localtls.CACommonName, []string{suffix.BuiltIn}); err != nil {`

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tls/ ./internal/daemon/ ./internal/cli/ && go vet ./...`
Expected: PASS (all packages), vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tls/tls.go internal/tls/tls_test.go internal/daemon/daemon.go internal/cli/cli.go
git commit -m "feat: add critical name constraints to the local CA"
```

---

### Task 2: Minting allowlist in the TLS manager

**Files:**
- Modify: `internal/tls/tls.go` (Manager struct ~line 29, NewManager ~line 190, GetCertificate ~line 201)
- Test: `internal/tls/tls_test.go`

**Interfaces:**
- Consumes: `CACommonName`, `NewCA(commonName, permittedDNSDomains)`, `(c *CA) HasNameConstraints()` from Task 1.
- Produces: `tls.NewManager(ca *CA, allowedSuffixes []string) *Manager`. GetCertificate mints for any host inside `allowedSuffixes` that the CA's constraints cover; an unconstrained (legacy) CA mints for any allowed host.

- [ ] **Step 1: Write the failing tests**

In `internal/tls/tls_test.go`, **delete** `TestManagerRejectsConfiguredCustomSuffixSNI` (its documented V1 contract — custom suffixes are HTTP-only — is exactly what this feature changes) and add:

```go
func TestManagerMintsForAllowedCustomSuffix(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp", "local.todoordie.com"})
	leaf, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "app.local.todoordie.com"})
	if err != nil {
		t.Fatalf("refused allowed custom-suffix host: %v", err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "app.local.todoordie.com" {
		t.Fatalf("DNSNames=%v", cert.DNSNames)
	}
}

func TestManagerRejectsUnlistedSuffixes(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	// Domain mirrors and unmanaged names never appear in allowedSuffixes, so
	// the allowlist refuses them regardless of routing or DNS state.
	manager := NewManager(ca, []string{"lewp"})
	for _, host := range []string{"example.com", "app.localkickofflabs.com", "app.local.todoordie.com"} {
		if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: host}); err == nil {
			t.Fatalf("issued a leaf for unlisted host %q", host)
		}
	}
}

// TestManagerRefusesWhenCAConstraintsLagConfig: suffix configured but the CA
// was not rotated — refuse with a setup-pointing error instead of minting a
// cert the browser will reject with a scarier constraint-violation error.
func TestManagerRefusesWhenCAConstraintsLagConfig(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp", "local.todoordie.com"})
	_, err = manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "app.local.todoordie.com"})
	if err == nil {
		t.Fatal("minted despite CA constraints not covering the host")
	}
	if !strings.Contains(err.Error(), "lewp setup") {
		t.Fatalf("stale-CA error does not point at lewp setup: %v", err)
	}
}

// TestManagerLegacyUnconstrainedCAStillMints: during migration an existing
// unconstrained CA keeps working (doctor flags it); minting is not blocked.
func TestManagerLegacyUnconstrainedCAStillMints(t *testing.T) {
	ca, err := NewCA(CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp", "local.todoordie.com"})
	for _, host := range []string{"feature-1.audit.lewp", "app.local.todoordie.com"} {
		if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: host}); err != nil {
			t.Fatalf("legacy CA refused %q: %v", host, err)
		}
	}
}
```

Add `"strings"` to the test imports. Update every other `NewManager(ca)` call in the test file to `NewManager(ca, []string{"lewp"})`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tls/ -v`
Expected: compile FAIL — "too many arguments in call to NewManager".

- [ ] **Step 3: Implement**

In `internal/tls/tls.go`: add `allowed []string` to the Manager struct, and add `"github.com/scottwater/lewp/internal/suffix"` to the imports.

```go
type Manager struct {
	ca         *CA
	allowed    []string
	mu         sync.Mutex
	cache      map[string]*gotls.Certificate
	cacheOrder []string
	cacheLimit int
}

// NewManager builds a minting manager limited to allowedSuffixes: the built-in
// lewp suffix plus configured safe-subtree suffixes. Domain-mirror suffixes
// must never be passed — minting a real registrable apex domain is the one
// thing Lewp stays cryptographically unable to do.
func NewManager(ca *CA, allowedSuffixes []string) *Manager {
	return &Manager{ca: ca, allowed: append([]string(nil), allowedSuffixes...), cache: map[string]*gotls.Certificate{}, cacheLimit: 128}
}
```

Replace the doc comment and suffix check in `GetCertificate` (currently the `.lewp` HasSuffix check at ~line 194-208):

```go
// GetCertificate mints (and caches) a leaf certificate for the TLS SNI host.
//
// Minting is limited to the manager's allowlist (lewp + safe-subtree custom
// suffixes; never domain mirrors). When the CA carries name constraints that
// do not cover the host — a configured suffix whose CA rotation has not run —
// the handshake is refused with a setup-pointing error instead of serving a
// cert the browser would reject as a constraint violation.
func (m *Manager) GetCertificate(hello *gotls.ClientHelloInfo) (*gotls.Certificate, error) {
	host := hello.ServerName
	if host == "" {
		return nil, errors.New("missing TLS SNI host")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !suffix.HostInManagedSuffix(host, m.allowed) {
		return nil, fmt.Errorf("TLS SNI host %q is outside the TLS-enabled suffixes %v", host, m.allowed)
	}
	if m.ca.HasNameConstraints() && !hostPermitted(host, m.ca.Certificate.PermittedDNSDomains) {
		return nil, fmt.Errorf("local CA name constraints do not cover %q; re-run lewp setup to rotate the CA", host)
	}
	// ... existing cache/mint body unchanged from here ...
```

Add the helper (same subtree semantics as X.509 DNS constraints):

```go
// hostPermitted mirrors X.509 DNS name-constraint matching: a permitted entry
// covers itself and any subdomain.
func hostPermitted(host string, permitted []string) bool {
	for _, p := range permitted {
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}
```

Update the one non-test `NewManager` call site, `internal/daemon/daemon.go:60`: `tlsConfig = localtls.NewManager(ca, []string{"lewp"}).TLSConfig()` (real list arrives in Task 3).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tls/ ./internal/daemon/ && go vet ./...`
Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tls/tls.go internal/tls/tls_test.go internal/daemon/daemon.go
git commit -m "feat: allow TLS minting for configured safe-subtree suffixes"
```

---

### Task 3: Plumb TLS-eligible suffixes from config to daemon

**Files:**
- Modify: `internal/suffix/suffix.go` (add TLSEligible after Managed ~line 112)
- Modify: `internal/daemon/daemon.go` (Config + Serve TLS block)
- Modify: `internal/cli/cli.go:255-288` (runDaemon)
- Test: `internal/suffix/suffix_test.go`, `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `NewManager(ca, allowedSuffixes)`, `EnsureCA(..., permittedDNSDomains)`, `NewCA(..., permittedDNSDomains)`, `CACommonName` from Tasks 1-2.
- Produces: `suffix.TLSEligible(custom []Entry) []string` — `["lewp", <sorted safe-subtree names...>]`, mirrors of `suffix.Managed` filtering; `daemon.Config.TLSSuffixes []string` (empty defaults to `["lewp"]`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/suffix/suffix_test.go`:

```go
func TestTLSEligibleFiltersDomainMirrors(t *testing.T) {
	entries := []Entry{
		{Name: "localkickofflabs.com", Mode: ModeDomainMirror},
		{Name: "local.todoordie.com", Mode: ModeSafeSubtree},
	}
	got := TLSEligible(entries)
	want := []string{"lewp", "local.todoordie.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TLSEligible=%v want %v", got, want)
	}
}

func TestTLSEligibleEmptyConfig(t *testing.T) {
	if got := TLSEligible(nil); !reflect.DeepEqual(got, []string{"lewp"}) {
		t.Fatalf("TLSEligible(nil)=%v", got)
	}
}
```

(Add `"reflect"` to imports if missing.)

Append to `internal/daemon/daemon_test.go` (add `gotls "crypto/tls"`, `"crypto/x509"`, `"net"` to imports as needed):

```go
// TestServeMintsForConfiguredTLSSuffix drives a real TLS handshake through the
// daemon's HTTPS listener for a custom safe-subtree host and checks the served
// leaf, proving the TLSSuffixes plumbing end to end.
func TestServeMintsForConfiguredTLSSuffix(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{
			RegistryPath:    dir + "/registry.sqlite",
			HTTPSListeners:  []net.Listener{ln},
			ManagedSuffixes: []string{"lewp", "local.todoordie.com"},
			TLSSuffixes:     []string{"lewp", "local.todoordie.com"},
			CAPath:          dir + "/ca.pem",
			CAKeyPath:       dir + "/ca-key.pem",
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	}()

	conn, err := gotls.Dial("tcp", ln.Addr().String(), &gotls.Config{
		ServerName:         "app.local.todoordie.com",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("handshake for custom-suffix host failed: %v", err)
	}
	defer conn.Close()
	leaf := conn.ConnectionState().PeerCertificates[0]
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "app.local.todoordie.com" {
		t.Fatalf("served leaf DNSNames=%v", leaf.DNSNames)
	}
	var _ = x509.Certificate{} // keep import if unused elsewhere
}
```

(If `daemon_test.go` already imports these packages or has an existing handshake-style test, match its conventions and drop the `var _` line by using `x509` naturally or removing the import.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/suffix/ ./internal/daemon/ -run 'TestTLSEligible|TestServeMintsForConfiguredTLSSuffix' -v`
Expected: compile FAIL — "undefined: TLSEligible" and "unknown field TLSSuffixes".

- [ ] **Step 3: Implement**

`internal/suffix/suffix.go`, after `Managed`:

```go
// TLSEligible returns the suffixes the local CA may be constrained to and the
// TLS manager may mint for: the built-in lewp suffix plus every valid
// safe-subtree custom suffix. Domain mirrors are excluded by design — Lewp
// must stay cryptographically unable to issue certificates for a real
// registrable apex domain.
func TLSEligible(custom []Entry) []string {
	seen := map[string]struct{}{BuiltIn: {}}
	normalized := make([]string, 0, len(custom))
	for _, entry := range custom {
		if entry.Mode != ModeSafeSubtree {
			continue
		}
		s, err := ValidateCustom(entry.Name, entry.Mode)
		if err != nil {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		normalized = append(normalized, s)
	}
	sort.Strings(normalized)
	return append([]string{BuiltIn}, normalized...)
}
```

`internal/daemon/daemon.go`: add to Config after `ManagedSuffixes`:

```go
	// TLSSuffixes lists the suffixes the TLS manager may mint leaves for and
	// the CA (when created here) is constrained to: lewp + safe-subtree custom
	// suffixes, never domain mirrors. Empty means lewp only.
	TLSSuffixes []string
```

and change the TLS block (lines ~48-61):

```go
	tlsConfig := cfg.TLSConfig
	if tlsConfig == nil && len(cfg.HTTPSListeners) > 0 {
		tlsSuffixes := cfg.TLSSuffixes
		if len(tlsSuffixes) == 0 {
			tlsSuffixes = []string{"lewp"}
		}
		var ca *localtls.CA
		var err error
		if cfg.CAPath != "" && cfg.CAKeyPath != "" {
			// Create-if-missing only: rotation of an existing CA is setup's
			// job because only setup owns keychain prompts.
			ca, err = localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, localtls.CACommonName, tlsSuffixes)
		} else {
			ca, err = localtls.NewCA(localtls.CACommonName, tlsSuffixes)
		}
		if err != nil {
			return err
		}
		tlsConfig = localtls.NewManager(ca, tlsSuffixes).TLSConfig()
	}
```

`internal/cli/cli.go` runDaemon (~line 279-288): add `TLSSuffixes: suffix.TLSEligible(suffixCfg.Suffixes),` to the `daemon.Config` literal.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/suffix/ ./internal/daemon/ ./internal/cli/ && go vet ./...`
Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/suffix/suffix.go internal/suffix/suffix_test.go internal/daemon/daemon.go internal/daemon/daemon_test.go internal/cli/cli.go
git commit -m "feat: plumb TLS-eligible suffixes from config to the daemon"
```

---

### Task 4: Setup auto-rotation of the CA

**Files:**
- Modify: `internal/cli/cli.go` (runSetup ~lines 356-364 and output ~line 421; runSuffixRemove ~line 493-520)
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `suffix.TLSEligible`, `localtls.EnsureCA(..., domains)`, `localtls.ConstraintsMatch`, `localtls.UntrustCommand`, `localtls.CACommonName`.
- Produces: `ensureConstrainedCA(cfg Config, desired []string) (rotated bool, err error)` (unexported, cli package). Setup output gains `# rotating local CA ...` and a rotated `CA=` line; `suffix remove` prints a `Next: lewp setup` hint.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/cli_test.go` (uses the same harness as `TestRunSetupAddsCustomSuffixResolver` at line 553):

```go
// TestRunSetupRotatesLegacyUnconstrainedCA: an existing CA without name
// constraints (pre-2026-07 install) must be untrusted, regenerated with
// constraints, and re-trusted by setup.
func TestRunSetupRotatesLegacyUnconstrainedCA(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	legacy, err := localtls.NewCA(localtls.CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	var commands []string
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      t.TempDir(),
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand: func(_ context.Context, argv []string) error {
			commands = append(commands, strings.Join(argv, " "))
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	rotated, err := localtls.LoadCA(dir+"/ca.pem", dir+"/ca-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.HasNameConstraints() {
		t.Fatal("setup left an unconstrained CA in place")
	}
	if rotated.Certificate.SerialNumber.Cmp(legacy.Certificate.SerialNumber) == 0 {
		t.Fatal("setup kept the legacy CA certificate")
	}
	joined := strings.Join(commands, "\n")
	untrust := strings.Index(joined, "security delete-certificate -c "+localtls.CACommonName)
	trust := strings.Index(joined, "security add-trusted-cert")
	if untrust == -1 || trust == -1 || untrust > trust {
		t.Fatalf("expected untrust before re-trust, got:\n%s", joined)
	}
	if got := stdout.String(); !strings.Contains(got, "rotating local CA") {
		t.Fatalf("setup output missing rotation notice:\n%s", got)
	}
}

// TestRunSetupRotatesCAWhenSuffixAdded: adding a safe-subtree suffix to an
// install whose CA only covers lewp must rotate the CA to cover the suffix.
func TestRunSetupRotatesCAWhenSuffixAdded(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	existing, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := existing.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "local.todoordie.com"},
		WorkDir:      t.TempDir(),
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	rotated, err := localtls.LoadCA(dir+"/ca.pem", dir+"/ca-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if !localtls.ConstraintsMatch(rotated, []string{"lewp", "local.todoordie.com"}) {
		t.Fatalf("rotated CA constraints=%v", rotated.Certificate.PermittedDNSDomains)
	}
}

// TestRunSetupKeepsMatchingCA: a CA whose constraints already match must not
// be rotated (same serial before and after).
func TestRunSetupKeepsMatchingCA(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	existing, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := existing.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      t.TempDir(),
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	kept, err := localtls.LoadCA(dir+"/ca.pem", dir+"/ca-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Certificate.SerialNumber.Cmp(existing.Certificate.SerialNumber) != 0 {
		t.Fatal("setup rotated a CA whose constraints already matched")
	}
	if strings.Contains(stdout.String(), "rotating local CA") {
		t.Fatalf("unexpected rotation notice:\n%s", stdout.String())
	}
}

func TestSuffixRemovePrintsSetupHint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	if err := suffix.Save(dir+"/suffixes.toml", suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"suffix", "remove", "local.todoordie.com"},
		WorkDir:      t.TempDir(),
		Stdout:       &stdout,
		Stderr:       &stderr,
		SuffixesPath: dir + "/suffixes.toml",
		ResolverPath: dir + "/resolver/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Next: lewp setup") {
		t.Fatalf("suffix remove output missing setup hint:\n%s", stdout.String())
	}
}
```

Add `localtls "github.com/scottwater/lewp/internal/tls"` to the test imports if not present. If the existing `suffix remove` path requires more Config fields (check `runSuffixRemove` at `internal/cli/cli.go:493`), copy the field set from the nearest existing `suffix remove` test.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunSetupRotates|TestRunSetupKeeps|TestSuffixRemovePrints' -v`
Expected: FAIL — legacy CA left unconstrained ("setup left an unconstrained CA in place"), no rotation notice, no setup hint.

- [ ] **Step 3: Implement**

In `internal/cli/cli.go`, replace the `EnsureCA` block in runSetup (lines ~356-364) with:

```go
	// Create or rotate the local CA first. It is local and needs no sudo, so
	// doing it before the sudo resolver write and the keychain prompt avoids
	// leaving the system half-configured if CA generation fails after the user
	// has already authenticated.
	desiredTLS := suffix.TLSEligible(suffixCfg.Suffixes)
	rotatedCA, err := ensureConstrainedCA(cfg, desiredTLS)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "create CA: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: ensure %s is writable, then re-run: lewp setup\n", cfg.CAPath)
		return 1
	}
```

Add the helper near the other setup helpers:

```go
// ensureConstrainedCA loads-or-creates the local CA and rotates it when its
// name constraints do not match the TLS-eligible suffixes: untrust the old
// certificate, regenerate with the desired constraints, and let setup's later
// trust step re-add it — one keychain prompt total. Rotation lives here, not
// in the daemon, because only setup may touch the keychain.
func ensureConstrainedCA(cfg Config, desired []string) (bool, error) {
	ca, err := localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, localtls.CACommonName, desired)
	if err != nil {
		return false, err
	}
	if localtls.ConstraintsMatch(ca, desired) {
		return false, nil
	}
	fmt.Fprintf(cfg.Stdout, "# rotating local CA: name constraints change to %s\n", strings.Join(desired, ", "))
	if err := cfg.RunCommand(context.Background(), localtls.UntrustCommand(localtls.CACommonName)); err != nil {
		// The certificate may already be absent from the keychain; the later
		// add-trusted-cert step still installs the new one either way.
		fmt.Fprintf(cfg.Stderr, "# warning: could not remove old CA from keychain: %v\n", err)
	}
	if err := os.Remove(cfg.CAPath); err != nil {
		return false, err
	}
	if err := os.Remove(cfg.CAKeyPath); err != nil {
		return false, err
	}
	if _, err := localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, localtls.CACommonName, desired); err != nil {
		return false, err
	}
	return true, nil
}
```

Change the `CA=` output line (~line 421) to reflect rotation, and add a restart reminder:

```go
	if rotatedCA {
		fmt.Fprintf(cfg.Stdout, "CA=%s (rotated: constraints now %s)\n", cfg.CAPath, strings.Join(desiredTLS, ", "))
		fmt.Fprintln(cfg.Stdout, "# CA rotated: restart the daemon (lewp system restart) so HTTPS re-mints leaf certificates")
	} else {
		fmt.Fprintf(cfg.Stdout, "CA=%s\n", cfg.CAPath)
	}
```

In `runSuffixRemove` (after the successful save/removed output, ~line 516), add:

```go
	fmt.Fprintln(cfg.Stdout, "Next: lewp setup  # narrows the CA name constraints and refreshes resolver files")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/ && go vet ./...`
Expected: PASS (including all pre-existing setup tests — fresh temp dirs create matching CAs, so no rotation fires in them), vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: rotate the local CA when suffix constraints drift"
```

---

### Task 5: Doctor constraint checks

**Files:**
- Modify: `internal/cli/doctor.go:167-177` (local CA check inside `setupArtifactChecks`)
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `localtls.LoadCA`, `(c *CA) HasNameConstraints()`, `localtls.ConstraintsMatch`, `suffix.Load`, `suffix.TLSEligible`.
- Produces: doctor `local CA` check states — fail (missing/unreadable), fail (unconstrained), warn (constraint drift), ok.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/cli_test.go` (harness matches `TestDoctorKeychainCheckReflectsTrust` at line 235: call `setupArtifactChecks(cfg)` directly and use `findCheck`):

```go
func TestDoctorFailsUnconstrainedCA(t *testing.T) {
	dir := t.TempDir()
	legacy, err := localtls.NewCA(localtls.CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		PlistPath:    dir + "/none.plist",
		SuffixesPath: dir + "/suffixes.toml",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	}
	check := findCheck(t, setupArtifactChecks(cfg), "local CA")
	if check.Status != statusFail || check.Run != "lewp setup" {
		t.Fatalf("unconstrained CA check=%+v, want fail with lewp setup", check)
	}
	if !strings.Contains(check.Detail, "name constraints") {
		t.Fatalf("detail does not explain the problem: %q", check.Detail)
	}
}

func TestDoctorWarnsOnConstraintDrift(t *testing.T) {
	dir := t.TempDir()
	ca, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	if err := suffix.Save(dir+"/suffixes.toml", suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		PlistPath:    dir + "/none.plist",
		SuffixesPath: dir + "/suffixes.toml",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	}
	check := findCheck(t, setupArtifactChecks(cfg), "local CA")
	if check.Status != statusWarn || check.Run != "lewp setup" {
		t.Fatalf("drifted CA check=%+v, want warn with lewp setup", check)
	}
}

func TestDoctorPassesMatchingConstrainedCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		PlistPath:    dir + "/none.plist",
		SuffixesPath: dir + "/suffixes.toml",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	}
	check := findCheck(t, setupArtifactChecks(cfg), "local CA")
	if check.Status != statusOK {
		t.Fatalf("matching CA check=%+v, want ok", check)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestDoctorFailsUnconstrained|TestDoctorWarnsOnConstraintDrift|TestDoctorPassesMatching' -v`
Expected: `TestDoctorFailsUnconstrainedCA` and `TestDoctorWarnsOnConstraintDrift` FAIL (check currently reports ok for any loadable CA); `TestDoctorPassesMatchingConstrainedCA` may already pass.

- [ ] **Step 3: Implement**

Replace the `local CA` check in `setupArtifactChecks` (`internal/cli/doctor.go:167-177`):

```go
	ca := doctorCheck{Name: "local CA"}
	// Compare against the TLS-eligible suffixes from config; a load failure
	// falls back to lewp-only so a broken suffixes.toml does not mask CA state.
	desiredTLS := []string{suffix.BuiltIn}
	if suffixCfg, err := suffix.Load(cfg.SuffixesPath); err == nil {
		desiredTLS = suffix.TLSEligible(suffixCfg.Suffixes)
	}
	loadedCA, err := localtls.LoadCA(cfg.CAPath, cfg.CAKeyPath)
	switch {
	case err != nil:
		ca.Status = statusFail
		ca.Detail = "not present or unreadable"
		ca.Run = "lewp setup"
		ca.Inspect = cfg.CAPath
	case !loadedCA.HasNameConstraints():
		// Pre-constraint CA: trusted in the keychain but able to sign ANY
		// domain, so a stolen key forges public sites. Security posture
		// failure, not a functional warning.
		ca.Status = statusFail
		ca.Detail = "CA has no name constraints (a stolen key could sign certificates for any domain)"
		ca.Run = "lewp setup"
		ca.Inspect = cfg.CAPath
	case !localtls.ConstraintsMatch(loadedCA, desiredTLS):
		ca.Status = statusWarn
		ca.Detail = fmt.Sprintf("CA constraints %v do not match configured suffixes %v; HTTPS for uncovered suffixes will fail", loadedCA.Certificate.PermittedDNSDomains, desiredTLS)
		ca.Run = "lewp setup"
	default:
		ca.Status = statusOK
		ca.Detail = cfg.CAPath
	}
	checks = append(checks, ca)
```

(Ensure `doctor.go` imports `"fmt"` and `"github.com/scottwater/lewp/internal/suffix"` — check existing imports first.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/ && go vet ./...`
Expected: PASS. If pre-existing doctor tests created a CA via the old helper and now hit the unconstrained-CA fail path, update those tests to create their CA with `localtls.NewCA(localtls.CACommonName, []string{"lewp"})` — that is the new correct state, not a regression.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/doctor.go internal/cli/cli_test.go
git commit -m "feat: teach doctor to verify CA name constraints"
```

---

### Task 6: Documentation and full verification

**Files:**
- Modify: `README.md` (HTTPS section, ~lines 257-271, and the custom-suffix note near line 83)
- Modify: `DOCUMENTATION.md` (line 69 and the HTTPS_URL note near line 230)
- Modify: `docs/plan.md` (design decisions)
- Test: full suite

**Interfaces:**
- Consumes: final behavior from Tasks 1-5. No code changes.

- [ ] **Step 1: Update README.md**

In the `## HTTPS` section, after the existing `.lewp` explanation, document the new contract (adapt to surrounding prose, keeping these facts):

```markdown
Safe-subtree custom suffixes (for example `local.mycompany.com`) get the same
treatment: Lewp mints browser-trusted certificates for hosts under them, which
satisfies OAuth providers that require a real TLD. Domain-mirror suffixes stay
HTTP-only — Lewp's CA is cryptographically unable to sign a real registrable
domain.

The CA certificate carries critical X.509 name constraints limited to `.lewp`
plus your configured safe-subtree suffixes. Even if the CA key is stolen, a
forged certificate for any other domain is rejected by the browser. Adding or
removing a safe-subtree suffix rotates the CA on the next `lewp setup` (one
keychain prompt); restart the daemon afterwards so HTTPS re-mints leaves.
```

Update the note near line 83 that says custom public suffix hosts are HTTP-only: safe-subtree suffixes now support HTTPS after `lewp setup`; domain mirrors remain HTTP-only.

- [ ] **Step 2: Update DOCUMENTATION.md**

Replace line 69 (`Lewp's local TLS certificate issuance is limited to `.lewp` hosts.`) with:

```markdown
Lewp's local TLS certificate issuance covers `.lewp` hosts and configured
safe-subtree custom suffixes; domain-mirror suffixes are HTTP-only. The CA is
name-constrained to exactly that set and `lewp setup` rotates it (untrust,
regenerate, re-trust — one keychain prompt) whenever the set changes. `lewp
doctor` fails on a legacy unconstrained CA and warns when constraints drift
from the suffix config.
```

Update the HTTPS_URL note near line 230 to say the HTTPS URL is usable for `.lewp` and safe-subtree custom suffix hosts after `lewp setup`, and not for domain mirrors.

- [ ] **Step 3: Update docs/plan.md**

Find the locked design decisions list and append:

```markdown
- Local CA carries critical X.509 name constraints (`lewp` + safe-subtree
  custom suffixes, never domain mirrors), so a stolen CA key cannot sign
  certificates outside Lewp's scope. `lewp setup` rotates the CA when the
  constraint set drifts from the suffix config; the daemon never rotates
  (keychain prompts are setup-only). Decided 2026-07-01 after the deep
  security review; spec at
  docs/superpowers/specs/2026-07-01-custom-suffix-tls-design.md.
```

- [ ] **Step 4: Full verification**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

Run: `go test -race ./internal/tls/ ./internal/daemon/`
Expected: PASS (concurrency paths touched by Manager changes).

- [ ] **Step 5: Commit**

```bash
git add README.md DOCUMENTATION.md docs/plan.md
git commit -m "docs: document custom-suffix TLS and CA name constraints"
```

- [ ] **Step 6: Manual browser verification (user-assisted, post-merge)**

Not automatable in tests — run on the real machine after installing the new build:

1. `bin/reinstall && lewp setup` — expect the rotation notice and one keychain prompt.
2. `lewp system restart && lewp doctor` — local CA check must be ok.
3. Visit an existing `.lewp` HTTPS URL in Safari and Chrome — padlock, no warning.
4. `lewp setup --suffix local.<your-domain>.com`, restart, `lewp add` in a project with that suffix configured, visit `https://<host>` — padlock in both browsers.
5. Forgery check: `openssl` or a scratch Go program signs a leaf for `example.com` with `~/Library/Application Support/lewp/ca-key.pem`; serve it locally and confirm Safari and Chrome REJECT it (constraint violation).

---

## Self-Review Notes

- Spec coverage: CA constraints (Task 1), minting policy incl. stale-CA guard and legacy-CA minting (Task 2), plumbing incl. daemon create-never-rotate (Task 3), setup auto-rotation + suffix-remove hint (Task 4), doctor fail/warn (Task 5), docs + manual browser verification (Task 6). Out-of-scope items in spec (Firefox, mirrors, leaf migration) have no tasks — correct.
- Type consistency: `NewCA(commonName string, permittedDNSDomains []string)`, `EnsureCA(certPath, keyPath, commonName string, permittedDNSDomains []string)`, `NewManager(ca *CA, allowedSuffixes []string)`, `ConstraintsMatch(ca *CA, desired []string) bool`, `TLSEligible(custom []Entry) []string`, `daemon.Config.TLSSuffixes []string`, `ensureConstrainedCA(cfg Config, desired []string) (bool, error)` — used identically across tasks.
- Known judgment calls for the implementer: exact insertion points may drift a few lines from the `:NNN` references (the file has been edited since the review); anchor on the quoted code, not the line number. If `daemon_test.go` lacks the listed imports, add them; if an existing doctor/setup test now exercises the new fail path, fix the test's CA construction rather than weakening the check.
