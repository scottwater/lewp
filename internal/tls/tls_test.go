package tls

import (
	"crypto/rand"
	"crypto/rsa"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCreateCAAndLeafCertificate(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if !ca.Certificate.IsCA {
		t.Fatal("CA certificate is not a CA")
	}

	leaf, err := ca.Leaf("feature-1.audit.lewp")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "feature-1.audit.lewp" {
		t.Fatalf("DNSNames=%v", cert.DNSNames)
	}
}

func TestManagerCachesSNILeaves(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp"})
	first, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "feature-1.audit.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "feature-1.audit.lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("leaf certificate was not cached")
	}
	cfg := manager.TLSConfig()
	if cfg.GetCertificate == nil {
		t.Fatal("TLSConfig missing GetCertificate")
	}
}

func TestEnsureCALoadsPersistedCA(t *testing.T) {
	dir := t.TempDir()
	certPath := dir + "/ca.pem"
	keyPath := dir + "/ca-key.pem"
	first, err := EnsureCA(certPath, keyPath, CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCA(certPath, keyPath, CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Certificate.SerialNumber.Cmp(second.Certificate.SerialNumber) != 0 {
		t.Fatal("EnsureCA did not load existing CA")
	}
	if _, err := second.Leaf("feature-1.audit.lewp"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCADoesNotOverwriteCorruptCA(t *testing.T) {
	dir := t.TempDir()
	certPath := dir + "/ca.pem"
	keyPath := dir + "/ca-key.pem"
	corrupt := []byte("not a valid PEM file")
	if err := os.WriteFile(certPath, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCA(certPath, keyPath, CACommonName, []string{"lewp"}); err == nil {
		t.Fatal("EnsureCA succeeded over corrupt CA material")
	}
	got, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatal("EnsureCA overwrote existing CA certificate on load error")
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotKey) != string(corrupt) {
		t.Fatal("EnsureCA overwrote existing CA key on load error")
	}
}

func TestEnsureCARecoversCertOnlyPartialSave(t *testing.T) {
	dir := t.TempDir()
	certPath := dir + "/ca.pem"
	keyPath := dir + "/ca-key.pem"
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	recovered, err := EnsureCA(certPath, keyPath, CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(original) {
		t.Fatal("EnsureCA kept orphaned cert-only CA")
	}
	if recovered.Certificate.SerialNumber.Cmp(ca.Certificate.SerialNumber) == 0 {
		t.Fatal("EnsureCA reused orphaned certificate")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("EnsureCA did not recreate key: %v", err)
	}
}

func TestSerialReturnsErrorOnEntropyFailure(t *testing.T) {
	if _, err := serial(failingReader{}); err == nil {
		t.Fatal("serial did not propagate entropy failure")
	}
}

func TestSerialUsesProvidedEntropy(t *testing.T) {
	first, err := serial(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := serial(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cmp(second) == 0 {
		t.Fatal("serial returned identical values from random entropy")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("no entropy available")
}

func TestManagerEvictsOldSNILeaves(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp"})
	manager.cacheLimit = 2
	for _, host := range []string{"a.test.lewp", "b.test.lewp"} {
		if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: host}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "a.test.lewp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "c.test.lewp"}); err != nil {
		t.Fatal(err)
	}
	if manager.cache["b.test.lewp"] != nil {
		t.Fatal("oldest leaf certificate was not evicted")
	}
	if manager.cache["a.test.lewp"] == nil {
		t.Fatal("recently used leaf certificate was evicted")
	}
}

func TestManagerRejectsNonLewpSNI(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp"})
	if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "example.com"}); err == nil {
		t.Fatal("accepted non-.lewp SNI")
	}
}

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

// TestManagerConcurrentSameHostConverges exercises the relaxed locking in
// GetCertificate (leaf generation runs outside m.mu): concurrent handshakes for
// one host must converge on a single cached certificate and not corrupt the LRU
// bookkeeping. Run with -race to catch cache/order data races.
func TestManagerConcurrentSameHostConverges(t *testing.T) {
	ca, err := NewCA(CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca, []string{"lewp"})

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]*gotls.Certificate, goroutines)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cert, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "feature-1.audit.lewp"})
			if err != nil {
				t.Errorf("goroutine %d: %v", idx, err)
				return
			}
			results[idx] = cert
		}(i)
	}
	wg.Wait()

	for i, cert := range results {
		if cert != results[0] {
			t.Fatalf("goroutine %d saw a different cached certificate than goroutine 0", i)
		}
	}
	if len(manager.cacheOrder) != 1 || manager.cache["feature-1.audit.lewp"] == nil {
		t.Fatalf("cache did not converge on one leaf: order=%v", manager.cacheOrder)
	}
}

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

// TestConstraintsMatchRequiresMaxPathLenZero: MaxPathLenZero is one of the
// security-critical properties NewCA always sets alongside the DNS/IP
// constraints (no sub-CAs, so the constraints cannot be laundered through an
// intermediate). NewCA can't produce a CA with this property unset, so this
// test crafts and self-signs an x509 template directly to simulate a future
// refactor that drops MaxPathLenZero while leaving the other constraints
// intact — ConstraintsMatch must still refuse it.
func TestConstraintsMatchRequiresMaxPathLenZero(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	desired := []string{"lewp", "local.todoordie.com"}
	tmpl := &x509.Certificate{
		SerialNumber:                big.NewInt(1),
		Subject:                     pkix.Name{CommonName: CACommonName},
		NotBefore:                   time.Now().Add(-time.Hour),
		NotAfter:                    time.Now().AddDate(10, 0, 0),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		PermittedDNSDomains:         append([]string(nil), desired...),
		PermittedDNSDomainsCritical: true,
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
		// Deliberately left false: this is the property under test.
		MaxPathLenZero: false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ca := &CA{Certificate: cert, Key: key, CertDER: der}
	if ConstraintsMatch(ca, desired) {
		t.Fatal("ConstraintsMatch reported a match despite MaxPathLenZero being unset")
	}
}

func TestKeychainCommands(t *testing.T) {
	add := TrustCommand("/tmp/lewp-ca.pem")
	if got := join(add); got != "security add-trusted-cert -r trustRoot -p ssl -k login.keychain /tmp/lewp-ca.pem" {
		t.Fatalf("trust command=%q", got)
	}
	remove := UntrustCommand("Lewp Local Development CA")
	if got := join(remove); got != "security delete-certificate -c Lewp Local Development CA" {
		t.Fatalf("untrust command=%q", got)
	}
	check := TrustCheckCommand("/tmp/lewp-ca.pem")
	if got := join(check); got != "security verify-cert -c /tmp/lewp-ca.pem -p ssl" {
		t.Fatalf("trust check command=%q", got)
	}
}

func join(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += " "
		}
		out += part
	}
	return out
}

// TestDefaultCAPathsFailWithoutHome confirms the CA default paths return an
// error rather than a cwd-relative fallback ("lewp-ca.pem") when the home
// directory cannot be determined, so a relative CA path cannot let the CLI and
// daemon read/write different certificate files.
func TestDefaultCAPathsFailWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	for name, fn := range map[string]func() (string, error){
		"DefaultCAPath":    DefaultCAPath,
		"DefaultCAKeyPath": DefaultCAKeyPath,
	} {
		got, err := fn()
		if err == nil {
			t.Fatalf("%s returned %q, want error when home is unavailable", name, got)
		}
		if got != "" {
			t.Fatalf("%s returned non-empty path %q alongside error", name, got)
		}
		if !strings.Contains(err.Error(), "cannot determine home directory") {
			t.Fatalf("%s error = %q, want it to mention home directory", name, err)
		}
	}
}
