package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scottwater/lewp/internal/registry"
	"github.com/scottwater/lewp/internal/suffix"
)

type Proxy struct {
	store           *registry.Store
	lastSeen        map[int64]time.Time
	managedSuffixes []string
	mu              sync.Mutex
	// Logger, when set, receives one line per proxied request with the host,
	// method, scheme, upstream target, and resulting status (or error). It is
	// left nil in tests so request logging stays quiet unless asserted.
	Logger *log.Logger
}

func New(store *registry.Store) *Proxy {
	return NewWithSuffixes(store, []string{suffix.BuiltIn})
}

func NewWithSuffixes(store *registry.Store, managed []string) *Proxy {
	return &Proxy{
		store:           store,
		lastSeen:        map[int64]time.Time{},
		managedSuffixes: canonicalManagedSuffixes(managed),
	}
}

func canonicalManagedSuffixes(managed []string) []string {
	seen := map[string]struct{}{suffix.BuiltIn: {}}
	custom := make([]string, 0, len(managed))
	for _, raw := range managed {
		name, err := suffix.Normalize(raw)
		if err != nil {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		custom = append(custom, name)
	}
	sort.Strings(custom)
	return append([]string{suffix.BuiltIn}, custom...)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	route, ok, err := p.store.RouteByHost(r.Context(), host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		p.logRequest(r, host, "", http.StatusInternalServerError, err)
		return
	}
	if !ok {
		if proxyHostInManagedSuffix(host, p.managedSuffixes) {
			writeUnknownRoutePage(w, host)
			p.logRequest(r, host, "", http.StatusNotFound, errors.New("no route registered"))
			return
		}
		http.NotFound(w, r)
		p.logRequest(r, host, "", http.StatusNotFound, nil)
		return
	}
	p.touch(r.Context(), route.LeaseID)
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", fmt.Sprint(route.Port))}
	var proxyErr error
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
			proxyErr = err
			writeDebugPage(w, r, route)
		},
	}
	if p.Logger == nil {
		proxy.ServeHTTP(w, r)
		return
	}
	rec := &statusRecorder{ResponseWriter: w}
	proxy.ServeHTTP(rec, r)
	if !rec.wrote {
		rec.status = http.StatusOK
	}
	p.logRequest(r, host, target.Host, rec.status, proxyErr)
}

func proxyHostInManagedSuffix(host string, managed []string) bool {
	if suffix.HostInManagedSuffix(host, managed) {
		return true
	}
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, raw := range managed {
		s, err := suffix.Normalize(raw)
		if err != nil {
			continue
		}
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// logRequest emits a single structured request line when a logger is attached.
// It never panics on a missing logger so callers can invoke it unconditionally.
func (p *Proxy) logRequest(r *http.Request, host, target string, status int, err error) {
	if p.Logger == nil {
		return
	}
	fields := fmt.Sprintf("request host=%s method=%s proto=%s path=%s status=%d",
		host, r.Method, scheme(r), r.URL.Path, status)
	if target != "" {
		fields += " target=" + target
	}
	if err != nil {
		fields += " error=" + strconv.Quote(err.Error())
	}
	p.Logger.Println(fields)
}

// statusRecorder wraps an http.ResponseWriter to remember the status code while
// forwarding Flush and Hijack so SSE streaming and WebSocket upgrades keep
// working through the proxy.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		if !s.wrote {
			s.status = http.StatusSwitchingProtocols
			s.wrote = true
		}
		return h.Hijack()
	}
	return nil, nil, errors.New("proxy: underlying ResponseWriter does not support hijacking")
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

// pageCSS is a small, framework-neutral stylesheet shared by the proxy error
// pages. It is intentionally lightweight (no external assets, no web fonts) so
// the pages render instantly and read well in both light and dark mode.
const pageCSS = `:root{color-scheme:light dark}
*{box-sizing:border-box}
body{margin:0;padding:3rem 1.25rem;font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;color:#1d1d1f;background:#f5f5f7}
main{max-width:42rem;margin:0 auto}
h1{font-size:1.4rem;margin:0 0 .35rem}
h2{font-size:1rem;margin:1.75rem 0 .5rem}
.sub{color:#6e6e73;margin:0 0 1.5rem}
dl{display:grid;grid-template-columns:max-content 1fr;gap:.4rem 1.25rem;margin:1.5rem 0}
dt{color:#6e6e73}
dd{margin:0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:rgba(127,127,127,.16);padding:.1rem .35rem;border-radius:.3rem}
.cmd{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:#1d1d1f;color:#f5f5f7;padding:.85rem 1rem;border-radius:.6rem;overflow-x:auto;margin:.6rem 0 0;user-select:all}
.hint{color:#6e6e73;font-size:.9rem;margin-top:1.5rem}
ol{padding-left:1.25rem}
li{margin:.25rem 0}
a{color:#0a84ff}
@media(prefers-color-scheme:dark){body{color:#f5f5f7;background:#1d1d1f}.cmd{background:#000;border:1px solid #333}}`

