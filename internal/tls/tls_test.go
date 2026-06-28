package tls

import (
	gotls "crypto/tls"
	"crypto/x509"
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
	if got := join(add); got != "security add-trusted-cert -d -r trustRoot -k login.keychain /tmp/lewp-ca.pem" {
		t.Fatalf("trust command=%q", got)
	}
	remove := UntrustCommand("Lewp Local Development CA")
	if got := join(remove); got != "security delete-certificate -c Lewp Local Development CA" {
		t.Fatalf("untrust command=%q", got)
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
