package tls

import (
	"crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"testing"
)

func TestCreateCAAndLeafCertificate(t *testing.T) {
	ca, err := NewCA("Lewp Local Development CA")
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
	ca, err := NewCA("Lewp Local Development CA")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca)
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
	first, err := EnsureCA(certPath, keyPath, "Lewp Local Development CA")
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCA(certPath, keyPath, "Lewp Local Development CA")
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
	if _, err := EnsureCA(certPath, keyPath, "Lewp Local Development CA"); err == nil {
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
	ca, err := NewCA("Lewp Local Development CA")
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
	recovered, err := EnsureCA(certPath, keyPath, "Lewp Local Development CA")
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
	ca, err := NewCA("Lewp Local Development CA")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca)
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
	ca, err := NewCA("Lewp Local Development CA")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ca)
	if _, err := manager.GetCertificate(&gotls.ClientHelloInfo{ServerName: "example.com"}); err == nil {
		t.Fatal("accepted non-.lewp SNI")
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
