package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
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

func TestKeychainTrustLineChecksSecurity(t *testing.T) {
	var ran []string
	got := keychainTrustLine(context.Background(), func(_ context.Context, argv []string) error {
		ran = argv
		return nil
	})
	if got != "keychain: trusted" {
		t.Fatalf("line=%q", got)
	}
	if !strings.Contains(strings.Join(ran, " "), "security find-certificate") {
		t.Fatalf("trust check did not run security command: %v", ran)
	}

	got = keychainTrustLine(context.Background(), func(_ context.Context, _ []string) error {
		return errors.New("missing")
	})
	if got != "keychain: not trusted (run lewp setup)" {
		t.Fatalf("line=%q", got)
	}
}
