package proxy

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/scottwater/lewp/internal/registry"
)

type Proxy struct {
	store    *registry.Store
	lastSeen map[int64]time.Time
	mu       sync.Mutex
}

func New(store *registry.Store) *Proxy {
	return &Proxy{store: store, lastSeen: map[int64]time.Time{}}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	route, ok, err := p.store.RouteByHost(r.Context(), host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	p.touch(r.Context(), route.LeaseID)
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", fmt.Sprint(route.Port))}
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(target)
			out.SetXForwarded()
			out.Out.Host = out.In.Host
			out.Out.Header.Set("X-Forwarded-Host", out.In.Host)
			out.Out.Header.Set("X-Forwarded-Proto", scheme(out.In))
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeDebugPage(w, r, route)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (p *Proxy) touch(ctx context.Context, leaseID int64) {
	p.mu.Lock()
	last := p.lastSeen[leaseID]
	if time.Since(last) < 30*time.Second {
		p.mu.Unlock()
		return
	}
	p.lastSeen[leaseID] = time.Now()
	p.mu.Unlock()
	_ = p.store.TouchLastSeen(ctx, leaseID)
}

func writeDebugPage(w http.ResponseWriter, r *http.Request, route registry.Record) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	target := "127.0.0.1:" + fmt.Sprint(route.Port)
	fmt.Fprintf(w, "<!doctype html><title>%s down</title>", html.EscapeString(route.Host))
	fmt.Fprintf(w, "<h1>%s is registered but not responding</h1>", html.EscapeString(route.Host))
	fmt.Fprintf(w, "<p>Target: %s</p>", html.EscapeString(target))
	fmt.Fprintf(w, "<p>Project: %s</p>", html.EscapeString(route.Path))
	fmt.Fprintf(w, "<p>Root/name: %s/%s</p>", html.EscapeString(route.Root), html.EscapeString(route.Name))
	fmt.Fprintf(w, "<p>Last seen: %s</p>", html.EscapeString(route.LastSeenAt))
	fmt.Fprintf(w, "<p>Release state: %s</p>", html.EscapeString(route.State))
	fmt.Fprintf(w, "<p>Try: PORT=%d bin/dev</p>", route.Port)
	fmt.Fprint(w, "<p>Hints: lewp list; lewp doctor</p>")
}

func hostOnly(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
