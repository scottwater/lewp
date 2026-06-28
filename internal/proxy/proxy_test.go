package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"feature-1.audit.lewp is registered but not responding", "127.0.0.1:", "lewp list", "lewp doctor"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type=%q", ct)
	}
	if !strings.Contains(body, "<style>") {
		t.Fatalf("debug page missing inline stylesheet:\n%s", body)
	}
}

func TestProxyClosedTargetPageIncludesStartCommandAndPath(t *testing.T) {
	store := openProxyStore(t)
	port := freePort(t)
	registerRoute(t, store, "feature-1.audit.lewp", port)
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	for _, want := range []string{
		"<dt>Project</dt>",
		"audit / feature-1",
		fmt.Sprintf("PORT=%d &lt;your dev command&gt;", port),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("debug page missing %q:\n%s", want, body)
		}
	}
	// The start command must stay framework-neutral: no assumed tooling.
	if strings.Contains(body, "bin/dev") || strings.Contains(body, "npm ") || strings.Contains(body, "rails ") {
		t.Fatalf("debug page leaks framework-specific start command:\n%s", body)
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

func TestProxyUnknownLewpHostShowsHelpfulPage(t *testing.T) {
	store := openProxyStore(t)
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://feature-2.audit.lewp/dashboard", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type=%q", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"no route registered",
		"feature-2.audit.lewp",
		"instance <code>feature-2</code>",
		"root <code>audit</code>",
		"lewp add",
		"lewp list",
		"lewp doctor",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestProxyUnknownConfiguredSuffixShowsLewpDebugPage(t *testing.T) {
	store := openProxyStore(t)
	handler := NewWithSuffixes(store, []string{"lewp", "local.todoordie.com"})

	req := httptest.NewRequest(http.MethodGet, "http://missing.local.todoordie.com/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "missing.local.todoordie.com is not registered with Lewp") {
		t.Fatalf("custom suffix did not get Lewp debug page:\n%s", body)
	}
}

func TestProxyUnknownLewpHostEscapesHost(t *testing.T) {
	store := openProxyStore(t)
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	req.Host = "<script>.audit.lewp"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("unescaped host in body:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected escaped host in body:\n%s", body)
	}
}

func TestProxyUnknownNonLewpHostReturnsGeneric404(t *testing.T) {
	store := openProxyStore(t)
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "no route registered") || strings.Contains(body, "lewp doctor") {
		t.Fatalf("non-.lewp host should get generic 404, got:\n%s", body)
	}
}

func TestProxyLogsRequestDetails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", port)
	handler := New(store)
	var logs bytes.Buffer
	handler.Logger = log.New(&logs, "", 0)

	req := httptest.NewRequest(http.MethodPost, "http://feature-1.audit.lewp/widgets", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	got := logs.String()
	for _, want := range []string{
		"host=feature-1.audit.lewp",
		"method=POST",
		"proto=http",
		"path=/widgets",
		"status=202",
		"target=127.0.0.1:" + fmt.Sprint(port),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("request log missing %q:\n%s", want, got)
		}
	}
}

func TestProxyLogsDefaultOKForHeaderlessResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", port)
	handler := New(store)
	var logs bytes.Buffer
	handler.Logger = log.New(&logs, "", 0)

	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/no-body", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := logs.String(); !strings.Contains(got, "status=200") {
		t.Fatalf("headerless response log should default to 200:\n%s", got)
	}
}

func TestProxyLogsClosedTargetError(t *testing.T) {
	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", freePort(t))
	handler := New(store)
	var logs bytes.Buffer
	handler.Logger = log.New(&logs, "", 0)

	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	got := logs.String()
	if !strings.Contains(got, "status=502") || !strings.Contains(got, "error=") {
		t.Fatalf("closed-target log missing status/error:\n%s", got)
	}
}

func TestProxyLoggingPreservesWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("upstream hijack: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if err := rw.Flush(); err != nil {
			t.Errorf("flush upgrade: %v", err)
			return
		}
		line, err := rw.ReadString('\n')
		if err != nil {
			t.Errorf("read tunneled data: %v", err)
			return
		}
		if line != "ping\n" {
			t.Errorf("tunneled line=%q", line)
			return
		}
		fmt.Fprint(rw, "pong\n")
		_ = rw.Flush()
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	registerRoute(t, store, "feature-1.audit.lewp", port)
	handler := New(store)
	var logs bytes.Buffer
	handler.Logger = log.New(&logs, "", 0)
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()
	proxyPort := proxyServer.Listener.Addr().(*net.TCPAddr).Port

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(proxyPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: feature-1.audit.lewp\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	status, err := rw.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101 Switching Protocols") {
		t.Fatalf("upgrade status=%q", status)
	}
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	fmt.Fprint(rw, "ping\n")
	if err := rw.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := rw.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "pong\n" {
		t.Fatalf("tunneled response=%q", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if strings.Contains(logs.String(), "status=101") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if logLine := logs.String(); !strings.Contains(logLine, "status=101") {
		t.Fatalf("upgrade log missing 101:\n%s", logLine)
	}
}

func TestProxyUnknownLewpHostLogsRequest(t *testing.T) {
	store := openProxyStore(t)
	handler := New(store)
	var logs bytes.Buffer
	handler.Logger = log.New(&logs, "", 0)

	req := httptest.NewRequest(http.MethodGet, "http://feature-2.audit.lewp/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	got := logs.String()
	if !strings.Contains(got, "host=feature-2.audit.lewp") || !strings.Contains(got, "status=404") {
		t.Fatalf("unknown-host log missing host/status:\n%s", got)
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
