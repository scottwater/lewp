package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/dns"
	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/launchd"
	"github.com/scottwater/lewp/internal/suffix"
	localtls "github.com/scottwater/lewp/internal/tls"
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

func TestRunSetupCreatesPrivateLogDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	logDir := dir + "/Logs/lewp"
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      t.TempDir(),
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/suffixes.toml",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		LogDir:       logDir,
		ProgramPath:  dir + "/bin/lewp",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand:   func(context.Context, []string) error { return nil },
		RunCommandOutput: func(context.Context, []string) (string, error) {
			return dir + "/bin/lewp\n", nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("log dir mode=%#o, want 0700", got)
	}
}

func TestDoctorReportsCustomSuffixResolvers(t *testing.T) {
	dir := t.TempDir()
	suffixesPath := dir + "/suffixes.toml"
	resolverPath := dir + "/resolver/lewp"
	customResolverPath := dir + "/resolver/local.todoordie.com"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	if err := dns.WriteResolverFile(customResolverPath, dns.DefaultPort); err != nil {
		t.Fatal(err)
	}

	checks := suffixResolverChecks(Config{
		ResolverPath: resolverPath,
		SuffixesPath: suffixesPath,
	})
	got := findCheck(t, checks, "resolver local.todoordie.com")
	if got.Status != statusOK || !strings.Contains(got.Detail, customResolverPath) || !strings.Contains(got.Detail, string(suffix.ModeSafeSubtree)) {
		t.Fatalf("custom suffix resolver check=%+v", got)
	}
}

func TestDoctorChecksCustomSuffixDNS(t *testing.T) {
	dir := t.TempDir()
	suffixesPath := dir + "/suffixes.toml"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	var lookedUp []string
	checks := dnsResolutionChecks(Config{
		SuffixesPath: suffixesPath,
		LookupLewp: func(_ context.Context, _ string, host string) (net.IP, error) {
			lookedUp = append(lookedUp, host)
			return net.ParseIP("127.0.0.1"), nil
		},
	})
	if findCheck(t, checks, "DNS local.todoordie.com").Status != statusOK {
		t.Fatalf("custom suffix DNS check missing or not ok: %+v", checks)
	}
	if got := strings.Join(lookedUp, ","); got != "doctor.lewp,doctor.local.todoordie.com" {
		t.Fatalf("lookups=%q", got)
	}
}

// TestDoctorInferenceUsesConfiguredSuffixes proves doctor's current-folder
// inference honors configured custom suffixes the same way `lewp add` does: a
// directory whose .lewp.local.toml pins a custom-suffix host must resolve OK,
// not warn that the folder cannot infer a valid host.
func TestDoctorInferenceUsesConfiguredSuffixes(t *testing.T) {
	dir := t.TempDir()
	suffixesPath := dir + "/suffixes.toml"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/.lewp.local.toml", []byte("host = \"app.local.todoordie.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := inferenceCheck(Config{WorkDir: dir, SuffixesPath: suffixesPath})
	if check.Status != statusOK {
		t.Fatalf("current-folder check should be ok for a configured suffix, got %+v", check)
	}
	if !strings.Contains(check.Detail, "app.local.todoordie.com") {
		t.Fatalf("current-folder detail should name the configured host, got %q", check.Detail)
	}
}

func TestDoctorConflictUsesConfiguredSuffixes(t *testing.T) {
	dir := t.TempDir()
	suffixesPath := dir + "/suffixes.toml"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/.lewp.local.toml", []byte("host = \"app.local.todoordie.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := startFakeControlResponse(t, control.Response{Entries: []control.ListEntry{{
		Host:  "app.local.todoordie.com",
		Path:  dir + "-other",
		Kind:  identity.KindRoute,
		State: "down",
	}}})

	checks := conflictCheck(Config{WorkDir: dir, SuffixesPath: suffixesPath, SocketPath: socketPath})
	if len(checks) != 1 || checks[0].Name != "hostname conflict" || checks[0].Status != statusWarn {
		t.Fatalf("expected configured-suffix conflict warning, got %+v", checks)
	}
	if !strings.Contains(checks[0].Detail, "app.local.todoordie.com") {
		t.Fatalf("conflict detail should name the configured host, got %q", checks[0].Detail)
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
	if want := []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
	got := stdout.String()
	if !strings.Contains(got, "SUFFIX=local.todoordie.com") || !strings.Contains(got, "RESOLVER="+dir+"/resolver/local.todoordie.com") {
		t.Fatalf("setup output missing suffix info:\n%s", got)
	}
	if !strings.Contains(got, "suffix changes load when the daemon starts or kickstarts") {
		t.Fatalf("setup output missing suffix reload note:\n%s", got)
	}
}

