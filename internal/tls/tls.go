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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CA struct {
	Certificate *x509.Certificate
	Key         *rsa.PrivateKey
	CertDER     []byte
}

type Manager struct {
	ca         *CA
	mu         sync.Mutex
	cache      map[string]*gotls.Certificate
	cacheOrder []string
	cacheLimit int
}

func NewCA(commonName string) (*CA, error) {
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

func EnsureCA(certPath, keyPath, commonName string) (*CA, error) {
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
	ca, err = NewCA(commonName)
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

func NewManager(ca *CA) *Manager {
	return &Manager{ca: ca, cache: map[string]*gotls.Certificate{}, cacheLimit: 128}
}

func (m *Manager) GetCertificate(hello *gotls.ClientHelloInfo) (*gotls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	host := hello.ServerName
	if host == "" {
		return nil, errors.New("missing TLS SNI host")
	}
	if !strings.HasSuffix(strings.TrimSuffix(strings.ToLower(host), "."), ".lewp") {
		return nil, fmt.Errorf("TLS SNI host %q must be inside .lewp", host)
	}
	if cert := m.cache[host]; cert != nil {
		m.touch(host)
		return cert, nil
	}
	cert, err := m.ca.Leaf(host)
	if err != nil {
		return nil, err
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
