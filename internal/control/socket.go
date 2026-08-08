package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
	"github.com/scottwater/lewp/internal/suffix"
)

const (
	// readTimeout bounds how long a connection may take to deliver its single
	// request. A local process that connects but sends nothing (or partial JSON)
	// is dropped instead of pinning a goroutine and fd forever.
	readTimeout = 10 * time.Second
	// writeTimeout bounds how long writing the response may block on a client
	// that stops reading.
	writeTimeout = 10 * time.Second
	// requestTimeout bounds the dispatched command itself so a wedged registry
	// operation cannot hang a handler indefinitely.
	requestTimeout = 15 * time.Second
	// maxRequestBytes caps a single request body. Control requests are small JSON
	// objects; this rejects an oversized/never-ending body well before it can
	// exhaust memory.
	maxRequestBytes = 1 << 20 // 1 MiB
	// handlerDrainTimeout bounds how long shutdown waits for in-flight handlers
	// to finish before the deferred store.Close runs, so a stuck handler cannot
	// block daemon shutdown forever.
	handlerDrainTimeout = 5 * time.Second
)

type Request struct {
	Command     string                `json:"command"`
	Lease       LeaseRequest          `json:"lease,omitempty"`
	Port        PortRequest           `json:"port,omitempty"`
	Release     ReleaseRequest        `json:"release,omitempty"`
	ReleasePlan *ReleasePlanReference `json:"release_plan,omitempty"`
	Info        InfoRequest           `json:"info,omitempty"`
	Move        MoveRequest           `json:"move,omitempty"`
	Alias       AliasRequest          `json:"alias,omitempty"`
	All         bool                  `json:"all,omitempty"`
}

type Response struct {
	Lease         *LeaseResponse         `json:"lease,omitempty"`
	Release       *ReleaseResponse       `json:"release,omitempty"`
	ReleasePlan   *ReleasePlanReference  `json:"release_plan,omitempty"`
	LegacyRelease *legacyReleaseResponse `json:"-"`
	AliasRemove   *AliasRemoveResponse   `json:"alias_remove,omitempty"`
	Entries       []ListEntry            `json:"entries,omitempty"`
	Checks        []string               `json:"checks,omitempty"`
	Error         string                 `json:"error,omitempty"`
}

func (r Response) MarshalJSON() ([]byte, error) {
	obj := map[string]any{}
	if r.Lease != nil {
		obj["lease"] = r.Lease
	}
	if r.LegacyRelease != nil {
		obj["release"] = r.LegacyRelease
	} else if r.Release != nil {
		obj["release"] = r.Release
	}
	if r.ReleasePlan != nil {
		obj["release_plan"] = r.ReleasePlan
	}
	if r.AliasRemove != nil {
		obj["alias_remove"] = r.AliasRemove
	}
	if r.Entries != nil {
		obj["entries"] = r.Entries
	}
	if len(r.Checks) > 0 {
		obj["checks"] = r.Checks
	}
	if r.Error != "" {
		obj["error"] = r.Error
	}
	return json.Marshal(obj)
}

func Serve(ctx context.Context, socketPath, registryPath string, portRange registry.PortRange) error {
	return ServeWithSuffixes(ctx, socketPath, registryPath, portRange, []string{suffix.BuiltIn})
}

func ServeWithSuffixes(ctx context.Context, socketPath, registryPath string, portRange registry.PortRange, managedSuffixes []string) error {
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
	svc.SetManagedSuffixes(managedSuffixes)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	// handlers tracks in-flight connection handlers so shutdown can drain them
	// before the deferred store.Close runs, avoiding a handler racing a closed
	// registry.
	var handlers sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			drainHandlers(&handlers)
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			handle(ctx, conn, svc)
		}()
	}
}

