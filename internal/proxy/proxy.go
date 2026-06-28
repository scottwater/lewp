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
		if strings.HasSuffix(host, ".lewp") {
			writeUnknownRoutePage(w, host)
			return
		}
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

// writeUnknownRoutePage renders a Lewp-branded 404 for a `.lewp` hostname that
// has no registered route. It explains the situation and gives concrete next
// steps, tailored to the host's labels where possible. It must not depend on the
// daemon or control socket — only on the parsed host.
func writeUnknownRoutePage(w http.ResponseWriter, host string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)

	esc := html.EscapeString(host)
	root, name := parseLewpHost(host)

	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>No Lewp route for %s</title>", esc)
	fmt.Fprint(w, "<h1>Lewp: no route registered</h1>")
	fmt.Fprintf(w, "<p>Nothing is registered for <code>%s</code>.</p>", esc)

	if name != "" && root != "" {
		fmt.Fprintf(w, "<p>This host looks like instance <code>%s</code> in root <code>%s</code>.</p>",
			html.EscapeString(name), html.EscapeString(root))
	} else if root != "" {
		fmt.Fprintf(w, "<p>This host looks like root <code>%s</code>.</p>", html.EscapeString(root))
	}

	fmt.Fprint(w, "<h2>Next steps</h2><ol>")
	fmt.Fprint(w, "<li>From the project folder you want this host to point at, run <code>lewp lease</code> to claim a port and hostname.</li>")
	fmt.Fprint(w, "<li>See what is currently registered: <code>lewp list</code>.</li>")
	fmt.Fprint(w, "<li>Check daemon, DNS, and TLS health: <code>lewp doctor</code>.</li>")
	fmt.Fprint(w, "</ol>")
	fmt.Fprint(w, "<p>Lewp routes traffic to developer-started processes; it does not start them for you.</p>")
}

// parseLewpHost splits a `<instance>.<root>.lewp` (or `<root>.lewp`) hostname
// into its root and instance labels. Either return value may be empty when the
// host does not match the expected shape.
func parseLewpHost(host string) (root, name string) {
	trimmed := strings.TrimSuffix(host, ".lewp")
	if trimmed == "" || trimmed == host {
		return "", ""
	}
	labels := strings.Split(trimmed, ".")
	switch len(labels) {
	case 1:
		return labels[0], ""
	default:
		// Last label before .lewp is the root; the rest is the instance.
		root = labels[len(labels)-1]
		name = strings.Join(labels[:len(labels)-1], ".")
		return root, name
	}
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
