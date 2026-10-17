package proxy

import (
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/scottwater/lewp/internal/registry"
)

// serveStatic answers a request for a static route from route.StaticDir and
// returns the response status for request logging. Files are opened through an
// os.Root, so neither ".." segments nor symlinks can reach outside the
// directory. Dotfiles and dot-directories (.env, .git) are never served, even
// through a symlink, and directories are never listed: a directory serves its
// index.html or 404s.
func serveStatic(w http.ResponseWriter, r *http.Request, route registry.Record) (int, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return http.StatusMethodNotAllowed, nil
	}
	root, err := os.OpenRoot(route.StaticDir)
	if err != nil {
		writeStaticMissingPage(w, route, err)
		return http.StatusBadGateway, err
	}
	defer root.Close()

	clean := path.Clean("/" + r.URL.Path)
	if hasDotSegment(clean) {
		http.NotFound(w, r)
		return http.StatusNotFound, nil
	}
	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		name = "."
	}
	f, info, status, err := openStatic(root, name)
	if f == nil {
		writeStaticError(w, r, status)
		return status, err
	}
	if info.IsDir() {
		_ = f.Close()
		if !strings.HasSuffix(r.URL.Path, "/") {
			target := r.URL.Path + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return http.StatusMovedPermanently, nil
		}
		// Only an index.html that is itself a regular file is served; there is
		// no listing fallback, so a missing, renamed, or directory-shaped index
		// is simply not found.
		name = path.Join(name, "index.html")
		if f, info, status, err = openStatic(root, name); f == nil {
			writeStaticError(w, r, status)
			return status, err
		}
		if info.IsDir() {
			_ = f.Close()
			http.NotFound(w, r)
			return http.StatusNotFound, nil
		}
	}
	defer f.Close()
	// os.Root keeps symlinks inside the directory but still follows them, so a
	// harmless-looking name can point at .env or into .git. Check where the
	// name actually lands before serving it.
	if hidden, err := resolvesToHidden(route.StaticDir, name); hidden || err != nil {
		http.NotFound(w, r)
		return http.StatusNotFound, err
	}

	// Build output changes constantly during development, so make browsers
	// revalidate every time. ServeContent still answers revalidation with 304
	// from the file's modification time, so unchanged files are not re-sent.
	w.Header().Set("Cache-Control", "no-cache")
	rec := &statusRecorder{ResponseWriter: w}
	http.ServeContent(rec, r, path.Base(name), info.ModTime(), f)
	if !rec.wrote {
		rec.status = http.StatusOK
	}
	return rec.status, nil
}

// openStatic opens name inside root. On failure it returns a nil file and the
// status to answer with: 404 when nothing is there, 403 when the file exists
// but cannot be read or a symlink points outside the directory.
func openStatic(root *os.Root, name string) (*os.File, fs.FileInfo, int, error) {
	f, err := root.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, http.StatusNotFound, nil
		}
		return nil, nil, http.StatusForbidden, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, http.StatusForbidden, err
	}
	return f, info, 0, nil
}

func writeStaticError(w http.ResponseWriter, r *http.Request, status int) {
	if status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "cannot read file", status)
}

// resolvesToHidden reports whether name, once every symlink is followed, is a
// dotfile or sits inside a dot-directory of dir.
func resolvesToHidden(dir, name string) (bool, error) {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	realPath, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil {
		return false, err
	}
	return hasDotSegment(filepath.ToSlash(rel)), nil
}

func hasDotSegment(cleanPath string) bool {
	for _, segment := range strings.Split(cleanPath, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func writeStaticMissingPage(w http.ResponseWriter, route registry.Record, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)

	writePageHead(w, route.Host+" static directory is unavailable")
	fmt.Fprintf(w, "<h1>%s cannot serve its static directory</h1>", html.EscapeString(route.Host))
	fmt.Fprint(w, "<p class=\"sub\">Lewp serves this host from files on disk, but the directory could not be opened.</p>")

	fmt.Fprint(w, "<dl>")
	fmt.Fprintf(w, "<dt>Static directory</dt><dd><code>%s</code></dd>", html.EscapeString(route.StaticDir))
	fmt.Fprintf(w, "<dt>Error</dt><dd><code>%s</code></dd>", html.EscapeString(err.Error()))
	if route.MatchedHost != "" && route.MatchedHost != route.Host {
		fmt.Fprintf(w, "<dt>Matched route</dt><dd><code>%s</code></dd>", html.EscapeString(route.MatchedHost))
	}
	fmt.Fprintf(w, "<dt>Project</dt><dd><code>%s</code></dd>", html.EscapeString(route.Path))
	fmt.Fprint(w, "</dl>")

	fmt.Fprint(w, "<h2>Build or re-point the directory</h2>")
	fmt.Fprint(w, "<p>Run your build so the directory exists, or point the route at a different one from the project folder:</p>")
	writeCommandBlock(w, "lewp lease --static <dir>")

	fmt.Fprintf(w, "<p class=\"hint\">Inspect routes with <code>lewp list</code> or diagnose with <code>lewp doctor</code>.</p>")
	fmt.Fprintf(w, "</main></body></html>")
}
