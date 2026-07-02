package launchd

import (
	"encoding/xml"
	"io"
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

func TestPlistDefaultsToBareDaemonSubcommand(t *testing.T) {
	got := Plist(Config{Label: "dev.lewp.daemon", Program: "/usr/local/bin/lewp"})
	if !strings.Contains(got, "<string>daemon</string>") {
		t.Fatalf("plist missing daemon subcommand:\n%s", got)
	}
	if strings.Contains(got, "--log-requests") {
		t.Fatalf("plist should not add daemon flags by default:\n%s", got)
	}
}

func TestPlistAppendsExtraDaemonArgs(t *testing.T) {
	got := Plist(Config{
		Label:           "dev.lewp.daemon",
		Program:         "/usr/local/bin/lewp",
		ExtraDaemonArgs: []string{"--log-requests=all"},
	})
	daemon := strings.Index(got, "<string>daemon</string>")
	arg := strings.Index(got, "<string>--log-requests=all</string>")
	if daemon < 0 || arg < 0 {
		t.Fatalf("plist missing daemon args:\n%s", got)
	}
	if arg < daemon {
		t.Fatalf("extra daemon args must follow the daemon subcommand:\n%s", got)
	}
}

func TestPlistKeepsDaemonAliveOnAbnormalExit(t *testing.T) {
	got := Plist(Config{Label: "dev.lewp.daemon", Program: "/usr/local/bin/lewp"})
	value, ok := plistDictBool(t, got, "KeepAlive", "SuccessfulExit")
	if !ok || value {
		t.Fatalf("SuccessfulExit must be false so clean stops are not relaunched:\n%s", got)
	}
}

func plistDictBool(t *testing.T, body, dictKey, boolKey string) (bool, bool) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false, false
		}
		if err != nil {
			t.Fatalf("decode plist: %v", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := dec.DecodeElement(&key, &start); err != nil {
			t.Fatalf("decode plist key: %v", err)
		}
		if key != dictKey {
			nextStart(t, dec)
			if err := dec.Skip(); err != nil {
				t.Fatalf("skip plist value: %v", err)
			}
			continue
		}
		value := nextStart(t, dec)
		if value.Name.Local != "dict" {
			return false, false
		}
		return plistBoolInCurrentDict(t, dec, boolKey)
	}
}

func plistBoolInCurrentDict(t *testing.T, dec *xml.Decoder, boolKey string) (bool, bool) {
	t.Helper()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false, false
		}
		if err != nil {
			t.Fatalf("decode plist dict: %v", err)
		}
		switch tok := tok.(type) {
		case xml.EndElement:
			if tok.Name.Local == "dict" {
				return false, false
			}
		case xml.StartElement:
			if tok.Name.Local != "key" {
				if err := dec.Skip(); err != nil {
					t.Fatalf("skip plist value: %v", err)
				}
				continue
			}
			var key string
			if err := dec.DecodeElement(&key, &tok); err != nil {
				t.Fatalf("decode plist key: %v", err)
			}
			value := nextStart(t, dec)
			if key != boolKey {
				if err := dec.Skip(); err != nil {
					t.Fatalf("skip plist value: %v", err)
				}
				continue
			}
			switch value.Name.Local {
			case "false":
				return false, true
			case "true":
				return true, true
			default:
				return false, false
			}
		}
	}
}

func nextStart(t *testing.T, dec *xml.Decoder) xml.StartElement {
	t.Helper()
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("decode plist value: %v", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start
		}
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

func TestPlistDeclaresAllSocketActivationNames(t *testing.T) {
	got := Plist(Config{Label: "dev.lewp.daemon", Program: "/usr/local/bin/lewp"})
	for _, name := range SocketNames {
		if !strings.Contains(got, "<key>"+name+"</key>") {
			t.Fatalf("plist missing socket %q:\n%s", name, got)
		}
	}
}

func TestReadProgramRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.lewp.daemon.plist")
	if err := WritePlist(path, Config{Label: "dev.lewp.daemon", Program: "/opt/homebrew/bin/lewp"}); err != nil {
		t.Fatal(err)
	}
	program, err := ReadProgram(path)
	if err != nil {
		t.Fatal(err)
	}
	if program != "/opt/homebrew/bin/lewp" {
		t.Fatalf("program=%q", program)
	}
}

func TestReadProgramUnescapesXML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.lewp.daemon.plist")
	if err := WritePlist(path, Config{Label: "dev.lewp.daemon", Program: "/tmp/a&b/lewp"}); err != nil {
		t.Fatal(err)
	}
	program, err := ReadProgram(path)
	if err != nil {
		t.Fatal(err)
	}
	if program != "/tmp/a&b/lewp" {
		t.Fatalf("program=%q", program)
	}
}

func TestPlanPrint(t *testing.T) {
	got, err := Plan("print", Config{Label: "dev.lewp.daemon", PlistPath: "/tmp/x.plist"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "launchctl print gui/") || !strings.HasSuffix(joined, "/dev.lewp.daemon") {
		t.Fatalf("print plan=%v", got)
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
