package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpgradeReplacesExecutable(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	cacheDir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, binaryName)
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write old executable: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/scottwater/lewp/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v0.2.0"}`)
		case "/scottwater/lewp/releases/download/v0.2.0/lewp_darwin_arm64.tar.gz":
			writeArchive(t, w, "new-binary")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := testManager(now, cacheDir, server)
	manager.executable = func() (string, error) { return execPath, nil }

	result, err := manager.Upgrade(context.Background(), "0.1.0")
	if err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}
	if result.UpToDate {
		t.Fatalf("expected upgrade to run, got %#v", result)
	}
	if result.LatestVersion != "v0.2.0" {
		t.Fatalf("latest version = %q", result.LatestVersion)
	}
	data, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("read upgraded executable: %v", err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("upgraded executable = %q", string(data))
	}
}

func TestUpgradeNoopsWhenCurrentVersionIsLatest(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	cacheDir := t.TempDir()
	execPath := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(execPath, []byte("same"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.2.0"}`)
	}))
	defer server.Close()

	manager := testManager(now, cacheDir, server)
	manager.executable = func() (string, error) { return execPath, nil }

	result, err := manager.Upgrade(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}
	if !result.UpToDate {
		t.Fatalf("expected up-to-date result, got %#v", result)
	}
	data, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if string(data) != "same" {
		t.Fatalf("executable changed to %q", string(data))
	}
}

func testManager(now time.Time, cacheDir string, server *httptest.Server) *Manager {
	client := server.Client()
	client.Transport = rewriteTransport{base: client.Transport, serverURL: server.URL}
	return &Manager{
		client:     client,
		now:        func() time.Time { return now },
		cacheDir:   func() (string, error) { return cacheDir, nil },
		executable: os.Executable,
		goos:       "darwin",
		goarch:     "arm64",
	}
}

type rewriteTransport struct {
	base      http.RoundTripper
	serverURL string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := req.Body
	rewritten, err := http.NewRequestWithContext(req.Context(), req.Method, t.serverURL+req.URL.Path, body)
	if err != nil {
		return nil, err
	}
	rewritten.Header = req.Header.Clone()
	return t.base.RoundTrip(rewritten)
}

func writeArchive(t *testing.T, w http.ResponseWriter, contents string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte(contents)
	if err := tw.WriteHeader(&tar.Header{Name: "lewp", Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	w.Header().Set("Content-Type", "application/gzip")
	if _, err := w.Write(buf.Bytes()); err != nil && !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("write response: %v", err)
	}
}
