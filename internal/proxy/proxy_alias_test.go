package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/registry"
)

func TestProxyRoutesExactAliasAndPreservesRequestedHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "tags.app.work.lewp" {
			t.Fatalf("Host=%q", r.Host)
		}
		_, _ = w.Write([]byte("alias ok"))
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	routeID := registerRoute(t, store, "app.work.lewp", port)
	if _, _, err := store.AddRouteHost(context.Background(), routeID, "tags.app.work.lewp", registry.HostTypeAlias, "cli"); err != nil {
		t.Fatal(err)
	}
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://tags.app.work.lewp/widgets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "alias ok" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestProxyRoutesWildcardAliasAndPreservesRequestedHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "leads.app.work.lewp" {
			t.Fatalf("Host=%q", r.Host)
		}
		_, _ = w.Write([]byte("wildcard ok"))
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port

	store := openProxyStore(t)
	routeID := registerRoute(t, store, "app.work.lewp", port)
	if _, _, err := store.AddRouteHost(context.Background(), routeID, "*.app.work.lewp", registry.HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://leads.app.work.lewp/widgets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "wildcard ok" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestProxyClosedWildcardAliasDebugPageIncludesMatchedRoute(t *testing.T) {
	store := openProxyStore(t)
	routeID := registerRoute(t, store, "app.work.lewp", freePort(t))
	if _, _, err := store.AddRouteHost(context.Background(), routeID, "*.app.work.lewp", registry.HostTypeWildcard, "cli"); err != nil {
		t.Fatal(err)
	}
	handler := New(store)

	req := httptest.NewRequest(http.MethodGet, "http://leads.app.work.lewp/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "<dt>Matched route</dt><dd><code>*.app.work.lewp</code></dd>") {
		t.Fatalf("debug page missing matched wildcard:\n%s", body)
	}
}
