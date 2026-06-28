package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/launchd"
	"github.com/scottwater/lewp/internal/suffix"
)

func TestRunAddReportsDaemonNotRunning(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:       []string{"add"},
		WorkDir:    t.TempDir(),
		SocketPath: t.TempDir() + "/missing.sock",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code == 0 {
		t.Fatal("add succeeded without daemon")
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
		PlistPath: writeTempPlist(t),
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
		PlistPath: writeTempPlist(t),
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
	plistPath := dir + "/dev.lewp.daemon.plist"
	if err := os.WriteFile(plistPath, []byte("dev.lewp.daemon"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:      []string{"system", "start"},
		WorkDir:   t.TempDir(),
		PlistPath: plistPath,
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

// writeTempPlist creates a minimal Lewp-owned plist so `system start` clears its
// missing-setup preflight in tests that only care about the launchctl plan.
func writeTempPlist(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/dev.lewp.daemon.plist"
	if err := os.WriteFile(path, []byte("dev.lewp.daemon"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func findCheck(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, checks)
	return doctorCheck{}
}

func TestInstalledBinaryChecksDetectsMismatch(t *testing.T) {
	dir := t.TempDir()
	installed := dir + "/installed-lewp"
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := dir + "/dev.lewp.daemon.plist"
	if err := launchd.WritePlist(plistPath, launchd.Config{Label: "dev.lewp.daemon", Program: installed}); err != nil {
		t.Fatal(err)
	}
	checks := installedBinaryChecks(Config{
		ProgramPath: dir + "/current-lewp",
		PlistPath:   plistPath,
		LogDir:      dir + "/Logs",
		Version:     "1.2.3",
		RunCommandOutput: func(_ context.Context, _ []string) (string, error) {
			return "lewp version 0.9.0\n", nil
		},
	})
	prog := findCheck(t, checks, "installed program")
	if prog.Status != statusWarn || !strings.Contains(prog.Detail, "launchd runs "+installed) {
		t.Fatalf("missing binary mismatch: %+v", prog)
	}
	ver := findCheck(t, checks, "installed version")
	if ver.Status != statusWarn || !strings.Contains(ver.Detail, "0.9.0") {
		t.Fatalf("missing version mismatch: %+v", ver)
	}
	log := findCheck(t, checks, "daemon log")
	if !strings.Contains(log.Detail, dir+"/Logs/daemon.err.log") {
		t.Fatalf("missing daemon log path: %+v", log)
	}
}

func TestInstalledBinaryChecksMatches(t *testing.T) {
	dir := t.TempDir()
	installed := dir + "/lewp"
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := dir + "/dev.lewp.daemon.plist"
	if err := launchd.WritePlist(plistPath, launchd.Config{Label: "dev.lewp.daemon", Program: installed}); err != nil {
		t.Fatal(err)
	}
	checks := installedBinaryChecks(Config{
		ProgramPath: installed,
		PlistPath:   plistPath,
		LogDir:      dir + "/Logs",
		Version:     "1.2.3",
		RunCommandOutput: func(_ context.Context, _ []string) (string, error) {
			return "lewp version 1.2.3\n", nil
		},
	})
	prog := findCheck(t, checks, "installed program")
	if prog.Status != statusOK || !strings.Contains(prog.Detail, "matches this CLI") {
		t.Fatalf("expected match: %+v", prog)
	}
	ver := findCheck(t, checks, "installed version")
	if ver.Status != statusOK {
		t.Fatalf("unexpected version mismatch: %+v", ver)
	}
}

func TestInstalledBinaryChecksMissingPlist(t *testing.T) {
	dir := t.TempDir()
	checks := installedBinaryChecks(Config{
		ProgramPath:      dir + "/lewp",
		PlistPath:        dir + "/nope.plist",
		LogDir:           dir + "/Logs",
		RunCommandOutput: func(_ context.Context, _ []string) (string, error) { return "", nil },
	})
	// With no plist, installedBinaryChecks only reports the running CLI binary;
	// the missing plist itself is reported by setupArtifactChecks.
	if len(checks) != 1 || checks[0].Name != "cli binary" {
		t.Fatalf("expected only cli binary check, got %+v", checks)
	}
}

func TestDoctorKeychainCheckReflectsTrust(t *testing.T) {
	dir := t.TempDir()
	base := Config{CAPath: dir + "/ca.pem", CAKeyPath: dir + "/ca-key.pem", PlistPath: dir + "/none.plist"}

	var ran []string
	trusted := base
	trusted.RunCommand = func(_ context.Context, argv []string) error { ran = argv; return nil }
	kc := findCheck(t, setupArtifactChecks(trusted), "keychain trust")
	if kc.Status != statusOK {
		t.Fatalf("expected trusted keychain: %+v", kc)
	}
	if !strings.Contains(strings.Join(ran, " "), "security verify-cert -c "+dir+"/ca.pem -p ssl") {
		t.Fatalf("trust check did not run security command: %v", ran)
	}

	untrusted := base
	untrusted.RunCommand = func(_ context.Context, _ []string) error { return errors.New("not trusted") }
	kc = findCheck(t, setupArtifactChecks(untrusted), "keychain trust")
	if kc.Status != statusWarn || kc.Run != "lewp setup" {
		t.Fatalf("expected untrusted keychain warn: %+v", kc)
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
		Args:         []string{"doctor"},
		WorkDir:      dir,
		SocketPath:   dir + "/missing.sock",
		ProgramPath:  installed,
		PlistPath:    plistPath,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		LogDir:       dir + "/Logs",
		Version:      "1.2.3",
		Stdout:       &stdout,
		Stderr:       &stderr,
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
		"installed program: matches this CLI",
		"installed version: 1.2.3",
		"daemon log: " + dir + "/Logs/daemon.err.log",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, got)
		}
	}
}

// TestDoctorSurfacesBrowserTrustScope proves doctor explicitly states the V1
// browser-trust boundary (Safari/Chromium supported, Firefox/NSS not), so a
// Firefox HTTPS warning is explained rather than mysterious.
func TestDoctorSurfacesBrowserTrustScope(t *testing.T) {
	c := browserTrustCheck()
	if c.Status != statusOK {
		t.Fatalf("browser trust check should be informational, got %+v", c)
	}
	for _, want := range []string{"Firefox", "V1", "Chromium"} {
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("browser trust detail missing %q: %q", want, c.Detail)
		}
	}

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	Run(Config{
		Args:         []string{"doctor"},
		WorkDir:      dir,
		SocketPath:   dir + "/missing.sock",
		ProgramPath:  dir + "/lewp",
		PlistPath:    dir + "/nope.plist",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		LogDir:       dir + "/Logs",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand:   func(context.Context, []string) error { return errors.New("not trusted") },
	})
	if !strings.Contains(stdout.String(), "browser trust") || !strings.Contains(stdout.String(), "Firefox") {
		t.Fatalf("doctor output missing browser trust scope:\n%s", stdout.String())
	}
}

func TestRunDoctorJSONEmitsCheckArray(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:         []string{"doctor", "--json"},
		WorkDir:      dir,
		SocketPath:   dir + "/missing.sock",
		ProgramPath:  dir + "/lewp",
		PlistPath:    dir + "/nope.plist",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		LogDir:       dir + "/Logs",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand:   func(context.Context, []string) error { return errors.New("not trusted") },
	})
	if code != 1 {
		t.Fatalf("code=%d (expected fail from missing setup)", code)
	}
	var parsed struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("doctor --json not valid JSON: %v\n%s", err, stdout.String())
	}
	if parsed.OK {
		t.Fatalf("expected ok=false with missing setup: %s", stdout.String())
	}
	if len(parsed.Checks) == 0 {
		t.Fatalf("expected checks in JSON: %s", stdout.String())
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

func TestRunSetupAddsCustomSuffixResolver(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "local.todoordie.com"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	assertResolverPort(t, dir+"/resolver/lewp")
	assertResolverPort(t, dir+"/resolver/local.todoordie.com")
	cfg, err := suffix.Load(dir + "/config/suffixes.toml")
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	if want := []string{"local.todoordie.com"}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
	got := stdout.String()
	if !strings.Contains(got, "SUFFIX=local.todoordie.com") || !strings.Contains(got, "RESOLVER="+dir+"/resolver/local.todoordie.com") {
		t.Fatalf("setup output missing suffix info:\n%s", got)
	}
}

func TestRunSetupRejectsApexSuffix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "todoordie.com"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be below registrable domain") {
		t.Fatalf("stderr missing apex validation: %q", stderr.String())
	}
	if _, err := os.Stat(dir + "/config/suffixes.toml"); !os.IsNotExist(err) {
		t.Fatalf("suffix config should not be written: %v", err)
	}
	if _, err := os.Stat(dir + "/resolver/local.todoordie.com"); !os.IsNotExist(err) {
		t.Fatalf("custom resolver should not be written: %v", err)
	}
}

func TestRunSetupAdditiveSuffixConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	suffixesPath := dir + "/config/suffixes.toml"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []string{"local.old.com"}}); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "local.new.com"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: suffixesPath,
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	cfg, err := suffix.Load(suffixesPath)
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	if want := []string{"local.new.com", "local.old.com"}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
	assertResolverPort(t, dir+"/resolver/lewp")
	assertResolverPort(t, dir+"/resolver/local.new.com")
	assertResolverPort(t, dir+"/resolver/local.old.com")
}

func assertResolverPort(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resolver %s: %v", path, err)
	}
	if !strings.Contains(string(body), "port 15353") {
		t.Fatalf("%s has wrong resolver content:\n%s", path, string(body))
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

// TestRunSystemUninstallPrintsAffectedSummaryFirst proves uninstall prints the
// affected-file summary before any removal output, so the scope is visible up
// front rather than inferred from the trailing "removed X" lines.
func TestRunSystemUninstallPrintsAffectedSummaryFirst(t *testing.T) {
	var stdout bytes.Buffer
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
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := stdout.String()
	summaryAt := strings.Index(got, "uninstall will affect:")
	removedAt := strings.Index(got, "removed "+plistPath)
	if summaryAt < 0 {
		t.Fatalf("uninstall missing affected-file summary:\n%s", got)
	}
	if removedAt < 0 || summaryAt > removedAt {
		t.Fatalf("summary must precede removal output:\n%s", got)
	}
	for _, want := range []string{"launchd: bootout", "keychain: remove trust", "resolver: remove " + resolverPath} {
		if !strings.Contains(got, want) {
			t.Fatalf("uninstall summary missing %q:\n%s", want, got)
		}
	}
}

func TestRunSystemUninstallKeepsAndReportsCAFiles(t *testing.T) {
	var stdout bytes.Buffer
	dir := t.TempDir()
	plistPath := dir + "/dev.lewp.daemon.plist"
	resolverPath := dir + "/resolver/lewp"
	// The real default CA dir is "~/Library/Application Support/lewp", which
	// contains a space. Mirror that here so the test proves the safe-removal
	// guidance shell-quotes paths rather than emitting an unsafe `rm a b`.
	caDir := dir + "/Application Support/lewp"
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		t.Fatal(err)
	}
	caPath := caDir + "/ca.pem"
	caKeyPath := caDir + "/ca-key.pem"
	if err := os.WriteFile(plistPath, []byte("dev.lewp.daemon"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{caPath, caKeyPath} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
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
		CAPath:       caPath,
		CAKeyPath:    caKeyPath,
		Stdout:       &stdout,
		Stderr:       &bytes.Buffer{},
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	// Uninstall leaves the CA material on disk by default.
	if _, err := os.Stat(caPath); err != nil {
		t.Fatalf("CA cert should be retained: %v", err)
	}
	if _, err := os.Stat(caKeyPath); err != nil {
		t.Fatalf("CA key should be retained: %v", err)
	}
	got := stdout.String()
	// The retained-file list shows the raw paths, but the copy-paste removal
	// command must single-quote each path so the embedded space is safe.
	wantRm := "rm '" + caPath + "' '" + caKeyPath + "'"
	for _, want := range []string{"kept local CA files", caPath, caKeyPath, wantRm} {
		if !strings.Contains(got, want) {
			t.Fatalf("uninstall output missing %q:\n%s", want, got)
		}
	}
	// Guard against a regression to the unsafe unquoted form.
	if strings.Contains(got, "rm "+caPath+" ") {
		t.Fatalf("uninstall printed unsafe unquoted rm command:\n%s", got)
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
		for _, want := range []string{"Usage:", "add", "setup", "version", "command-specific help"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%v help missing %q:\n%s", args, want, got)
			}
		}
		if strings.Contains(got, "\n  lease") || strings.Contains(got, "lewp lease") || strings.Contains(got, "Alias for add") {
			t.Fatalf("%v help should not mention removed lease command:\n%s", args, got)
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

func TestRunLeaseCommandIsRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"lease"}, Stdout: &stdout, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "unknown command \"lease\"") || !strings.Contains(got, "lewp add") {
		t.Fatalf("stderr missing removed-command guidance: %q", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunCommandHelpPrintsCommandHelp(t *testing.T) {
	cases := map[string]string{
		"add":     "--shell",
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

func TestRunVersionPrintsOnlyVersionByDefault(t *testing.T) {
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
		if got != "lewp version 1.2.3\n" {
			t.Fatalf("%v version output=%q", args, got)
		}
	}
}

func TestRunVersionDetailedPrintsInjectedMetadata(t *testing.T) {
	for _, args := range [][]string{{"version", "--detailed"}, {"--version", "--detailed"}, {"-v", "--detailed"}} {
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
