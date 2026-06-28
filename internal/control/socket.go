package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/scottwater/lewp/internal/registry"
)

type Request struct {
	Command string         `json:"command"`
	Lease   LeaseRequest   `json:"lease,omitempty"`
	Port    PortRequest    `json:"port,omitempty"`
	Release ReleaseRequest `json:"release,omitempty"`
	Info    InfoRequest    `json:"info,omitempty"`
	Move    MoveRequest    `json:"move,omitempty"`
	All     bool           `json:"all,omitempty"`
}

type Response struct {
	Lease   *LeaseResponse `json:"lease,omitempty"`
	Entries []ListEntry    `json:"entries,omitempty"`
	Checks  []string       `json:"checks,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func Serve(ctx context.Context, socketPath, registryPath string, portRange registry.PortRange) error {
	if err := os.MkdirAll(dir(socketPath), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir(socketPath), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(dir(registryPath), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir(registryPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	defer ln.Close()

	store, err := registry.Open(registryPath)
	if err != nil {
		return err
	}
	defer store.Close()
	svc := NewService(store, portRange)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return err
		}
		go handle(conn, svc)
	}
}

func Call(ctx context.Context, socketPath string, req Request) (Response, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func handle(conn net.Conn, svc *Service) {
	serveConn(conn, func(ctx context.Context, req Request) (Response, error) {
		return dispatch(ctx, svc, req)
	})
}

// serveConn reads one request, dispatches it, and writes one response. A panic
// from a malformed or crafted request is recovered and returned as an error
// response so a single bad connection cannot crash the daemon (which also owns
// the proxy and DNS responder).
func serveConn(conn net.Conn, dispatch func(context.Context, Request) (Response, error)) {
	defer conn.Close()
	defer func() {
		if rec := recover(); rec != nil {
			_ = json.NewEncoder(conn).Encode(Response{Error: fmt.Sprintf("internal error: %v", rec)})
		}
	}()
	var req Request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: err.Error()})
		return
	}
	resp, err := dispatch(connContext(), req)
	if err != nil {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

func dispatch(ctx context.Context, svc *Service, req Request) (Response, error) {
	switch req.Command {
	case "add":
		lease, err := svc.Lease(ctx, req.Lease)
		return Response{Lease: &lease}, err
	case "port":
		lease, err := svc.Port(ctx, req.Port)
		return Response{Lease: &lease}, err
	case "release":
		return Response{}, svc.Release(ctx, req.Release)
	case "list":
		entries, err := svc.List(ctx, req.All)
		return Response{Entries: entries}, err
	case "info":
		entries, err := svc.Info(ctx, req.Info)
		return Response{Entries: entries}, err
	case "move":
		entries, err := svc.Move(ctx, req.Move)
		return Response{Entries: entries}, err
	case "doctor":
		return Response{Checks: svc.Doctor(ctx)}, nil
	default:
		return Response{}, errors.New("unknown command")
	}
}

func connContext() context.Context {
	return context.Background()
}

func dir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}