// drainHandlers waits for in-flight handlers to finish, but no longer than
// handlerDrainTimeout so a stuck handler cannot block shutdown. Per-connection
// deadlines keep handlers bounded, so this normally returns promptly.
func drainHandlers(handlers *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		handlers.Wait()
		close(done)
	}()
	timer := time.NewTimer(handlerDrainTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func Call(ctx context.Context, socketPath string, req Request) (Response, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	// DialContext only bounds the dial. Propagate the caller's deadline to the
	// connection so a wedged daemon that accepts but never replies cannot hang
	// the encode/decode (and therefore the CLI command) forever.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
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

func handle(ctx context.Context, conn net.Conn, svc *Service) {
	serveConn(ctx, conn, func(ctx context.Context, req Request) (Response, error) {
		return dispatch(ctx, svc, req)
	})
}

// serveConn reads one request, dispatches it, and writes one response. A panic
// from a malformed or crafted request is recovered and returned as an error
// response so a single bad connection cannot crash the daemon (which also owns
// the proxy and DNS responder).
//
// The connection carries read/write deadlines and a request-size cap so a local
// process that stalls, sends partial JSON, or streams an oversized body cannot
// pin a handler goroutine. The dispatched command runs under a context derived
// from the server context (so shutdown cancels in-flight work) with its own
// timeout.
func serveConn(ctx context.Context, conn net.Conn, dispatch func(context.Context, Request) (Response, error)) {
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer func() {
		if rec := recover(); rec != nil {
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			_ = json.NewEncoder(conn).Encode(Response{Error: fmt.Sprintf("internal error: %v", rec)})
		}
	}()
	writeResponse := func(resp Response) {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_ = json.NewEncoder(conn).Encode(resp)
	}
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	var req Request
	if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&req); err != nil {
		writeResponse(Response{Error: err.Error()})
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := dispatch(reqCtx, req)
	if err != nil {
		resp.Error = err.Error()
	}
	writeResponse(resp)
}

type legacyReleaseResponse struct {
	Routes int `json:"routes"`
	Ports  int `json:"ports"`
}

func legacyReleaseCounts(result ReleaseResponse) legacyReleaseResponse {
	var counts legacyReleaseResponse
	for _, item := range result.Items {
		for _, action := range item.Actions {
			if action != registry.ReleaseActionRelease {
				continue
			}
			if item.Kind == identity.KindRoute {
				counts.Routes++
			} else if item.Kind == identity.KindPort {
				counts.Ports++
			}
			break
		}
	}
	return counts
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
		res, err := svc.Release(ctx, req.Release)
		if err != nil {
			return Response{}, err
		}
		legacy := legacyReleaseCounts(res)
		return Response{Release: &res, LegacyRelease: &legacy}, nil
	case "release-plan":
		res, plan, err := svc.PlanRelease(ctx, req.Release)
		if err != nil {
			return Response{}, err
		}
		ref, err := svc.releasePlanReference(plan)
		if err != nil {
			return Response{}, err
		}
		return Response{Release: &res, ReleasePlan: &ref}, nil
	case "release-apply":
		if req.ReleasePlan == nil {
			return Response{}, errors.New("release-apply requires a release plan")
		}
		res, err := svc.applyReleaseReference(ctx, *req.ReleasePlan, req.Release.DryRun)
		if err != nil {
			return Response{}, err
		}
		return Response{Release: &res}, nil
	case "list":
		entries, err := svc.List(ctx, req.All)
		return Response{Entries: entries}, err
	case "info":
		entries, err := svc.Info(ctx, req.Info)
		return Response{Entries: entries}, err
	case "move":
		entries, err := svc.Move(ctx, req.Move)
		return Response{Entries: entries}, err
	case "alias-add":
		lease, err := svc.AliasAdd(ctx, req.Alias)
		return Response{Lease: &lease}, err
	case "alias-remove":
		res, err := svc.AliasRemove(ctx, req.Alias)
		return Response{AliasRemove: &res}, err
	case "alias-list":
		entries, err := svc.AliasList(ctx, req.Alias)
		return Response{Entries: entries}, err
	case "doctor":
		return Response{Checks: svc.Doctor(ctx)}, nil
	default:
		return Response{}, errors.New("unknown command")
	}
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
