package dns

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestHandlerResolvesAnyLewpHostToLoopback(t *testing.T) {
	query := mustQuery(t, "feature-1.audit.lewp.", dnsmessage.TypeA)

	reply, err := Handle(query)
	if err != nil {
		t.Fatal(err)
	}
	answers := parseAnswers(t, reply)
	if len(answers) != 1 {
		t.Fatalf("answers=%d", len(answers))
	}
	a, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer type=%T", answers[0].Body)
	}
	if got := netip.AddrFrom4(a.A); got.String() != "127.0.0.1" {
		t.Fatalf("A=%s", got)
	}
}

func TestHandleWithSuffixesAnswersCustomManagedSuffix(t *testing.T) {
	query := mustQuery(t, "feature-1.local.todoordie.com.", dnsmessage.TypeA)

	reply, err := HandleWithSuffixes(query, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	answers := parseAnswers(t, reply)
	if len(answers) != 1 {
		t.Fatalf("answers=%d", len(answers))
	}
	a, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer type=%T", answers[0].Body)
	}
	if got := netip.AddrFrom4(a.A); got.String() != "127.0.0.1" {
		t.Fatalf("A=%s", got)
	}
}

func TestHandleWithSuffixesAnswersCustomManagedSuffixApex(t *testing.T) {
	query := mustQuery(t, "localkickofflabs.com.", dnsmessage.TypeA)

	reply, err := HandleWithSuffixes(query, []string{"lewp", "localkickofflabs.com"})
	if err != nil {
		t.Fatal(err)
	}
	answers := parseAnswers(t, reply)
	if len(answers) != 1 {
		t.Fatalf("answers=%d", len(answers))
	}
	a, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer type=%T", answers[0].Body)
	}
	if got := netip.AddrFrom4(a.A); got.String() != "127.0.0.1" {
		t.Fatalf("A=%s", got)
	}
}

func TestHandlerRejectsNonLewpHost(t *testing.T) {
	query := mustQuery(t, "example.com.", dnsmessage.TypeA)

	reply, err := Handle(query)
	if err != nil {
		t.Fatal(err)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	if header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("rcode=%v", header.RCode)
	}
}

func TestHandleWithSuffixesRejectsUnmanagedSuffix(t *testing.T) {
	query := mustQuery(t, "feature-1.todoordie.com.", dnsmessage.TypeA)

	reply, err := HandleWithSuffixes(query, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	if header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("rcode=%v", header.RCode)
	}
}

func TestResolverFileUsesHighPort(t *testing.T) {
	got := ResolverFile(15353)
	for _, want := range []string{"nameserver 127.0.0.1", "port 15353"} {
		if !strings.Contains(got, want) {
			t.Fatalf("resolver missing %q:\n%s", want, got)
		}
	}
}

func TestResolverPort(t *testing.T) {
	got, ok := ResolverPort("nameserver 127.0.0.1\nport 15353\n")
	if !ok || got != 15353 {
		t.Fatalf("ResolverPort()=(%d, %v)", got, ok)
	}
	for _, content := range []string{
		"nameserver 127.0.0.1\n",
		"port -1\n",
		"port 0\n",
		"port 65536\n",
		"port nope\n",
	} {
		if got, ok := ResolverPort(content); ok {
			t.Fatalf("ResolverPort(%q)=(%d, true)", content, got)
		}
	}
}

func TestWriteResolverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolver", "lewp")
	if err := WriteResolverFile(path, 15353); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ResolverFile(15353) {
		t.Fatalf("resolver file=%q", string(got))
	}
}

func TestServeAnswersOverUDPAndShutsDown(t *testing.T) {
	addr := freeUDPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- ServeWithSuffixes(ctx, addr, []string{"lewp"}) }()

	// Retry the round trip until the server has bound its socket, rather than
	// sleeping for a fixed interval.
	ip, err := lookupWithRetry(t, ctx, addr, "feature-1.audit.lewp.")
	if err != nil {
		cancel()
		t.Fatalf("lookup: %v", err)
	}
	if ip.String() != "127.0.0.1" {
		cancel()
		t.Fatalf("A=%s", ip)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not shut down after ctx cancel")
	}
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func lookupWithRetry(t *testing.T, ctx context.Context, server, host string) (net.IP, error) {
	t.Helper()
	var lastErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		queryCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		ip, err := Lookup(queryCtx, server, host)
		cancel()
		if err == nil {
			return ip, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func mustQuery(t *testing.T, host string, typ dnsmessage.Type) []byte {
	t.Helper()
	name, err := dnsmessage.NewName(host)
	if err != nil {
		t.Fatal(err)
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  typ,
			Class: dnsmessage.ClassINET,
		}},
	}
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func parseAnswers(t *testing.T, reply []byte) []dnsmessage.Resource {
	t.Helper()
	var msg dnsmessage.Message
	if err := msg.Unpack(reply); err != nil {
		t.Fatal(err)
	}
	return msg.Answers
}
