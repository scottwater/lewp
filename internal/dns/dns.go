package dns

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scottwater/lewp/internal/suffix"
	"golang.org/x/net/dns/dnsmessage"
)

const DefaultPort = 15353

func Handle(packet []byte) ([]byte, error) {
	return HandleWithSuffixes(packet, []string{suffix.BuiltIn})
}

func HandleWithSuffixes(packet []byte, managed []string) ([]byte, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(packet); err != nil {
		return nil, err
	}
	reply := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:            msg.Header.ID,
			Response:      true,
			Authoritative: true,
			RCode:         dnsmessage.RCodeSuccess,
		},
		Questions: msg.Questions,
	}
	if len(msg.Questions) == 0 || !suffix.HostInManagedSuffix(msg.Questions[0].Name.String(), managed) {
		reply.Header.RCode = dnsmessage.RCodeNameError
		return reply.Pack()
	}
	q := msg.Questions[0]
	switch q.Type {
	case dnsmessage.TypeA:
		reply.Answers = append(reply.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 1},
			Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
		})
	case dnsmessage.TypeAAAA:
		reply.Answers = append(reply.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 1},
			Body:   &dnsmessage.AAAAResource{AAAA: [16]byte{15: 1}},
		})
	default:
		reply.Header.RCode = dnsmessage.RCodeSuccess
	}
	return reply.Pack()
}

func Serve(ctx context.Context, addr string) error {
	return ServeWithSuffixes(ctx, addr, []string{suffix.BuiltIn})
}

func ServeWithSuffixes(ctx context.Context, addr string, managed []string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Close the socket when ctx is cancelled so the blocking ReadFrom returns.
	// done bounds the watcher to this function so it never outlives Serve when
	// the read loop exits for its own reason (e.g. an unexpected read error).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = conn.Close()
	}()
	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		reply, err := HandleWithSuffixes(buf[:n], managed)
		if err == nil {
			_, _ = conn.WriteTo(reply, peer)
		}
	}
}

func ResolverFile(port int) string {
	return "nameserver 127.0.0.1\nport " + strconv.Itoa(port) + "\n"
}

func WriteResolverFile(path string, port int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(ResolverFile(port)), 0o644)
}

// ResolverPort extracts the "port N" value from /etc/resolver/lewp content. It
// reports ok=false when no port line is present, so doctor can distinguish a
// malformed resolver file from one pointing at the wrong port.
func ResolverPort(content string) (int, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "port" {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 && n <= 65535 {
				return n, true
			}
		}
	}
	return 0, false
}

// Lookup queries the .lewp DNS responder at server (host:port) for host's A
// record and returns the first address. It is used by doctor to confirm the
// daemon's DNS responder answers .lewp names with loopback.
func Lookup(ctx context.Context, server, host string) (net.IP, error) {
	fqdn := strings.TrimSuffix(strings.ToLower(host), ".") + "."
	name, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(packed); err != nil {
		return nil, err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	var reply dnsmessage.Message
	if err := reply.Unpack(buf[:n]); err != nil {
		return nil, err
	}
	for _, ans := range reply.Answers {
		if a, ok := ans.Body.(*dnsmessage.AResource); ok {
			return net.IP(a.A[:]), nil
		}
	}
	return nil, errors.New("no A record in response")
}
