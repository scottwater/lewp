package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptDownloadsLatestRelease(t *testing.T) {
	body, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		`REPO="scottwater/lewp"`,
		`BINARY="lewp"`,
		`go install github.com/scottwater/lewp/cmd/lewp@latest`,
		`https://api.github.com/repos/${REPO}/releases/latest`,
		`${BINARY}_${os}_${arch}.tar.gz`,
		`lewp setup`,
		`lewp system start`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("install.sh missing %q:\n%s", want, script)
		}
	}
}

func TestInstallScriptCleansUpTemporaryDirectory(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	testRoot := t.TempDir()
	fakeBin := filepath.Join(testRoot, "bin")
	installDir := filepath.Join(testRoot, "install")
	downloadDir := filepath.Join(testRoot, "download")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}

	writeExecutable := func(name, body string) {
		t.Helper()
		path := filepath.Join(fakeBin, name)
		if err := os.WriteFile(path, []byte("#!/bin/bash\nset -euo pipefail\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("uname", `
		if [[ "${1:-}" == "-s" ]]; then echo Darwin; else echo arm64; fi
	`)
	writeExecutable("mktemp", `
		mkdir -p "${LEWP_TEST_DOWNLOAD_DIR}"
		echo "${LEWP_TEST_DOWNLOAD_DIR}"
	`)
	writeExecutable("curl", `
		output=""
		while (($#)); do
			if [[ "$1" == "-o" ]]; then output="$2"; shift 2; else shift; fi
		done
		if [[ -n "$output" ]]; then
			: > "$output"
		else
			echo '{"tag_name":"v0.1.0"}'
		fi
	`)
	writeExecutable("tar", `
		destination=""
		while (($#)); do
			if [[ "$1" == "-C" ]]; then destination="$2"; shift 2; else shift; fi
		done
		printf '#!/bin/bash\n' > "${destination}/lewp"
		chmod 0755 "${destination}/lewp"
	`)

	cmd := exec.Command("/bin/bash", filepath.Join(repoRoot, "install.sh"))
	cmd.Env = append(os.Environ(),
		"HOME="+testRoot,
		"LEWP_INSTALL_DIR="+installDir,
		"LEWP_TEST_DOWNLOAD_DIR="+downloadDir,
		"PATH="+fakeBin+":/usr/bin:/bin",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "unbound variable") {
		t.Fatalf("install.sh reported a cleanup error:\n%s", output)
	}
	if _, err := os.Stat(downloadDir); !os.IsNotExist(err) {
		t.Fatalf("temporary download directory still exists: %s", downloadDir)
	}
}

func TestReleaseWorkflowBuildsGoReleaserArtifacts(t *testing.T) {
	releaseWorkflow, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(releaseWorkflow)
	for _, want := range []string{
		`tags:`,
		`go-version: stable`,
		`go test ./...`,
		`goreleaser/goreleaser-action@v6`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("release workflow missing %q:\n%s", want, workflow)
		}
	}

	goreleaserConfig, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	config := string(goreleaserConfig)
	for _, want := range []string{
		`main: ./cmd/lewp`,
		`binary: lewp`,
		`darwin`,
		`amd64`,
		`arm64`,
		`github.com/scottwater/lewp/internal/buildinfo.Version={{.Version}}`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf(".goreleaser.yaml missing %q:\n%s", want, config)
		}
	}
}

func TestCIWorkflowRunsGoTests(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, want := range []string{
		`name: CI`,
		`pull_request:`,
		`branches:`,
		`macos-latest`,
		`go-version: stable`,
		`go test ./...`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("CI workflow missing %q:\n%s", want, workflow)
		}
	}
}
