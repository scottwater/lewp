package launchd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlistUsesLoopbackSocketsForHTTPAndHTTPS(t *testing.T) {
	got := Plist(Config{
		Label:      "dev.lewp.daemon",
		Program:    "/usr/local/bin/lewp",
		StdoutPath: "/Users/scott/Library/Logs/lewp/daemon.out.log",
		StderrPath: "/Users/scott/Library/Logs/lewp/daemon.err.log",
	})
	for _, want := range []string{
		"<key>Label</key>",
		"<string>dev.lewp.daemon</string>",
		"<string>/usr/local/bin/lewp</string>",
		"<key>RunAtLoad</key>",
		"<key>StandardOutPath</key>",
		"<string>/Users/scott/Library/Logs/lewp/daemon.out.log</string>",
		"<key>StandardErrorPath</key>",
		"<string>/Users/scott/Library/Logs/lewp/daemon.err.log</string>",
		"<string>127.0.0.1</string>",
		"<string>::1</string>",
		"<string>80</string>",
		"<string>443</string>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plist missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0.0.0.0") {
		t.Fatalf("plist exposes LAN:\n%s", got)
	}
}

func TestPlistEscapesXML(t *testing.T) {
	got := Plist(Config{Label: "dev.lewp.&", Program: "/tmp/a&b/lewp"})
	if !strings.Contains(got, "dev.lewp.&amp;") || !strings.Contains(got, "/tmp/a&amp;b/lewp") {
		t.Fatalf("plist did not escape XML:\n%s", got)
	}
}

func TestWritePlistCreatesLaunchAgentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "LaunchAgents", "dev.lewp.daemon.plist")
	if err := WritePlist(path, Config{Label: "dev.lewp.daemon", Program: "/usr/local/bin/lewp"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "<string>/usr/local/bin/lewp</string>") {
		t.Fatalf("plist missing program:\n%s", string(got))
	}
}

func TestPlanSystemCommands(t *testing.T) {
	cfg := Config{Label: "dev.lewp.daemon", PlistPath: "/tmp/dev.lewp.daemon.plist"}
	tests := map[string][]string{
		"start":     {"launchctl", "bootstrap", "gui/", "/tmp/dev.lewp.daemon.plist"},
		"stop":      {"launchctl", "bootout", "gui/", "/tmp/dev.lewp.daemon.plist"},
		"restart":   {"launchctl", "kickstart", "-k", "gui/"},
		"uninstall": {"launchctl", "bootout", "gui/", "/tmp/dev.lewp.daemon.plist"},
	}
	for action, prefix := range tests {
		got, err := Plan(action, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) < len(prefix) {
			t.Fatalf("%s plan too short: %v", action, got)
		}
		for i := range prefix {
			if prefix[i] == "gui/" {
				if !strings.HasPrefix(got[i], "gui/") {
					t.Fatalf("%s arg %d=%q want gui/<uid>", action, i, got[i])
				}
				if action == "restart" && !strings.HasSuffix(got[i], "/dev.lewp.daemon") {
					t.Fatalf("%s arg %d=%q want gui/<uid>/dev.lewp.daemon", action, i, got[i])
				}
				continue
			}
			if got[i] != prefix[i] {
				t.Fatalf("%s arg %d=%q want %q in %v", action, i, got[i], prefix[i], got)
			}
		}
	}
}
