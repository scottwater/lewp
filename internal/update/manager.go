package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	repoOwner      = "scottwater"
	repoName       = "lewp"
	binaryName     = "lewp"
	requestTimeout = 20 * time.Second
)

type Manager struct {
	client     *http.Client
	now        func() time.Time
	cacheDir   func() (string, error)
	executable func() (string, error)
	goos       string
	goarch     string
}

type releaseResponse struct {
	TagName string `json:"tag_name"`
}

type UpgradeResult struct {
	CurrentVersion string
	LatestVersion  string
	ExecutablePath string
	UpToDate       bool
}

func NewManager() *Manager {
	return &Manager{
		client:     &http.Client{Timeout: requestTimeout},
		now:        time.Now,
		cacheDir:   os.UserCacheDir,
		executable: os.Executable,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
	}
}

func (m *Manager) Upgrade(ctx context.Context, currentVersion string) (UpgradeResult, error) {
	if !supportedPlatform(m.goos, m.goarch) {
		return UpgradeResult{}, fmt.Errorf("upgrade is unavailable on %s/%s", m.goos, m.goarch)
	}
	latest, err := m.fetchLatestVersion(ctx)
	if err != nil {
		return UpgradeResult{}, err
	}
	path, err := m.targetExecutablePath()
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("resolve executable path: %w", err)
	}
	result := UpgradeResult{
		CurrentVersion: currentVersion,
		LatestVersion:  latest,
		ExecutablePath: path,
		UpToDate:       compareVersions(latest, currentVersion) <= 0,
	}
	if result.UpToDate {
		return result, nil
	}
	if err := m.replaceExecutable(ctx, path, latest); err != nil {
		return UpgradeResult{}, err
	}
	_ = m.writeCache(cacheState{CheckedAt: m.now(), LatestVersion: latest})
	return result, nil
}

func (m *Manager) fetchLatestVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("fetch latest release: GitHub returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var release releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	if normalizeVersion(release.TagName) == "" {
		return "", fmt.Errorf("latest release is missing tag_name")
	}
	return release.TagName, nil
}

func (m *Manager) replaceExecutable(ctx context.Context, targetPath, version string) error {
	downloadURL := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s_%s_%s.tar.gz", repoOwner, repoName, version, binaryName, m.goos, m.goarch)
	tmpDir, err := os.MkdirTemp(filepath.Dir(targetPath), ".lewp-upgrade-")
	if err != nil {
		return fmt.Errorf("create temporary upgrade directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	mode := os.FileMode(0o755)
	if info, err := os.Stat(targetPath); err == nil {
		mode = info.Mode().Perm()
	}
	tmpBinary := filepath.Join(tmpDir, binaryName)
	if err := m.downloadBinary(ctx, downloadURL, tmpBinary, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpBinary, targetPath); err != nil {
		return fmt.Errorf("replace executable at %s: %w", targetPath, err)
	}
	return nil
}

func (m *Manager) downloadBinary(ctx context.Context, url, targetPath string, mode os.FileMode) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("download release archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("download release archive: GitHub returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		if header.FileInfo().IsDir() || filepath.Base(header.Name) != binaryName {
			continue
		}
		file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("create upgraded binary at %s: %w", targetPath, err)
		}
		if _, err := io.Copy(file, tr); err != nil {
			_ = file.Close()
			return fmt.Errorf("extract upgraded binary: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close upgraded binary: %w", err)
		}
		if err := os.Chmod(targetPath, mode); err != nil {
			return fmt.Errorf("chmod upgraded binary at %s: %w", targetPath, err)
		}
		return nil
	}
	return fmt.Errorf("release archive did not contain lewp binary")
}

func (m *Manager) writeCache(state cacheState) error {
	path, err := m.cachePath()
	if err != nil {
		return err
	}
	return saveCache(path, state)
}

func (m *Manager) cachePath() (string, error) {
	dir, err := m.cacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(dir, repoName, cacheFileName), nil
}

func (m *Manager) targetExecutablePath() (string, error) {
	path, err := m.executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return resolved, nil
	}
	return path, nil
}

func supportedPlatform(goos, goarch string) bool {
	if goos != "darwin" {
		return false
	}
	return goarch == "amd64" || goarch == "arm64"
}
