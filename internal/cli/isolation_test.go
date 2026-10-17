package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(runWithIsolatedHome(m))
}

// Config fills omitted paths from HOME. Mocking RunCommand does not intercept
// direct filesystem operations, including uninstall's removal of suffixes.toml.
// Isolate every CLI test before any default can reach the developer's files.
func runWithIsolatedHome(m *testing.M) int {
	home, err := os.MkdirTemp("", "lewp-cli-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test home:", err)
		return 1
	}
	defer os.RemoveAll(home)
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, "set isolated test home:", err)
		return 1
	}
	return m.Run()
}

func TestUninstallTestsPreserveInheritedHome(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "Library", "Application Support", "lewp", "suffixes.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("[[suffixes]]\nname = \"local.example.com\"\nmode = \"safe-subtree\"\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// This existing uninstall test deliberately leaves SuffixesPath unset. Its
	// old behavior passed while deleting the inherited HOME's real config.
	cmd := exec.Command(testBinary, "-test.run=^TestRunSystemUninstallPrintsKeychainCleanup$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HOME="+home)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall subprocess: %v\n%s", err, output)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("uninstall test damaged inherited home: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("uninstall test changed inherited suffix config: %q", got)
	}
}
