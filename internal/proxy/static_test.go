package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/registry"
)

// staticFixture lays out a build directory shaped like a versioned JS package
// and registers feature-1.audit.lewp to serve it.
func staticFixture(t *testing.T) (*Proxy, *registry.Store, int64, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"3.0.0/kol.js":     "console.log('v7')",
		"3.0.0/kol.js.map": `{"version":3}`,
		"_build/kol.js":    "console.log('dev')",
		"site/index.html":  "<h1>site</h1>",
		"empty/.keep":      "",
		".env":             "SECRET=1",
		".git/config":      "[core]",
	}
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := openProxyStore(t)
	routeID := registerRoute(t, store, "feature-1.audit.lewp", freePort(t))
	if err := store.SetRouteStaticDir(context.Background(), routeID, dir); err != nil {
		t.Fatal(err)
	}
	return New(store), store, routeID, dir
}

func staticGet(t *testing.T, handler http.Handler, method, url string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(method, url, nil))
	return rr
}

func TestStaticRouteServesVersionedFiles(t *testing.T) {
	handler, _, _, _ := staticFixture(t)
	for _, scheme := range []string{"http", "https"} {
		rr := staticGet(t, handler, http.MethodGet, scheme+"://feature-1.audit.lewp/3.0.0/kol.js")
		if rr.Code != http.StatusOK || rr.Body.String() != "console.log('v7')" {
			t.Fatalf("%s status=%d body=%q", scheme, rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Fatalf("content-type=%q", ct)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("cache-control=%q", cc)
		}
	}
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/_build/kol.js")
	if rr.Code != http.StatusOK || rr.Body.String() != "console.log('dev')" {
		t.Fatalf("_build status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestStaticRouteServesAliasHosts(t *testing.T) {
	handler, store, routeID, _ := staticFixture(t)
	if _, _, err := store.AddRouteHost(context.Background(), routeID, "kickoffjs.lewp", registry.HostTypeAlias, ""); err != nil {
		t.Fatal(err)
	}
	rr := staticGet(t, handler, http.MethodGet, "https://kickoffjs.lewp/3.0.0/kol.js.map")
	if rr.Code != http.StatusOK || rr.Body.String() != `{"version":3}` {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestStaticRouteServesDirectoryIndex(t *testing.T) {
	handler, _, _, _ := staticFixture(t)
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/site/")
	if rr.Code != http.StatusOK || rr.Body.String() != "<h1>site</h1>" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestStaticRouteRefusesListingsDotfilesAndMissingFiles(t *testing.T) {
	handler, _, _, _ := staticFixture(t)
	for _, path := range []string{"/", "/3.0.0/", "/empty/", "/.env", "/.git/config", "/empty/.keep", "/nope.js", "/../outside"} {
		rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp"+path)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%q", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "SECRET") || strings.Contains(rr.Body.String(), "kol.js") {
			t.Fatalf("%s leaked contents: %q", path, rr.Body.String())
		}
	}
}

func TestStaticRouteDoesNotFollowSymlinksOutOfTheDirectory(t *testing.T) {
	handler, _, _, dir := staticFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/escape.txt")
	if rr.Code == http.StatusOK || strings.Contains(rr.Body.String(), "outside") {
		t.Fatalf("symlink escape served: status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestStaticRouteSupportsHeadAndRejectsWrites(t *testing.T) {
	handler, _, _, _ := staticFixture(t)
	rr := staticGet(t, handler, http.MethodHead, "http://feature-1.audit.lewp/3.0.0/kol.js")
	if rr.Code != http.StatusOK || rr.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", rr.Code, rr.Body.String())
	}
	rr = staticGet(t, handler, http.MethodPost, "http://feature-1.audit.lewp/3.0.0/kol.js")
	if rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", rr.Code, rr.Header().Get("Allow"))
	}
}

func TestStaticRouteMissingDirectoryShowsDebugPage(t *testing.T) {
	handler, _, _, dir := staticFixture(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/3.0.0/kol.js")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"cannot serve its static directory", dir, "lewp lease --static"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestStaticRouteRefusesSymlinksIntoDotfiles(t *testing.T) {
	handler, _, _, dir := staticFixture(t)
	if err := os.Symlink(".env", filepath.Join(dir, "public.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(dir, "repo")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/public.txt", "/repo/config"} {
		rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp"+path)
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "SECRET") || strings.Contains(rr.Body.String(), "[core]") {
			t.Fatalf("%s status=%d body=%q", path, rr.Code, rr.Body.String())
		}
	}

	// A symlink to an ordinary file inside the directory still works.
	if err := os.Symlink("3.0.0/kol.js", filepath.Join(dir, "latest.js")); err != nil {
		t.Fatal(err)
	}
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/latest.js")
	if rr.Code != http.StatusOK || rr.Body.String() != "console.log('v7')" {
		t.Fatalf("latest.js status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestStaticRouteNeverListsDirectoryShapedIndex(t *testing.T) {
	handler, _, _, dir := staticFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, "odd", "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "odd", "index.html", "inner.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/odd/")
	if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "inner.js") {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestStaticRouteRedirectsDirectoryWithoutTrailingSlash(t *testing.T) {
	handler, _, _, _ := staticFixture(t)
	rr := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/site?x=1")
	if rr.Code != http.StatusMovedPermanently || rr.Header().Get("Location") != "/site/?x=1" {
		t.Fatalf("status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestStaticRouteAnswersRevalidationWithNotModified(t *testing.T) {
	handler, _, _, _ := staticFixture(t)
	first := staticGet(t, handler, http.MethodGet, "http://feature-1.audit.lewp/3.0.0/kol.js")
	modified := first.Header().Get("Last-Modified")
	if modified == "" {
		t.Fatal("missing Last-Modified")
	}
	req := httptest.NewRequest(http.MethodGet, "http://feature-1.audit.lewp/3.0.0/kol.js", nil)
	req.Header.Set("If-Modified-Since", modified)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("revalidation status=%d", rr.Code)
	}
}