// writePageHead emits the doctype, head, and opening <main> shared by the proxy
// error pages. The pages stay deliberately script-free so a crafted hostname
// can never smuggle executable markup past the HTML escaping.
func writePageHead(w http.ResponseWriter, title string) {
	fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">"+
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">"+
		"<title>%s</title><style>%s</style></head><body><main>", html.EscapeString(title), pageCSS)
}

// writeCommandBlock renders a copyable, framework-neutral command block. The
// block uses `user-select:all` so a single click selects the whole command for
// copying, without needing any JavaScript.
func writeCommandBlock(w http.ResponseWriter, command string) {
	fmt.Fprintf(w, "<pre class=\"cmd\">%s</pre>", html.EscapeString(command))
}

func writeDebugPage(w http.ResponseWriter, r *http.Request, route registry.Record) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	target := "127.0.0.1:" + fmt.Sprint(route.Port)

	writePageHead(w, route.Host+" is not responding")
	fmt.Fprintf(w, "<h1>%s is registered but not responding</h1>", html.EscapeString(route.Host))
	fmt.Fprint(w, "<p class=\"sub\">Lewp has a route for this host, but nothing is listening on its loopback port yet.</p>")

	fmt.Fprint(w, "<dl>")
	fmt.Fprintf(w, "<dt>Target</dt><dd><code>%s</code></dd>", html.EscapeString(target))
	fmt.Fprintf(w, "<dt>Project</dt><dd><code>%s</code></dd>", html.EscapeString(route.Path))
	fmt.Fprintf(w, "<dt>Root/name</dt><dd>%s / %s</dd>", html.EscapeString(route.Root), html.EscapeString(route.Name))
	fmt.Fprintf(w, "<dt>Last seen</dt><dd>%s</dd>", html.EscapeString(lastSeenOrNever(route.LastSeenAt)))
	fmt.Fprintf(w, "<dt>Release state</dt><dd>%s</dd>", html.EscapeString(route.State))
	fmt.Fprint(w, "</dl>")

	fmt.Fprint(w, "<h2>Start your app on the leased port</h2>")
	fmt.Fprint(w, "<p>Lewp routes traffic but never starts processes. Run your usual dev command with this port, then reload:</p>")
	writeCommandBlock(w, fmt.Sprintf("PORT=%d <your dev command>", route.Port))

	fmt.Fprintf(w, "<p class=\"hint\">Inspect routes with <code>lewp list</code> or diagnose with <code>lewp doctor</code>.</p>")
	fmt.Fprintf(w, "</main></body></html>")
}

// lastSeenOrNever keeps the debug page readable when a lease has never seen
// traffic (empty timestamp) by printing a word instead of a blank field.
func lastSeenOrNever(lastSeen string) string {
	if strings.TrimSpace(lastSeen) == "" {
		return "never"
	}
	return lastSeen
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

	writePageHead(w, "No Lewp route for "+host)
	fmt.Fprint(w, "<h1>Lewp: no route registered</h1>")
	fmt.Fprintf(w, "<p class=\"sub\">%s is not registered with Lewp.</p>", esc)

	if name != "" && root != "" {
		fmt.Fprintf(w, "<p>This host looks like instance <code>%s</code> in root <code>%s</code>.</p>",
			html.EscapeString(name), html.EscapeString(root))
	} else if root != "" {
		fmt.Fprintf(w, "<p>This host looks like root <code>%s</code>.</p>", html.EscapeString(root))
	}

	fmt.Fprint(w, "<h2>Next steps</h2>")
	fmt.Fprint(w, "<p>From the project folder you want this host to point at, claim a port and hostname:</p>")
	writeCommandBlock(w, "lewp add")
	fmt.Fprint(w, "<ol>")
	fmt.Fprint(w, "<li>See what is currently registered: <code>lewp list</code>.</li>")
	fmt.Fprint(w, "<li>Check daemon, DNS, and TLS health: <code>lewp doctor</code>.</li>")
	fmt.Fprint(w, "</ol>")
	fmt.Fprint(w, "<p class=\"hint\">Lewp routes traffic to developer-started processes; it does not start them for you.</p>")
	fmt.Fprintf(w, "</main></body></html>")
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
