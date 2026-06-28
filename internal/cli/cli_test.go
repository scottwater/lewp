package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/launchd"
)

func TestRunLeaseReportsDaemonNotRunning(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"lease"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code == 0 {
		t.Fatal("lease succeeded without daemon")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "lewp daemon is not running") || !strings.Contains(errOut, "Run: lewp system start") {
		t.Fatalf("stderr missing daemon guidance: %q", errOut)
	}
}

func TestRunSystemStartPrintsLaunchdPlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var ran []string
	code := Run(Config{
		Args:      []string{"system", "start"},
		WorkDir:   t.TempDir(),
		PlistPath: t.TempDir() + "/dev.lewp.daemon.plist",
		Stdout:    &stdout,
		Stderr:    &stderr,
		RunCommand: func(_ context.Context, argv []string) error {
			ran = argv
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "launchctl bootstrap gui/") || !strings.Contains(got, "dev.lewp.daemon.plist") {
		t.Fatalf("unexpected system start output: %q", got)
	}
	if !strings.Contains(strings.Join(ran, " "), "launchctl bootstrap") {
		t.Fatalf("system start did not execute launchctl: %v", ran)
	}
}

func TestRunSystemStartKickstartsLoadedJob(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var ran []string
	code := Run(Config{
		Args:      []string{"system", "start"},
		WorkDir:   t.TempDir(),
		PlistPath: t.TempDir() + "/dev.lewp.daemon.plist",
		Stdout:    &stdout,
		Stderr:    &stderr,
		RunCommand: func(_ context.Context, argv []string) error {
			ran = append(ran, strings.Join(argv, " "))
			if len(ran) == 1 {
				return errors.New("launchctl bootstrap gui/501 /tmp/dev.lewp.daemon.plist: exit status 5: Bootstrap failed: 5: Input/output error")
			}
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := strings.Join(ran, "\n"); !strings.Contains(got, "launchctl bootstrap") || !strings.Contains(got, "launchctl kickstart -k gui/") || strings.Contains(got, "gui/501 dev.lewp.daemon") {
		t.Fatalf("system start did not fallback to kickstart:\n%s", got)
	}
	if got := stdout.String(); !strings.Contains(got, "launchctl kickstart -k") {
		t.Fatalf("stdout missing fallback command: %q", got)
	}
}

func TestRunSystemStartHardFailureShowsDiagnostics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:      []string{"system", "start"},
		WorkDir:   t.TempDir(),
		PlistPath: dir + "/dev.lewp.daemon.plist",
		LogDir:    dir + "/Logs/lewp",
		Stdout:    &stdout,
		Stderr:    &stderr,
		RunCommand: func(_ context.Context, _ []string) error {
			return errors.New("launchctl bootstrap gui/501 ...: exit status 78: Bootstrap failed: 78: Function not implemented")
		},
		RunCommandOutput: func(_ context.Context, argv []string) (string, error) {
			if strings.Contains(strings.Join(argv, " "), "launchctl print") {
				return "state = exited\n  last exit code = 1\n", nil
			}
			return "", errors.New("unexpected")
		},
	})
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	got := stderr.String()
	for _, want := range []string{
		"command failed: launchctl bootstrap",
		"daemon.err.log",
		"launchctl print gui/",
		"last exit code = 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("hard-failure diagnostics missing %q:\n%s", want, got)
		}
	}
}

