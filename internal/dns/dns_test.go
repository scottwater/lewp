package dns

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
