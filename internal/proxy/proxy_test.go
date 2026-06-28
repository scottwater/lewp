package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/registry"
)

func TestProxyRoutesByFullHostAndPreservesHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "feature-1.audit.lewp" {
			t.Fatalf("Host=%q", r.Host)
		}
		if r.Header.Get("X-Forwarded-Host") != "feature-1.audit.lewp" {
			t.Fatalf("X-Forwarded-Host=%q", r.Header.Get("X-Forwarded-Host"))
		}
		if r.Header.Get("X-Forwarded-Proto") != "http" {
			t.Fatalf("X-Forwarded-Proto=%q", r.Header.Get("X-Forwarded-Proto"))
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", port)
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/widgets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("body=%q", rr.Body.String())
	}
}

func TestProxyClosedTargetShowsDebugPage(t *testing.T) {
	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", freePort(t))
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"feature-1.audit.lewp is registered but not responding", "Target: 127.0.0.1:", "lewp list", "lewp doctor"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestProxyPassesSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hello\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", port)
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/events", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type=%q", rr.Header().Get("Content-Type"))
	}
	if rr.Body.String() != "data: hello\n\n" {
		t.Fatalf("body=%q", rr.Body.String())
	}
}

func openProxyStore(t *testing.T) *registry.Store {
	t.Helper()
	store, err := registry.Open(t.TempDir() + "/registry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func registerRoute(t *testing.T, store *registry.Store, host string, port int) {
	t.Helper()
	ident := identity.Result{
		Root:           "audit",
		Name:           "feature-1",
		NormalizedRoot: "audit",
		NormalizedName: "feature-1",
		Host:           host,
		HostKind:       identity.HostKindInstance,
		HostSource:     identity.SourceInferred,
		Path:           t.TempDir(),
		Kind:           identity.KindRoute,
	}
	if err := store.Remember(context.Background(), ident, port); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
