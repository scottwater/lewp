package daemon

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

func TestServeRoutesHTTPSWithSNI(t *testing.T) {
	upstream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	})}
	upstreamLn := listenLocal(t)
	go func() { _ = upstream.Serve(upstreamLn) }()
	t.Cleanup(func() { _ = upstream.Close() })

	registryPath := t.TempDir() + "/registry.sqlite"
	store, err := registry.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Remember(context.Background(), identity.Result{
		Root:           "audit",
		Name:           "feature-1",
		NormalizedRoot: "audit",
		NormalizedName: "feature-1",
		Host:           "feature-1.audit.lewp",
		HostKind:       identity.HostKindInstance,
		HostSource:     identity.SourceInferred,
		Path:           t.TempDir(),
		Kind:           identity.KindRoute,
	}, upstreamLn.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	httpsLn := listenLocal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Serve(ctx, Config{
			RegistryPath:   registryPath,
			HTTPSListeners: []net.Listener{httpsLn},
		})
	}()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "feature-1.audit.lewp"}}}
	req, err := http.NewRequest(http.MethodGet, "https://"+httpsLn.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "feature-1.audit.lewp"
	var resp *http.Response
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "feature-1.audit.lewp") {
		t.Fatalf("body=%q", string(body))
	}
}

func TestServeCleanShutdownReturnsNil(t *testing.T) {
	registryPath := t.TempDir() + "/registry.sqlite"
	httpLn := listenLocal(t)
	httpsLn := listenLocal(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{
			RegistryPath:   registryPath,
			HTTPListeners:  []net.Listener{httpLn},
			HTTPSListeners: []net.Listener{httpsLn},
		})
	}()

	// Wait until the proxy is actually accepting connections before shutting down,
	// so the listeners are closed by ctx cancellation rather than never opened.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		c, err := net.Dial("tcp", httpLn.Addr().String())
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancellation")
	}
}

func TestServeShutdownLetsInFlightRequestFinish(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("finished"))
	})}
	upstreamLn := listenLocal(t)
	go func() { _ = upstream.Serve(upstreamLn) }()
	t.Cleanup(func() { _ = upstream.Close() })

	registryPath := t.TempDir() + "/registry.sqlite"
	store, err := registry.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Remember(context.Background(), identity.Result{
		Root:           "audit",
		Name:           "feature-1",
		NormalizedRoot: "audit",
		NormalizedName: "feature-1",
		Host:           "feature-1.audit.lewp",
		HostKind:       identity.HostKindInstance,
		HostSource:     identity.SourceInferred,
		Path:           t.TempDir(),
		Kind:           identity.KindRoute,
	}, upstreamLn.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	httpLn := listenLocal(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{
			RegistryPath:  registryPath,
			HTTPListeners: []net.Listener{httpLn},
		})
	}()

	clientDone := make(chan string, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, "http://"+httpLn.Addr().String()+"/", nil)
		if err != nil {
			clientDone <- err.Error()
			return
		}
		req.Host = "feature-1.audit.lewp"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			clientDone <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		clientDone <- string(body)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach upstream")
	}
	cancel()
	close(release)

	select {
	case got := <-clientDone:
		if got != "finished" {
			t.Fatalf("client got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after request finished")
	}
}

// TestServeMintsForConfiguredTLSSuffix drives a real TLS handshake through the
// daemon's HTTPS listener for a custom safe-subtree host and checks the served
// leaf, proving the TLSSuffixes plumbing end to end.
func TestServeMintsForConfiguredTLSSuffix(t *testing.T) {
	dir := t.TempDir()
	ln := listenLocal(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{
			RegistryPath:    dir + "/registry.sqlite",
			HTTPSListeners:  []net.Listener{ln},
			ManagedSuffixes: []string{"lewp", "local.todoordie.com"},
			TLSSuffixes:     []string{"lewp", "local.todoordie.com"},
			CAPath:          dir + "/ca.pem",
			CAKeyPath:       dir + "/ca-key.pem",
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	}()

	var conn *tls.Conn
	var err error
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		conn, err = tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			ServerName:         "app.local.todoordie.com",
			InsecureSkipVerify: true,
		})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("handshake for custom-suffix host failed: %v", err)
	}
	defer conn.Close()
	leaf := conn.ConnectionState().PeerCertificates[0]
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "app.local.todoordie.com" {
		t.Fatalf("served leaf DNSNames=%v", leaf.DNSNames)
	}
}

func TestServeRequestLogRespectsLogAllRequests(t *testing.T) {
	for _, tc := range []struct {
		name        string
		logAll      bool
		wantSuccess bool
	}{
		{name: "errors-only default", logAll: false, wantSuccess: false},
		{name: "log all requests", logAll: true, wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})}
			upstreamLn := listenLocal(t)
			go func() { _ = upstream.Serve(upstreamLn) }()
			t.Cleanup(func() { _ = upstream.Close() })

			registryPath := t.TempDir() + "/registry.sqlite"
			store, err := registry.Open(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			err = store.Remember(context.Background(), identity.Result{
				Root:           "audit",
				Name:           "feature-1",
				NormalizedRoot: "audit",
				NormalizedName: "feature-1",
				Host:           "feature-1.audit.lewp",
				HostKind:       identity.HostKindInstance,
				HostSource:     identity.SourceInferred,
				Path:           t.TempDir(),
				Kind:           identity.KindRoute,
			}, upstreamLn.Addr().(*net.TCPAddr).Port)
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()

			httpLn := listenLocal(t)
			var logs lockedBuffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				_ = Serve(ctx, Config{
					RegistryPath:   registryPath,
					HTTPListeners:  []net.Listener{httpLn},
					RequestLog:     &logs,
					LogAllRequests: tc.logAll,
				})
			}()

			req, err := http.NewRequest(http.MethodGet, "http://"+httpLn.Addr().String()+"/", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "feature-1.audit.lewp"
			var resp *http.Response
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
				resp, err = http.DefaultClient.Do(req)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			logged := func() bool { return strings.Contains(logs.String(), "status=200") }
			if tc.wantSuccess {
				for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && !logged(); {
					time.Sleep(10 * time.Millisecond)
				}
				if !logged() {
					t.Fatalf("expected a success request line, got:\n%s", logs.String())
				}
			} else {
				// Give the daemon a moment to (not) log before asserting silence.
				time.Sleep(50 * time.Millisecond)
				if logged() {
					t.Fatalf("errors-only default logged a successful request:\n%s", logs.String())
				}
			}
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
