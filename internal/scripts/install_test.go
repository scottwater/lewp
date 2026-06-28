package scripts

import (
	"os"
	"strings"
	"testing"
)

// TestInstallSelfChecksPathAndResolution guards that bin/install warns when the
// install dir is not on PATH and shows command -v / lewp version guidance, so a
// source install fails loudly instead of silently leaving `lewp` unrunnable or
// shadowed by a stale copy.
func TestInstallSelfChecksPathAndResolution(t *testing.T) {
	body, err := os.ReadFile("../../bin/install")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"is not on your PATH",
		"command -v lewp",
		"lewp version",
		"install_dir",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bin/install missing %q:\n%s", want, script)
		}
	}
	// The self-check guidance must stay on stderr so bin/reinstall still captures
	// only the installed path from stdout.
	if !strings.Contains(script, "is not on your PATH; add it so 'lewp' is runnable by name:\" >&2") {
		t.Fatalf("bin/install PATH warning should go to stderr:\n%s", script)
	}
}
