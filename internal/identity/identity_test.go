package identity

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

	got, err := Resolve(context.Background(), Options{
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

	got, err := Resolve(context.Background(), Options{WorkDir: dir})
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

	got, err := Resolve(context.Background(), Options{
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

func TestResolveRejectsEmptyWorkDir(t *testing.T) {
	_, err := Resolve(context.Background(), Options{})
	if err == nil {
		t.Fatal("Resolve accepted an empty WorkDir instead of rejecting it")
	}
	if !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("error should mention the missing working directory: %v", err)
	}
}

func TestResolveRejectsUnknownConfigKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("root = \"audit\"\nnaem = \"typo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(context.Background(), Options{WorkDir: dir})
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
	_, err := Resolve(context.Background(), Options{WorkDir: dir})
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
	got, err := Resolve(context.Background(), Options{WorkDir: dir})
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
	got, err := Resolve(context.Background(), Options{WorkDir: dir, IgnoreConfig: true})
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

	got, err := Resolve(context.Background(), Options{
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

func TestResolveHonorsContextDeadlineForGitDetection(t *testing.T) {
	installHangingGit(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	dir := filepath.Join(t.TempDir(), "repo", "feature")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	got, err := Resolve(ctx, Options{WorkDir: dir})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Resolve returned error after git cancellation: %v", err)
	}
	if got.NormalizedRoot != "repo" || got.NormalizedName != "feature" {
		t.Fatalf("Resolve did not fall back to path inference after git cancellation: %+v", got)
	}
	assertPromptReturn(t, elapsed, "Resolve")
}

// TestGitOutputHonorsContextDeadline proves the git subprocess is bounded by
// the caller's context: a hung git (e.g. a dead network mount holding WorkDir)
// must be killed when the deadline passes instead of pinning the caller. It
// stubs a `git` on PATH that sleeps well past the deadline and asserts
// gitOutput returns promptly with no output.
func TestGitOutputHonorsContextDeadline(t *testing.T) {
	installHangingGit(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := gitOutput(ctx, t.TempDir(), "rev-parse", "--git-dir")
	elapsed := time.Since(start)

	if got != "" {
		t.Fatalf("gitOutput returned %q; want empty when git is killed by ctx", got)
	}
	assertPromptReturn(t, elapsed, "gitOutput")
}

// TestDetectGitHonorsCancelledContext confirms the cancellation is threaded all
// the way through detectGit (not just the leaf gitOutput), so Resolve's git
// inference cannot hang a control handler past its dispatch deadline.
func TestDetectGitHonorsCancelledContext(t *testing.T) {
	installHangingGit(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	got := detectGit(ctx, t.TempDir())
	elapsed := time.Since(start)

	if got != nil {
		t.Fatalf("detectGit returned %+v; want nil when ctx is cancelled", got)
	}
	assertPromptReturn(t, elapsed, "detectGit")
}

func installHangingGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stub for git")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func assertPromptReturn(t *testing.T, elapsed time.Duration, name string) {
	t.Helper()
	if elapsed > 5*time.Second {
		t.Fatalf("%s blocked %v; cancelled context was not honored", name, elapsed)
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
	if err := ValidateHostForSuffixes("lewp", []string{"lewp", "local.todoordie.com"}); err == nil {
		t.Fatal("bare built-in suffix accepted as route host")
	}
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

func TestValidateRouteHostPatternAcceptsExactAndWildcard(t *testing.T) {
	managed := []string{"lewp", "local.todoordie.com"}
	for _, host := range []string{
		"tags.app.lewp",
		"*.app.lewp",
		"tags.app.local.todoordie.com",
		"*.app.local.todoordie.com",
		"*.App.Local.TodoOrDie.Com.",
	} {
		if err := ValidateRouteHostPatternForSuffixes(host, managed); err != nil {
			t.Fatalf("ValidateRouteHostPatternForSuffixes(%q) error: %v", host, err)
		}
	}
}

func TestValidateRouteHostPatternAllowsCustomSuffixStartingWithLewp(t *testing.T) {
	managed := []string{"lewp", "lewp.example.com"}
	if err := ValidateRouteHostPatternForSuffixes("*.lewp.example.com", managed); err != nil {
		t.Fatalf("wildcard under custom lewp-prefixed suffix rejected: %v", err)
	}
}

func TestValidateRouteHostPatternRejectsBadWildcards(t *testing.T) {
	managed := []string{"lewp"}
	for _, host := range []string{"*", "*.", "*.*.app.lewp", "foo.*.app.lewp", "*.lewp", "*.bad.com", "*.app.lewp.."} {
		if err := ValidateRouteHostPatternForSuffixes(host, managed); err == nil {
			t.Fatalf("ValidateRouteHostPatternForSuffixes(%q) succeeded", host)
		}
	}
}

func TestValidateRouteHostPatternRejectsExactDoubleTrailingDot(t *testing.T) {
	if err := ValidateRouteHostPatternForSuffixes("tags.app.lewp..", []string{"lewp"}); err == nil {
		t.Fatal("exact host with double trailing dot accepted")
	}
}

func TestNormalizeHostPattern(t *testing.T) {
	if got := NormalizeHostPattern(" *.App.Lewp. "); got != "*.app.lewp" {
		t.Fatalf("NormalizeHostPattern=%q want *.app.lewp", got)
	}
}

func TestWildcardRouteHostMatchesOneLabel(t *testing.T) {
	if !WildcardRouteHostMatches("*.app.lewp", "tags.app.lewp") {
		t.Fatal("expected wildcard to match one label")
	}
	if !WildcardRouteHostMatches("*.App.Lewp.", "Tags.App.Lewp.") {
		t.Fatal("expected wildcard match to normalize host and pattern")
	}
	for _, host := range []string{"app.lewp", "foo.tags.app.lewp"} {
		if WildcardRouteHostMatches("*.app.lewp", host) {
			t.Fatalf("wildcard should not match %s", host)
		}
	}
	if WildcardRouteHostMatches("app.lewp", "tags.app.lewp") {
		t.Fatal("non-wildcard pattern matched host")
	}
}
