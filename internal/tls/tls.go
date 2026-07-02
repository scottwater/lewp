package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scottwater/lewp/internal/suffix"
)

// CACommonName is the subject and keychain identity of the Lewp local CA.
const CACommonName = "Lewp Local Development CA"

type CA struct {
	Certificate *x509.Certificate
	Key         *rsa.PrivateKey
	CertDER     []byte
}

type Manager struct {
	ca         *CA
	allowed    []string
	mu         sync.Mutex
	cache      map[string]*gotls.Certificate
	cacheOrder []string
	cacheLimit int
}

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

func EnsureCA(certPath, keyPath, commonName string, permittedDNSDomains []string) (*CA, error) {
	ca, err := LoadCA(certPath, keyPath)
	if err == nil {
		return ca, nil
	}
	if fileExists(certPath) && !fileExists(keyPath) {
		if removeErr := os.Remove(certPath); removeErr != nil {
			return nil, err
		}
	} else if fileExists(keyPath) && !fileExists(certPath) {
		if removeErr := os.Remove(keyPath); removeErr != nil {
			return nil, err
		}
	}
	// Only regenerate from a clean slate. If either file already exists, the
	// load failure means corrupt, mismatched, or unreadable material — never
	// overwrite it, or we silently mint a new untrusted CA and break HTTPS.
	if fileExists(certPath) || fileExists(keyPath) {
		return nil, err
	}
	ca, err = NewCA(commonName, permittedDNSDomains)
	if err != nil {
		return nil, err
	}
	if err := ca.Save(certPath, keyPath); err != nil {
		return nil, err
	}
	return ca, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("missing CA certificate PEM")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "RSA PRIVATE KEY" {
		return nil, errors.New("missing CA private key PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{Certificate: cert, Key: key, CertDER: certBlock.Bytes}, nil
}

func (c *CA) Save(certPath, keyPath string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.CertDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(c.Key)})
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(certPath, certPEM, 0o644)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

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

func (c *CA) Leaf(host string) (*gotls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial(rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Certificate, &key.PublicKey, c.Key)
	if err != nil {
		return nil, err
	}
	return &gotls.Certificate{Certificate: [][]byte{der, c.CertDER}, PrivateKey: key}, nil
}

// NewManager builds a minting manager limited to allowedSuffixes: the built-in
// lewp suffix plus configured safe-subtree suffixes. Domain-mirror suffixes
// must never be passed — minting a real registrable apex domain is the one
// thing Lewp stays cryptographically unable to do.
func NewManager(ca *CA, allowedSuffixes []string) *Manager {
	return &Manager{ca: ca, allowed: append([]string(nil), allowedSuffixes...), cache: map[string]*gotls.Certificate{}, cacheLimit: 128}
}

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

	// Hold m.mu only for cache reads and writes, never across leaf generation.
	// m.ca.Leaf does ECDSA keygen and signing; holding the lock across it would
	// serialize every concurrent handshake — even ones whose host is already
	// cached — behind a single cold mint.
	m.mu.Lock()
	if cert := m.cache[host]; cert != nil {
		m.touch(host)
		m.mu.Unlock()
		return cert, nil
	}
	m.mu.Unlock()

	cert, err := m.ca.Leaf(host)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// A concurrent handshake for the same host may have minted and cached a leaf
	// while we generated ours; reuse the cached one so all callers for this host
	// converge on a single certificate instead of racing to overwrite it.
	if existing := m.cache[host]; existing != nil {
		m.touch(host)
		return existing, nil
	}
	m.cache[host] = cert
	m.cacheOrder = append(m.cacheOrder, host)
	for len(m.cacheOrder) > m.cacheLimit {
		delete(m.cache, m.cacheOrder[0])
		m.cacheOrder = m.cacheOrder[1:]
	}
	return cert, nil
}

func (m *Manager) touch(host string) {
	for i, cached := range m.cacheOrder {
		if cached == host {
			copy(m.cacheOrder[i:], m.cacheOrder[i+1:])
			m.cacheOrder[len(m.cacheOrder)-1] = host
			return
		}
	}
}

func (m *Manager) TLSConfig() *gotls.Config {
	return &gotls.Config{GetCertificate: m.GetCertificate, MinVersion: gotls.VersionTLS12}
}

func TrustCommand(certPath string) []string {
	return []string{"security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", "login.keychain", certPath}
}

func UntrustCommand(commonName string) []string {
	return []string{"security", "delete-certificate", "-c", commonName}
}

func TrustCheckCommand(certPath string) []string {
	return []string{"security", "verify-cert", "-c", certPath, "-p", "ssl"}
}

func DefaultCAPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "lewp-ca.pem"
	}
	return filepath.Join(home, "Library", "Application Support", "lewp", "ca.pem")
}

func DefaultCAKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "lewp-ca-key.pem"
	}
	return filepath.Join(home, "Library", "Application Support", "lewp", "ca-key.pem")
}

func serial(r io.Reader) (*big.Int, error) {
	return rand.Int(r, new(big.Int).Lsh(big.NewInt(1), 128))
}
