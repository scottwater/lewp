package launchd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultLabel = "dev.lewp.daemon"

type Config struct {
	Label      string
	Program    string
	PlistPath  string
	StdoutPath string
	StderrPath string
	// ExtraDaemonArgs are appended to the daemon's ProgramArguments after the
	// "daemon" subcommand (e.g. "--log-requests=all"). They let setup persist
	// daemon options in the plist without the daemon honoring its own env.
	ExtraDaemonArgs []string
}

// Plist renders the launchd agent definition for the daemon.
//
// KeepAlive with SuccessfulExit=false makes launchd relaunch the daemon after
// any abnormal exit (a subserver error exits the process non-zero) without
// waiting for traffic. Socket activation alone cannot self-heal a crash: the
// daemon owns the .lewp DNS responder on 127.0.0.1:15353, so once it is dead
// names never resolve, the browser never opens a connection to the
// launchd-held 80/443 sockets, and activation never fires; the daemon stays
// wedged until a manual restart. A clean exit (exit 0, e.g. the SIGTERM from
// `lewp system stop`/bootout) is left alone, so stopping the daemon does not
// trigger an immediate relaunch.
func Plist(cfg Config) string {
	if cfg.Label == "" {
		cfg.Label = DefaultLabel
	}
	label := xmlEscape(cfg.Label)
	program := xmlEscape(cfg.Program)
	daemonArgs := "    <string>daemon</string>\n"
	for _, arg := range cfg.ExtraDaemonArgs {
		daemonArgs += fmt.Sprintf("    <string>%s</string>\n", xmlEscape(arg))
	}
	logPaths := ""
	if cfg.StdoutPath != "" {
		logPaths += fmt.Sprintf("  <key>StandardOutPath</key>\n  <string>%s</string>\n", xmlEscape(cfg.StdoutPath))
	}
	if cfg.StderrPath != "" {
		logPaths += fmt.Sprintf("  <key>StandardErrorPath</key>\n  <string>%s</string>\n", xmlEscape(cfg.StderrPath))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
%s  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
%s  <key>Sockets</key>
  <dict>
    <key>HTTP</key>
    <dict>
      <key>SockNodeName</key>
    <string>127.0.0.1</string>
      <key>SockServiceName</key>
      <string>80</string>
    </dict>
    <key>HTTP6</key>
    <dict>
      <key>SockNodeName</key>
      <string>::1</string>
      <key>SockServiceName</key>
      <string>80</string>
    </dict>
    <key>HTTPS</key>
    <dict>
      <key>SockNodeName</key>
      <string>127.0.0.1</string>
      <key>SockServiceName</key>
      <string>443</string>
    </dict>
    <key>HTTPS6</key>
    <dict>
      <key>SockNodeName</key>
      <string>::1</string>
      <key>SockServiceName</key>
      <string>443</string>
    </dict>
  </dict>
</dict>
</plist>
`, label, program, daemonArgs, logPaths)
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func WritePlist(path string, cfg Config) error {
	if cfg.Program == "" {
		cfg.Program = "/usr/local/bin/lewp"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Plist(cfg)), 0o644)
}

func Plan(action string, cfg Config) ([]string, error) {
	if cfg.Label == "" {
		cfg.Label = DefaultLabel
	}
	target := "gui/" + fmt.Sprint(os.Getuid())
	switch action {
	case "start":
		return []string{"launchctl", "bootstrap", target, cfg.PlistPath}, nil
	case "stop":
		return []string{"launchctl", "bootout", target, cfg.PlistPath}, nil
	case "restart":
		return []string{"launchctl", "kickstart", "-k", target + "/" + cfg.Label}, nil
	case "uninstall":
		return []string{"launchctl", "bootout", target, cfg.PlistPath}, nil
	case "print":
		return []string{"launchctl", "print", target + "/" + cfg.Label}, nil
	default:
		return nil, errors.New("unknown system action")
	}
}

// SocketNames is the ordered set of socket-activation entries the plist must
// declare so the daemon can claim loopback ports 80/443 over IPv4 and IPv6.
// runDaemon reads exactly these names back via the launch_activate_socket API.
var SocketNames = []string{"HTTP", "HTTP6", "HTTPS", "HTTPS6"}

// ReadProgram returns the program the installed plist tells launchd to run
// (the first entry of ProgramArguments). It lets the CLI compare what launchd
// launches against the binary the user is invoking now.
func ReadProgram(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	program, ok := programFromPlist(string(data))
	if !ok {
		return "", fmt.Errorf("no ProgramArguments found in %s", path)
	}
	return program, nil
}

// programFromPlist extracts the first <string> inside the ProgramArguments
// array. It is a deliberately small scan rather than a full plist parser; the
// plist we read is the one WritePlist produced.
func programFromPlist(body string) (string, bool) {
	idx := strings.Index(body, "<key>ProgramArguments</key>")
	if idx < 0 {
		return "", false
	}
	rest := body[idx:]
	open := strings.Index(rest, "<string>")
	if open < 0 {
		return "", false
	}
	rest = rest[open+len("<string>"):]
	close := strings.Index(rest, "</string>")
	if close < 0 {
		return "", false
	}
	return xmlUnescape(rest[:close]), true
}

func xmlUnescape(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&apos;", "'",
		"&#39;", "'",
		"&#34;", "\"",
	)
	return replacer.Replace(s)
}
