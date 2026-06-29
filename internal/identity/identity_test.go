package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeLabel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"scott/feature/JIRA-123 Add SSO callback!", "jira-123-add-sso-callback"},
		{"---Feature__One---", "feature-one"},
		{"main", "main"},
	}

	for _, tt := range tests {
		got, warnings, err := NormalizeLabel(tt.in)
		if err != nil {
			t.Fatalf("NormalizeLabel(%q) error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizeLabel(%q)=%q want %q", tt.in, got, tt.want)
		}
		if tt.in != tt.want && len(warnings) == 0 {
			t.Fatalf("NormalizeLabel(%q) returned no warning for changed label", tt.in)
		}
	}
}

func TestNormalizeLabelRejectsEmpty(t *testing.T) {
	if _, _, err := NormalizeLabel("!!!"); err == nil {
		t.Fatal("NormalizeLabel accepted unusable label")
	}
}

func TestResolveUsesDiscoveryOrderAndHostOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".lewp.local.toml"), []byte("root = \"config-root\"\nname = \"config-name\"\nhost = \"config.lewp\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(Options{
		WorkDir: dir,
		Env: map[string]string{
			"LEWP_ROOT": "env-root",
			"LEWP_NAME": "env-name",
			"LEWP_HOST": "env.lewp",
		},
		Root: "cli-root",
		Name: "cli-name",
		Host: "cli.lewp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "cli-root" || got.Name != "cli-name" || got.Host != "cli.lewp" {
		t.Fatalf("Resolve did not prefer CLI: %+v", got)
	}
	if got.HostKind != HostKindCustom || got.HostSource != SourceCLI {
		t.Fatalf("unexpected host metadata: kind=%s source=%s", got.HostKind, got.HostSource)
	}
}

func TestResolveInfersPathAndDefaultHost(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Audit", "JIRA-123 Add SSO callback!")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(Options{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got.NormalizedRoot != "audit" {
		t.Fatalf("root=%q want audit", got.NormalizedRoot)
	}
	if got.NormalizedName != "jira-123-add-sso-callback" {
		t.Fatalf("name=%q", got.NormalizedName)
	}
	if got.Host != "jira-123-add-sso-callback.audit.lewp" {
		t.Fatalf("host=%q", got.Host)
	}
	if got.RootSource != SourceInferred || got.NameSource != SourceInferred {
		t.Fatalf("sources root=%s name=%s", got.RootSource, got.NameSource)
	}
}

func TestResolveInfersGitWorktree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outside", "worktree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(Options{
		WorkDir: dir,
		Git: &GitInfo{
			IsWorktree: true,
			MainRoot:   "/Users/scott/projects/audit",
			Branch:     "scott/feature/JIRA-123 Add SSO callback!",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "audit" || got.Name != "JIRA-123 Add SSO callback!" {
		t.Fatalf("unexpected git inference: root=%q name=%q", got.Root, got.Name)
	}
	if got.Host != "jira-123-add-sso-callback.audit.lewp" {
		t.Fatalf("host=%q", got.Host)
	}
}

func TestResolveRejectsUnknownConfigKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("root = \"audit\"\nnaem = \"typo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Options{WorkDir: dir})
	if err == nil {
		t.Fatal("Resolve accepted unknown config key")
	}
	if !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "naem") {
		t.Fatalf("error should name the unknown key: %v", err)
	}
}

func TestResolveReportsTOMLSyntaxError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("root = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Options{WorkDir: dir})
	if err == nil {
		t.Fatal("Resolve accepted malformed TOML")
	}
	if !strings.Contains(err.Error(), ConfigFileName) {
		t.Fatalf("error should reference the config file path: %v", err)
	}
}

func TestResolveReadsValidTOMLConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("root = \"audit\"\nname = \"feature-1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(Options{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "audit" || got.Name != "feature-1" {
		t.Fatalf("config not applied: %+v", got)
	}
	if got.RootSource != SourceConfig || got.NameSource != SourceConfig {
		t.Fatalf("sources root=%s name=%s want config", got.RootSource, got.NameSource)
	}
	if got.Host != "feature-1.audit.lewp" {
		t.Fatalf("host=%q", got.Host)
	}
}

func TestResolveIgnoreConfigSkipsLocalFile(t *testing.T) {
	dir := t.TempDir()
	// Both a value-bearing and a malformed config must be ignored entirely when
	// IgnoreConfig is set: no influence on values, no parse error.
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("root = \"old-root\"\nnaem = \"typo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(Options{WorkDir: dir, IgnoreConfig: true})
	if err != nil {
		t.Fatalf("IgnoreConfig should not read (or fail on) the config: %v", err)
	}
	if got.Root == "old-root" || got.RootSource == SourceConfig {
		t.Fatalf("config value leaked despite IgnoreConfig: %+v", got)
	}
	if got.RootSource != SourceInferred {
		t.Fatalf("root source=%s want inferred", got.RootSource)
	}
}

func TestResolvePortIdentityIgnoresRouteHostConfig(t *testing.T) {
	dir := t.TempDir()

	got, err := Resolve(Options{
		WorkDir: dir,
		Name:    "vite",
		Kind:    KindPort,
		Env: map[string]string{
			"LEWP_HOST": "not-a-lewp.example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindPort || got.Host != "" || got.HostKind != "" {
		t.Fatalf("port identity should not carry host metadata: %+v", got)
	}
}

func TestValidateHostAllowsConfiguredSuffix(t *testing.T) {
	managed := []string{"lewp", "local.todoordie.com"}
	if err := ValidateHostForSuffixes("feature.local.todoordie.com", managed); err != nil {
		t.Fatalf("configured suffix host rejected: %v", err)
	}
	if err := ValidateHostForSuffixes("feature.todoordie.com", managed); err == nil {
		t.Fatal("unconfigured suffix host accepted")
	}
}

func TestValidateHostAllowsExactManagedSuffix(t *testing.T) {
	managed := []string{"lewp", "local.todoordie.com", "localkickofflabs.com"}
	if err := ValidateHostForSuffixes("local.todoordie.com", managed); err != nil {
		t.Fatalf("exact nested managed suffix rejected: %v", err)
	}
	if err := ValidateHostForSuffixes("localkickofflabs.com", managed); err != nil {
		t.Fatalf("exact managed suffix rejected: %v", err)
	}
	if err := ValidateHostForSuffixes("feature.local.todoordie.com", managed); err != nil {
		t.Fatalf("host below nested managed suffix rejected: %v", err)
	}
}
