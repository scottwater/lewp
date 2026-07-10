package scripts

import (
	"os"
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
