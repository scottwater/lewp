package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCompletionEmitsShellScripts(t *testing.T) {
	cases := map[string][]string{
		"bash": {"complete -F _lewp lewp", "_lewp()", "setup", "--shell --forget"},
		"zsh":  {"#compdef lewp", "compdef _lewp lewp", "doctor", "port subcommand/options"},
		"fish": {"complete -c lewp", "__fish_use_subcommand", "Release a bare port lease", "-l name"},
	}
	for shell, wants := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: []string{"completion", shell}, Stdout: &stdout, Stderr: &stderr})
		if code != 0 {
			t.Fatalf("completion %s: code=%d stderr=%q", shell, code, stderr.String())
		}
		got := stdout.String()
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Fatalf("completion %s missing %q:\n%s", shell, want, got)
			}
		}
		// Every top-level command must appear in each completion script.
		for _, c := range completionCommands {
			if !strings.Contains(got, c.name) {
				t.Fatalf("completion %s missing command %q:\n%s", shell, c.name, got)
			}
		}
	}
}

func TestRunCompletionMissingShellErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"completion"}, Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "a shell is required") {
		t.Fatalf("missing shell guidance: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty: %q", stdout.String())
	}
}

func TestRunCompletionUnsupportedShellErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"completion", "powershell"}, Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), `unsupported shell "powershell"`) {
		t.Fatalf("missing unsupported-shell guidance: %q", stderr.String())
	}
}

func TestRunCompletionHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"completion", "--help"}, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "lewp completion") || !strings.Contains(got, "bash|zsh|fish") {
		t.Fatalf("completion help missing content:\n%s", got)
	}
}
