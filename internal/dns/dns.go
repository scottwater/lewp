package dns

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

const DefaultPort = 15353

func Handle(packet []byte) ([]byte, error) {
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
	if len(msg.Questions) == 0 || !isLewp(msg.Questions[0].Name.String()) {
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
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
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
		reply, err := Handle(buf[:n])
		if err == nil {
			_, _ = conn.WriteTo(reply, peer)
		}
	}
}

func ResolverFile(port int) string {
	return "nameserver 127.0.0.1\nport " + itoa(port) + "\n"
}

func WriteResolverFile(path string, port int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(ResolverFile(port)), 0o644)
}

func isLewp(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "lewp" || strings.HasSuffix(host, ".lewp")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
