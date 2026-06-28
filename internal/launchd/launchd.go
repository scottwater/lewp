package launchd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const DefaultLabel = "dev.lewp.daemon"

type Config struct {
	Label      string
	Program    string
	PlistPath  string
	StdoutPath string
	StderrPath string
}

func Plist(cfg Config) string {
	if cfg.Label == "" {
		cfg.Label = DefaultLabel
	}
	label := xmlEscape(cfg.Label)
	program := xmlEscape(cfg.Program)
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
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
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
`, label, program, logPaths)
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
	default:
		return nil, errors.New("unknown system action")
	}
}