func TestRunSetupAllowsDomainMirrorSuffix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "localkickofflabs.com", "--allow-domain-mirror"},
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
	assertResolverPort(t, dir+"/resolver/localkickofflabs.com")
	cfg, err := suffix.Load(dir + "/config/suffixes.toml")
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	if want := []suffix.Entry{{Name: "localkickofflabs.com", Mode: suffix.ModeDomainMirror}}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
	got := stdout.String()
	for _, want := range []string{
		"SUFFIX=localkickofflabs.com RESOLVER=" + dir + "/resolver/localkickofflabs.com",
		"# warning: domain mirror localkickofflabs.com shadows public DNS for this suffix and its subdomains on this Mac",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup output missing %q:\n%s", want, got)
		}
	}
}

func TestRunSetupAllowsWWWDomainMirrorSuffix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "www.localkickofflabs.com", "--allow-domain-mirror"},
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
	assertResolverPort(t, dir+"/resolver/www.localkickofflabs.com")
	cfg, err := suffix.Load(dir + "/config/suffixes.toml")
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	if want := []suffix.Entry{{Name: "www.localkickofflabs.com", Mode: suffix.ModeDomainMirror}}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
}

func TestRunSetupRejectsOldSuffixConfigFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	suffixesPath := dir + "/config/suffixes.toml"
	if err := os.MkdirAll(dir+"/config", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suffixesPath, []byte("suffixes = [\"local.old.com\"]\n"), 0o600); err != nil {
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
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "Next: remove " + suffixesPath + ", then re-run: lewp setup --suffix local.new.com"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr missing cleanup guidance %q:\n%s", want, stderr.String())
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
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{{Name: "local.old.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
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
	if want := []suffix.Entry{
		{Name: "local.new.com", Mode: suffix.ModeSafeSubtree},
		{Name: "local.old.com", Mode: suffix.ModeSafeSubtree},
	}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
	assertResolverPort(t, dir+"/resolver/lewp")
	assertResolverPort(t, dir+"/resolver/local.new.com")
	assertResolverPort(t, dir+"/resolver/local.old.com")
}

func TestRunSuffixListShowsBuiltInAndCustom(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	suffixesPath := dir + "/config/suffixes.toml"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{
		{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree},
		{Name: "todoordie.com", Mode: suffix.ModeDomainMirror},
	}}); err != nil {
		t.Fatal(err)
	}

	code := Run(Config{
		Args:         []string{"suffix", "list"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		SuffixesPath: suffixesPath,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "SUFFIX\tMODE\nlewp\tbuilt-in\nlocal.todoordie.com\tsafe-subtree\ntodoordie.com\tdomain-mirror\n"; got != want {
		t.Fatalf("stdout=%q want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunSuffixRemoveDeletesConfigAndResolver(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	suffixesPath := dir + "/config/suffixes.toml"
	resolverPath := dir + "/resolver/lewp"
	customResolverPath := dir + "/resolver/local.todoordie.com"
	if err := suffix.Save(suffixesPath, suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	if err := dns.WriteResolverFile(customResolverPath, dns.DefaultPort); err != nil {
		t.Fatal(err)
	}

	code := Run(Config{
		Args:         []string{"suffix", "remove", "local.todoordie.com"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		ResolverPath: resolverPath,
		SuffixesPath: suffixesPath,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(customResolverPath); !os.IsNotExist(err) {
		t.Fatalf("resolver should be removed: %v", err)
	}
	cfg, err := suffix.Load(suffixesPath)
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	if len(cfg.Suffixes) != 0 {
		t.Fatalf("suffixes=%v want empty", cfg.Suffixes)
	}
	got := stdout.String()
	if !strings.Contains(got, "removed suffix local.todoordie.com") || !strings.Contains(got, "removed resolver "+customResolverPath) {
		t.Fatalf("stdout missing removal details:\n%s", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
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

func TestSystemUninstallRemovesCustomSuffixResolvers(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/resolver", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir + "/resolver/lewp", dir + "/resolver/local.todoordie.com"} {
		if err := os.WriteFile(path, []byte("nameserver 127.0.0.1\nport 15353\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := suffix.Save(dir+"/suffixes.toml", suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:         []string{"system", "uninstall", "--yes"},
		WorkDir:      dir,
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand:   func(context.Context, []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(dir + "/resolver/local.todoordie.com"); !os.IsNotExist(err) {
		t.Fatalf("custom resolver still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(dir + "/suffixes.toml"); !os.IsNotExist(err) {
		t.Fatalf("suffix config still exists or stat failed: %v", err)
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

// TestRunSystemUninstallContinuesAfterLaunchctlFailure proves uninstall is
// best-effort: a failing launchctl bootout does not abort the run, so the
// keychain trust, plist, and resolver are still cleaned up, and the accumulated
// failure is reported with a non-zero exit code.
func TestRunSystemUninstallContinuesAfterLaunchctlFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
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
	if err := os.WriteFile(resolverPath, []byte(dns.ResolverFile(dns.DefaultPort)), 0o644); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"system", "uninstall"},
		WorkDir:      t.TempDir(),
		PlistPath:    plistPath,
		ResolverPath: resolverPath,
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand: func(_ context.Context, argv []string) error {
			ran = append(ran, strings.Join(argv, " "))
			if len(argv) > 0 && argv[0] == "launchctl" {
				return errors.New("Bootstrap failed: 125: Domain does not support specified action")
			}
			return nil
		},
	})
	if code != 1 {
		t.Fatalf("expected non-zero exit on launchctl failure, code=%d", code)
	}
	if !strings.Contains(stderr.String(), "uninstall:") {
		t.Fatalf("stderr missing accumulated launchctl failure: %q", stderr.String())
	}
	// The keychain untrust must still run despite the earlier launchctl failure.
	if !strings.Contains(strings.Join(ran, "\n"), "security delete-certificate") {
		t.Fatalf("uninstall stopped before keychain cleanup: %v", ran)
	}
	// Plist and resolver must still be removed (best-effort continues).
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist not removed after launchctl failure: %v", err)
	}
	if _, err := os.Stat(resolverPath); !os.IsNotExist(err) {
		t.Fatalf("resolver not removed after launchctl failure: %v", err)
	}
}

// TestRunSystemUninstallTreatsNotLoadedAsSuccess proves an already-unloaded
// launchd service is non-fatal for uninstall: the desired end state is already
// met, so the run exits 0 and still removes the remaining files.
func TestRunSystemUninstallTreatsNotLoadedAsSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	plistPath := dir + "/dev.lewp.daemon.plist"
	resolverPath := dir + "/resolver/lewp"
	if err := os.WriteFile(plistPath, []byte("dev.lewp.daemon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir+"/resolver", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolverPath, []byte(dns.ResolverFile(dns.DefaultPort)), 0o644); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"system", "uninstall"},
		WorkDir:      t.TempDir(),
		PlistPath:    plistPath,
		ResolverPath: resolverPath,
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand: func(_ context.Context, argv []string) error {
			if len(argv) > 0 && argv[0] == "launchctl" {
				return errors.New("Boot-out failed: 3: No such process")
			}
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("already-unloaded service should be non-fatal, code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "service already unloaded") {
		t.Fatalf("stdout missing already-unloaded note: %q", stdout.String())
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist not removed: %v", err)
	}
}

// TestIsDaemonDownClassification proves the daemon-down check keys off the dial
// failure (a *net.OpError with Op=="dial"), not on fragile error-text
// substrings, and does not misclassify post-dial or unrelated errors.
func TestIsDaemonDownClassification(t *testing.T) {
	// A real dial failure against a missing/oversized unix socket path.
	var d net.Dialer
	_, dialErr := d.DialContext(context.Background(), "unix", t.TempDir()+"/missing.sock")
	if dialErr == nil {
		t.Fatal("expected dial to fail against missing socket")
	}
	if !isDaemonDown(dialErr) {
		t.Fatalf("dial failure should classify as daemon-down: %v", dialErr)
	}
	// A daemon-side error string (post-dial) must not be classified as down,
	// even though it contains the word "connection".
	if isDaemonDown(errors.New("lease failed: connection to registry lost")) {
		t.Fatal("post-dial error text should not classify as daemon-down")
	}
	if isDaemonDown(context.DeadlineExceeded) {
		t.Fatal("request timeout should not classify as daemon-down")
	}
	if isDaemonDown(&net.OpError{Op: "dial", Net: "unix", Err: context.DeadlineExceeded}) {
		t.Fatal("dial timeout should not classify as daemon-down")
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

// TestRunSetupRotatesLegacyUnconstrainedCA: an existing CA without name
// constraints (pre-2026-07 install) must be untrusted, regenerated with
// constraints, and re-trusted by setup.
func TestRunSetupRotatesLegacyUnconstrainedCA(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	legacy, err := localtls.NewCA(localtls.CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	var commands []string
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      t.TempDir(),
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand: func(_ context.Context, argv []string) error {
			commands = append(commands, strings.Join(argv, " "))
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	rotated, err := localtls.LoadCA(dir+"/ca.pem", dir+"/ca-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.HasNameConstraints() {
		t.Fatal("setup left an unconstrained CA in place")
	}
	if rotated.Certificate.SerialNumber.Cmp(legacy.Certificate.SerialNumber) == 0 {
		t.Fatal("setup kept the legacy CA certificate")
	}
	joined := strings.Join(commands, "\n")
	untrust := strings.Index(joined, "security delete-certificate -c "+localtls.CACommonName)
	trust := strings.Index(joined, "security add-trusted-cert")
	if untrust == -1 || trust == -1 || untrust > trust {
		t.Fatalf("expected untrust before re-trust, got:\n%s", joined)
	}
	if got := stdout.String(); !strings.Contains(got, "rotating local CA") {
		t.Fatalf("setup output missing rotation notice:\n%s", got)
	}
}

// TestRunSetupRotatesCAWhenSuffixAdded: adding a safe-subtree suffix to an
// install whose CA only covers lewp must rotate the CA to cover the suffix.
func TestRunSetupRotatesCAWhenSuffixAdded(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	existing, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := existing.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "local.todoordie.com"},
		WorkDir:      t.TempDir(),
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
	rotated, err := localtls.LoadCA(dir+"/ca.pem", dir+"/ca-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if !localtls.ConstraintsMatch(rotated, []string{"lewp", "local.todoordie.com"}) {
		t.Fatalf("rotated CA constraints=%v", rotated.Certificate.PermittedDNSDomains)
	}
}

// TestRunSetupKeepsMatchingCA: a CA whose constraints already match must not
// be rotated (same serial before and after).
func TestRunSetupKeepsMatchingCA(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	existing, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := existing.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"setup"},
		WorkDir:      t.TempDir(),
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
	kept, err := localtls.LoadCA(dir+"/ca.pem", dir+"/ca-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Certificate.SerialNumber.Cmp(existing.Certificate.SerialNumber) != 0 {
		t.Fatal("setup rotated a CA whose constraints already matched")
	}
	if strings.Contains(stdout.String(), "rotating local CA") {
		t.Fatalf("unexpected rotation notice:\n%s", stdout.String())
	}
}

func TestSuffixRemovePrintsSetupHint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	if err := suffix.Save(dir+"/suffixes.toml", suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"suffix", "remove", "local.todoordie.com"},
		WorkDir:      t.TempDir(),
		Stdout:       &stdout,
		Stderr:       &stderr,
		SuffixesPath: dir + "/suffixes.toml",
		ResolverPath: dir + "/resolver/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Next: lewp setup") {
		t.Fatalf("suffix remove output missing setup hint:\n%s", stdout.String())
	}
}

func TestDoctorFailsUnconstrainedCA(t *testing.T) {
	dir := t.TempDir()
	legacy, err := localtls.NewCA(localtls.CACommonName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		PlistPath:    dir + "/none.plist",
		SuffixesPath: dir + "/suffixes.toml",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	}
	check := findCheck(t, setupArtifactChecks(cfg), "local CA")
	if check.Status != statusFail || check.Run != "lewp setup" {
		t.Fatalf("unconstrained CA check=%+v, want fail with lewp setup", check)
	}
	if !strings.Contains(check.Detail, "name constraints") {
		t.Fatalf("detail does not explain the problem: %q", check.Detail)
	}
}

func TestDoctorWarnsOnConstraintDrift(t *testing.T) {
	dir := t.TempDir()
	ca, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	if err := suffix.Save(dir+"/suffixes.toml", suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		PlistPath:    dir + "/none.plist",
		SuffixesPath: dir + "/suffixes.toml",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	}
	check := findCheck(t, setupArtifactChecks(cfg), "local CA")
	if check.Status != statusWarn || check.Run != "lewp setup" {
		t.Fatalf("drifted CA check=%+v, want warn with lewp setup", check)
	}
}

func TestDoctorPassesMatchingConstrainedCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := localtls.NewCA(localtls.CACommonName, []string{"lewp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(dir+"/ca.pem", dir+"/ca-key.pem"); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		PlistPath:    dir + "/none.plist",
		SuffixesPath: dir + "/suffixes.toml",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	}
	check := findCheck(t, setupArtifactChecks(cfg), "local CA")
	if check.Status != statusOK {
		t.Fatalf("matching CA check=%+v, want ok", check)
	}
}