func TestInstalledDaemonChecksDetectsMismatch(t *testing.T) {
	dir := t.TempDir()
	installed := dir + "/installed-lewp"
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := dir + "/dev.lewp.daemon.plist"
	if err := launchd.WritePlist(plistPath, launchd.Config{Label: "dev.lewp.daemon", Program: installed}); err != nil {
		t.Fatal(err)
	}
	lines := installedDaemonChecks(Config{
		ProgramPath: dir + "/current-lewp",
		PlistPath:   plistPath,
		LogDir:      dir + "/Logs",
		Version:     "1.2.3",
		RunCommandOutput: func(_ context.Context, _ []string) (string, error) {
			return "lewp version 0.9.0\n", nil
		},
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "mismatch: launchd runs "+installed) {
		t.Fatalf("missing binary mismatch:\n%s", joined)
	}
	if !strings.Contains(joined, "installed version: 0.9.0") || !strings.Contains(joined, "version mismatch") {
		t.Fatalf("missing version mismatch:\n%s", joined)
	}
	if !strings.Contains(joined, "daemon log: "+dir+"/Logs/daemon.err.log") {
		t.Fatalf("missing daemon log path:\n%s", joined)
	}
}

func TestInstalledDaemonChecksMatches(t *testing.T) {
	dir := t.TempDir()
	installed := dir + "/lewp"
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := dir + "/dev.lewp.daemon.plist"
	if err := launchd.WritePlist(plistPath, launchd.Config{Label: "dev.lewp.daemon", Program: installed}); err != nil {
		t.Fatal(err)
	}
	lines := installedDaemonChecks(Config{
		ProgramPath: installed,
		PlistPath:   plistPath,
		LogDir:      dir + "/Logs",
		Version:     "1.2.3",
		RunCommandOutput: func(_ context.Context, _ []string) (string, error) {
			return "lewp version 1.2.3\n", nil
		},
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "installed program matches this CLI") {
		t.Fatalf("expected match line:\n%s", joined)
	}
	if strings.Contains(joined, "version mismatch") || strings.Contains(joined, "mismatch: launchd") {
		t.Fatalf("unexpected mismatch reported:\n%s", joined)
	}
}

func TestInstalledDaemonChecksMissingPlist(t *testing.T) {
	dir := t.TempDir()
	lines := installedDaemonChecks(Config{
		ProgramPath:      dir + "/lewp",
		PlistPath:        dir + "/nope.plist",
		LogDir:           dir + "/Logs",
		RunCommandOutput: func(_ context.Context, _ []string) (string, error) { return "", nil },
	})
	if !strings.Contains(strings.Join(lines, "\n"), "launchd plist: missing") {
		t.Fatalf("expected missing-plist line:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRunDoctorPrintsInstalledChecksWhenDaemonDown(t *testing.T) {
	dir := t.TempDir()
	installed := dir + "/lewp"
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := dir + "/dev.lewp.daemon.plist"
	if err := launchd.WritePlist(plistPath, launchd.Config{Label: "dev.lewp.daemon", Program: installed}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:        []string{"doctor"},
		SocketPath:  dir + "/missing.sock",
		ProgramPath: installed,
		PlistPath:   plistPath,
		LogDir:      dir + "/Logs",
		Version:     "1.2.3",
		Stdout:      &stdout,
		Stderr:      &stderr,
		RunCommand: func(context.Context, []string) error {
			return errors.New("not trusted")
		},
		RunCommandOutput: func(_ context.Context, argv []string) (string, error) {
			if len(argv) != 2 || argv[0] != installed || argv[1] != "version" {
				t.Fatalf("unexpected command: %v", argv)
			}
			return "lewp version 1.2.3\n", nil
		},
	})
	if code != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"daemon: not responding",
		"installed program matches this CLI",
		"installed version: 1.2.3",
		"daemon log: " + dir + "/Logs/daemon.err.log",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, got)
		}
	}
}

func TestRunSetupStatesHTTPSEnabled(t *testing.T) {
	var stdout bytes.Buffer
	dir := t.TempDir()
	var ran []string
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &bytes.Buffer{},
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand: func(_ context.Context, argv []string) error {
			ran = argv
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "HTTPS=enabled") || !strings.Contains(got, "LOGS="+dir+"/Logs/lewp") || !strings.Contains(got, "security add-trusted-cert") {
		t.Fatalf("setup output missing HTTPS state/trust command: %q", got)
	}
	if !strings.Contains(strings.Join(ran, " "), "security add-trusted-cert") {
		t.Fatalf("setup did not execute trust command: %v", ran)
	}
	if _, err := os.Stat(dir + "/ca.pem"); err != nil {
		t.Fatalf("CA cert not written: %v", err)
	}
	if _, err := os.Stat(dir + "/ca-key.pem"); err != nil {
		t.Fatalf("CA key not written: %v", err)
	}
	if _, err := os.Stat(dir + "/resolver/lewp"); err != nil {
		t.Fatalf("resolver file not written: %v", err)
	}
	if got, err := os.ReadFile(dir + "/LaunchAgents/dev.lewp.daemon.plist"); err != nil {
		t.Fatalf("launchd plist not written: %v", err)
	} else if !strings.Contains(string(got), "<string>/usr/local/bin/lewp</string>") ||
		!strings.Contains(string(got), "<string>"+dir+"/Logs/lewp/daemon.err.log</string>") {
		t.Fatalf("plist missing program path:\n%s", string(got))
	}
}

func TestRunSystemUninstallPrintsKeychainCleanup(t *testing.T) {
	var stdout bytes.Buffer
	var ran []string
	dir := t.TempDir()
	plistPath := dir + "/dev.lewp.daemon.plist"
	resolverPath := dir + "/resolver/lewp"
	if err := os.WriteFile(plistPath, []byte("dev.lewp.daemon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir+"/resolver", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolverPath, []byte("nameserver 127.0.0.1\nport 15353\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"system", "uninstall"},
		WorkDir:      t.TempDir(),
		PlistPath:    plistPath,
		ResolverPath: resolverPath,
		Stdout:       &stdout,
		Stderr:       &bytes.Buffer{},
		RunCommand: func(_ context.Context, argv []string) error {
			ran = append(ran, strings.Join(argv, " "))
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "security delete-certificate -c Lewp Local Development CA") {
		t.Fatalf("uninstall output missing keychain cleanup: %q", got)
	}
	if !strings.Contains(strings.Join(ran, "\n"), "launchctl bootout") {
		t.Fatalf("uninstall did not execute launchctl: %v", ran)
	}
	if !strings.Contains(strings.Join(ran, "\n"), "security delete-certificate") {
		t.Fatalf("uninstall did not execute keychain cleanup: %v", ran)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist not removed: %v", err)
	}
	if _, err := os.Stat(resolverPath); !os.IsNotExist(err) {
		t.Fatalf("resolver not removed: %v", err)
	}
}

func TestRunHelpVariantsPrintToStdout(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: args, Stdout: &stdout, Stderr: &stderr})
		if code != 0 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
		got := stdout.String()
		for _, want := range []string{"Usage:", "lease", "setup", "version", "command-specific help"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%v help missing %q:\n%s", args, want, got)
			}
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v wrote to stderr: %q", args, stderr.String())
		}
	}
}

func TestRunNoArgsPrintsHelpToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: nil, Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr missing help: %q", stderr.String())
	}
}

func TestRunUnknownCommandReturns2WithHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"bogus"}, Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "unknown command \"bogus\"") || !strings.Contains(got, "Usage:") {
		t.Fatalf("stderr missing unknown-command help: %q", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunCommandHelpPrintsCommandHelp(t *testing.T) {
	cases := map[string]string{
		"lease":   "--shell",
		"port":    "--name",
		"release": "--forget",
		"list":    "--all",
		"setup":   "sudo",
		"system":  "uninstall",
		"doctor":  "CA trust",
		"daemon":  "launchd",
	}
	for cmd, want := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(Config{Args: []string{cmd, "--help"}, Stdout: &stdout, Stderr: &stderr})
		if code != 0 {
			t.Fatalf("%s --help: code=%d stderr=%q", cmd, code, stderr.String())
		}
		got := stdout.String()
		if !strings.Contains(got, "lewp "+cmd) || !strings.Contains(got, want) {
			t.Fatalf("%s --help missing %q:\n%s", cmd, want, got)
		}
	}
}

func TestRunVersionPrintsInjectedMetadata(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		code := Run(Config{
			Args:      args,
			Version:   "1.2.3",
			Commit:    "abc1234",
			BuildDate: "2026-06-27T12:00:00Z",
			Stdout:    &stdout,
			Stderr:    &stderr,
		})
		if code != 0 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
		got := stdout.String()
		for _, want := range []string{"lewp version 1.2.3", "abc1234", "2026-06-27T12:00:00Z", "go:"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%v version missing %q:\n%s", args, want, got)
			}
		}
	}
}

func TestRunSetupPrintsSudoAndKeychainNotes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "sudo") || !strings.Contains(got, "install "+dir+"/resolver/lewp") {
		t.Fatalf("setup output missing sudo install note: %q", got)
	}
	if !strings.Contains(got, "trust the local development CA in your keychain") {
		t.Fatalf("setup output missing keychain trust note: %q", got)
	}
}

func TestRunSetupTrustFailureShowsExactCommandAndNextStep(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand: func(_ context.Context, argv []string) error {
			if len(argv) > 0 && strings.Contains(strings.Join(argv, " "), "add-trusted-cert") {
				return errors.New("user canceled")
			}
			return nil
		},
	})
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "trust CA:") || !strings.Contains(got, "Next: trust the CA manually") || !strings.Contains(got, "security add-trusted-cert") {
		t.Fatalf("trust failure missing exact command/next step: %q", got)
	}
}

func TestKeychainTrustLineChecksSecurity(t *testing.T) {
	var ran []string
	got := keychainTrustLine(context.Background(), "/tmp/lewp-ca.pem", func(_ context.Context, argv []string) error {
		ran = argv
		return nil
	})
	if got != "keychain: trusted" {
		t.Fatalf("line=%q", got)
	}
	if !strings.Contains(strings.Join(ran, " "), "security verify-cert -c /tmp/lewp-ca.pem -p ssl") {
		t.Fatalf("trust check did not run security command: %v", ran)
	}

	got = keychainTrustLine(context.Background(), "/tmp/lewp-ca.pem", func(_ context.Context, _ []string) error {
		return errors.New("not trusted")
	})
	if got != "keychain: not trusted (run lewp setup)" {
		t.Fatalf("line=%q", got)
	}
}
