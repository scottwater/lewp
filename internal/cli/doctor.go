package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/dns"
	"github.com/scottwater/lewp/internal/identity"
	"github.com/scottwater/lewp/internal/launchd"
	"github.com/scottwater/lewp/internal/suffix"
	localtls "github.com/scottwater/lewp/internal/tls"
)

// checkStatus is the outcome of a single doctor check. A fail makes doctor exit
// nonzero; a warn is surfaced but does not fail the command.
type checkStatus string

const (
	statusOK   checkStatus = "ok"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
)

// doctorCheck is one diagnostic line. Run/Inspect are optional recovery hints:
// Run is a command that fixes the problem, Inspect is a command or path that
// shows more detail.
type doctorCheck struct {
	Name    string      `json:"name"`
	Status  checkStatus `json:"status"`
	Detail  string      `json:"detail,omitempty"`
	Run     string      `json:"run,omitempty"`
	Inspect string      `json:"inspect,omitempty"`
}

func runDoctor(cfg Config) int {
	jsonOut := false
	for _, a := range cfg.Args[1:] {
		if a == "--json" {
			jsonOut = true
		}
	}
	checks := collectDoctorChecks(cfg)
	if jsonOut {
		renderDoctorJSON(cfg.Stdout, checks)
	} else {
		renderDoctorText(cfg.Stdout, checks)
	}
	for _, c := range checks {
		if c.Status == statusFail {
			return 1
		}
	}
	return 0
}

// collectDoctorChecks runs the documented diagnostic checklist. It is written to
// work whether or not the daemon is running: setup-artifact and resolver checks
// are local, while registry/route checks degrade to a single "daemon down" line
// when the control socket is unreachable.
func collectDoctorChecks(cfg Config) []doctorCheck {
	var checks []doctorCheck

	resolver := resolverCheck(cfg)
	checks = append(checks, resolver)
	checks = append(checks, suffixResolverChecks(cfg)...)
	checks = append(checks, setupArtifactChecks(cfg)...)
	checks = append(checks, browserTrustCheck())

	resp, daemonUp := daemonResponse(cfg)
	checks = append(checks, daemonCheck(cfg, daemonUp))
	if daemonUp {
		checks = append(checks, daemonReportedChecks(resp.Checks)...)
		checks = append(checks, proxyBindCheck(cfg))
		checks = append(checks, dnsResolutionCheck(cfg))
	}

	checks = append(checks, inferenceCheck(cfg))
	if daemonUp {
		checks = append(checks, targetPortChecks(cfg)...)
		checks = append(checks, conflictCheck(cfg)...)
	}

	checks = append(checks, installedBinaryChecks(cfg)...)
	return checks
}

// resolverCheck validates /etc/resolver/lewp exists and points at the .lewp DNS
// responder's port. A wrong port silently breaks resolution, so it is surfaced
// as a fail with the exact recovery command.
func resolverCheck(cfg Config) doctorCheck {
	c := doctorCheck{Name: "resolver file"}
	body, err := os.ReadFile(cfg.ResolverPath)
	if err != nil {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("missing (%s)", cfg.ResolverPath)
		c.Run = "lewp setup"
		return c
	}
	port, ok := dns.ResolverPort(string(body))
	if !ok {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("%s has no port line", cfg.ResolverPath)
		c.Run = "lewp setup"
		c.Inspect = "cat " + cfg.ResolverPath
		return c
	}
	if port != dns.DefaultPort {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("%s points at port %d, expected %d", cfg.ResolverPath, port, dns.DefaultPort)
		c.Run = "lewp setup"
		c.Inspect = "cat " + cfg.ResolverPath
		return c
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("%s (port %d)", cfg.ResolverPath, port)
	return c
}

func suffixResolverChecks(cfg Config) []doctorCheck {
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		return []doctorCheck{{Name: "custom suffixes", Status: statusFail, Detail: err.Error()}}
	}
	checks := make([]doctorCheck, 0, len(suffixCfg.Suffixes))
	for _, s := range suffixCfg.Suffixes {
		path := resolverPathForSuffix(cfg, s)
		c := doctorCheck{Name: "resolver " + s, Inspect: path}
		body, err := os.ReadFile(path)
		if err != nil {
			c.Status = statusFail
			c.Detail = err.Error()
		} else if port, ok := dns.ResolverPort(string(body)); !ok || port != dns.DefaultPort {
			c.Status = statusFail
			c.Detail = "resolver does not point at Lewp DNS port"
		} else {
			c.Status = statusOK
			c.Detail = fmt.Sprintf("%s -> port %d", path, port)
		}
		checks = append(checks, c)
	}
	return checks
}

// setupArtifactChecks reports on the launchd plist, the local CA, and keychain
// trust — the files `lewp setup` creates. Missing artifacts point back at setup
// rather than at `system start`, which would fail confusingly without them.
func setupArtifactChecks(cfg Config) []doctorCheck {
	var checks []doctorCheck

	plist := doctorCheck{Name: "launchd plist"}
	if _, err := os.Stat(cfg.PlistPath); err != nil {
		plist.Status = statusFail
		plist.Detail = fmt.Sprintf("missing (%s)", cfg.PlistPath)
		plist.Run = "lewp setup"
	} else {
		plist.Status = statusOK
		plist.Detail = cfg.PlistPath
	}
	checks = append(checks, plist)

	ca := doctorCheck{Name: "local CA"}
	if _, err := localtls.LoadCA(cfg.CAPath, cfg.CAKeyPath); err != nil {
		ca.Status = statusFail
		ca.Detail = "not present or unreadable"
		ca.Run = "lewp setup"
		ca.Inspect = cfg.CAPath
	} else {
		ca.Status = statusOK
		ca.Detail = cfg.CAPath
	}
	checks = append(checks, ca)

	keychain := doctorCheck{Name: "keychain trust"}
	if err := cfg.RunCommand(context.Background(), localtls.TrustCheckCommand(cfg.CAPath)); err != nil {
		keychain.Status = statusWarn
		keychain.Detail = "CA is not trusted in the login keychain"
		keychain.Run = "lewp setup"
	} else {
		keychain.Status = statusOK
		keychain.Detail = "CA trusted in the login keychain"
	}
	checks = append(checks, keychain)

	return checks
}

// browserTrustCheck is an informational note about which browsers trust the
// local CA in V1. Keychain trust covers the macOS system store (Safari and
// Chromium browsers); Firefox ships its own NSS store and is out of scope for
// V1, so doctor states that explicitly to match README/DOCUMENTATION rather than
// leaving a Firefox HTTPS warning unexplained. It never fails.
func browserTrustCheck() doctorCheck {
	return doctorCheck{
		Name:   "browser trust",
		Status: statusOK,
		Detail: "Safari and Chromium browsers use the macOS keychain; Firefox/NSS is not supported in V1 (use http:// in Firefox)",
	}
}

// daemonResponse calls the control socket once so multiple checks can reuse the
// result (and so doctor makes a single daemon round-trip).
func daemonResponse(cfg Config) (control.Response, bool) {
	resp, err := call(cfg, control.Request{Command: "doctor"})
	if err != nil {
		return control.Response{}, false
	}
	return resp, true
}

func daemonCheck(cfg Config, up bool) doctorCheck {
	c := doctorCheck{Name: "daemon"}
	if up {
		c.Status = statusOK
		c.Detail = "responding on control socket"
		return c
	}
	c.Status = statusFail
	c.Detail = "not responding on control socket"
	// Steer first-run users at setup before start: starting the daemon without
	// the plist/resolver/CA in place just fails again.
	if setupIncomplete(cfg) {
		c.Run = "lewp setup"
	} else {
		c.Run = "lewp system start"
	}
	c.Inspect = "lewp logs --lines 200"
	return c
}

// setupIncomplete reports whether any artifact `lewp setup` installs is missing,
// so daemon-down guidance can point at setup instead of start.
func setupIncomplete(cfg Config) bool {
	if _, err := os.Stat(cfg.PlistPath); err != nil {
		return true
	}
	if _, err := os.Stat(cfg.ResolverPath); err != nil {
		return true
	}
	if _, err := localtls.LoadCA(cfg.CAPath, cfg.CAKeyPath); err != nil {
		return true
	}
	return false
}

// daemonReportedChecks maps the daemon's own string checks (registry, https)
// into structured checks, classifying the wording into ok/warn/fail.
func daemonReportedChecks(reported []string) []doctorCheck {
	var checks []doctorCheck
	for _, line := range reported {
		// daemon/control socket liveness is already covered by daemonCheck.
		if line == "daemon: ok" || line == "control socket: ok" {
			continue
		}
		name, detail := splitCheckLine(line)
		c := doctorCheck{Name: name, Detail: detail, Status: statusOK}
		switch {
		case strings.Contains(line, "unreadable"):
			c.Status = statusFail
			c.Inspect = "lewp logs --lines 200"
		case strings.Contains(line, "not configured"), strings.Contains(line, "not trusted"):
			c.Status = statusWarn
			c.Run = "lewp setup"
		}
		checks = append(checks, c)
	}
	return checks
}

func splitCheckLine(line string) (string, string) {
	if i := strings.Index(line, ": "); i >= 0 {
		return line[:i], line[i+2:]
	}
	return line, ""
}

// proxyBindCheck confirms the daemon is actually serving HTTP on loopback port
// 80. The control socket can be up while socket activation failed to hand off
// port 80, so this is a distinct check.
func proxyBindCheck(cfg Config) doctorCheck {
	c := doctorCheck{Name: "proxy port 80"}
	if err := cfg.DialAddr("tcp", "127.0.0.1:80"); err != nil {
		c.Status = statusWarn
		c.Detail = "not accepting connections on 127.0.0.1:80"
		c.Run = "lewp system restart"
		c.Inspect = "lewp logs --lines 200"
		return c
	}
	c.Status = statusOK
	c.Detail = "listening on 127.0.0.1:80"
	return c
}

// dnsResolutionCheck queries the .lewp responder directly and confirms it
// answers with loopback. It targets the daemon's responder port, not the system
// resolver, so it works before /etc/resolver caches settle.
func dnsResolutionCheck(cfg Config) doctorCheck {
	c := doctorCheck{Name: ".lewp resolution"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := fmt.Sprintf("127.0.0.1:%d", dns.DefaultPort)
	ip, err := cfg.LookupLewp(ctx, server, "doctor.lewp")
	if err != nil {
		c.Status = statusWarn
		c.Detail = fmt.Sprintf("responder at %s did not answer (%v)", server, err)
		c.Run = "lewp system restart"
		return c
	}
	if !ip.IsLoopback() {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("doctor.lewp resolved to %s, expected loopback", ip)
		c.Run = "lewp system restart"
		return c
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("doctor.lewp -> %s via %s", ip, server)
	return c
}

// inferenceCheck shows what root/name/host doctor infers for the current
// directory, so users can see what `lewp add` would register here. It is
// inference-only (it does not consult remembered identities in the registry).
func inferenceCheck(cfg Config) doctorCheck {
	c := doctorCheck{Name: "current folder"}
	resolved, err := identity.Resolve(identity.Options{
		WorkDir: cfg.WorkDir,
		Env:     map[string]string{},
		Kind:    identity.KindRoute,
	})
	if err != nil {
		c.Status = statusWarn
		c.Detail = fmt.Sprintf("cannot infer a hostname here (%v); pass --name", err)
		return c
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("%s would map to %s (root=%s name=%s)",
		cfg.WorkDir, resolved.Host, resolved.Root, resolved.Name)
	return c
}

// targetPortChecks reports the health of routes registered for the current
// directory, using the daemon's TCP-based state. A down target gets a concrete
// "start your app" hint.
func targetPortChecks(cfg Config) []doctorCheck {
	resp, err := call(cfg, control.Request{Command: "info", Info: control.InfoRequest{WorkDir: cfg.WorkDir}})
	if err != nil {
		return nil
	}
	if len(resp.Entries) == 0 {
		return []doctorCheck{{
			Name:   "registered route",
			Status: statusOK,
			Detail: "no route registered for this directory",
			Run:    "lewp add",
		}}
	}
	var checks []doctorCheck
	for _, entry := range resp.Entries {
		label := entry.Host
		if label == "" {
			label = entry.Name
		}
		c := doctorCheck{Name: "target " + label}
		switch entry.State {
		case "up":
			c.Status = statusOK
			c.Detail = fmt.Sprintf("127.0.0.1:%d is up", entry.Port)
		case "stale":
			c.Status = statusWarn
			c.Detail = fmt.Sprintf("path %s is missing", entry.Path)
			c.Run = "lewp release --forget"
		default:
			c.Status = statusWarn
			c.Detail = fmt.Sprintf("127.0.0.1:%d is not responding", entry.Port)
			c.Run = fmt.Sprintf("PORT=%d <start your app>", entry.Port)
		}
		checks = append(checks, c)
	}
	return checks
}

// conflictCheck warns when the hostname inferred for the current directory is
// already owned by a different path, mirroring the deterministic-suffix warning
// `lewp add` would print.
func conflictCheck(cfg Config) []doctorCheck {
	resolved, err := identity.Resolve(identity.Options{
		WorkDir: cfg.WorkDir,
		Env:     map[string]string{},
		Kind:    identity.KindRoute,
	})
	if err != nil || resolved.Host == "" {
		return nil
	}
	resp, err := call(cfg, control.Request{Command: "list", All: true})
	if err != nil {
		return nil
	}
	for _, entry := range resp.Entries {
		if entry.Host == resolved.Host && entry.Path != "" && entry.Path != cfg.WorkDir && entry.Path != resolved.Path {
			return []doctorCheck{{
				Name:    "hostname conflict",
				Status:  statusWarn,
				Detail:  fmt.Sprintf("%s is already assigned to %s; lewp add here would use a -<suffix> host", resolved.Host, entry.Path),
				Inspect: "lewp list --all",
			}}
		}
	}
	return nil
}

// installedBinaryChecks compares the binary and config launchd is set up to run
// against the CLI invoking doctor right now, so the user can tell whether
// bin/install / bin/reinstall actually updated what launchd launches.
func installedBinaryChecks(cfg Config) []doctorCheck {
	checks := []doctorCheck{{Name: "cli binary", Status: statusOK, Detail: cfg.ProgramPath}}

	if _, err := os.Stat(cfg.PlistPath); err != nil {
		// Missing plist is already reported by setupArtifactChecks; skip here.
		return checks
	}

	installed, err := launchd.ReadProgram(cfg.PlistPath)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "installed program", Status: statusWarn, Detail: fmt.Sprintf("unreadable (%v)", err)})
		return checks
	}

	prog := doctorCheck{Name: "installed program", Detail: installed}
	if _, err := os.Stat(installed); err != nil {
		prog.Status = statusFail
		prog.Detail = fmt.Sprintf("%s is missing on disk", installed)
		prog.Run = "bin/install"
	} else if installed != cfg.ProgramPath {
		prog.Status = statusWarn
		prog.Detail = fmt.Sprintf("launchd runs %s but this CLI is %s", installed, cfg.ProgramPath)
		prog.Run = "bin/reinstall && lewp system restart"
	} else {
		prog.Status = statusOK
		prog.Detail = "matches this CLI"
	}
	checks = append(checks, prog)

	ver := doctorCheck{Name: "installed version", Detail: cfg.Version}
	if out, err := cfg.RunCommandOutput(context.Background(), []string{installed, "version"}); err == nil {
		installedVersion := firstVersionLine(out)
		ver.Detail = fmt.Sprintf("%s (this CLI: %s)", installedVersion, cfg.Version)
		if cfg.Version != "" && cfg.Version != "dev" && !strings.Contains(installedVersion, cfg.Version) {
			ver.Status = statusWarn
			ver.Run = "bin/reinstall && lewp system restart"
		} else {
			ver.Status = statusOK
		}
	} else {
		ver.Status = statusWarn
		ver.Detail = fmt.Sprintf("unavailable (%v)", err)
	}
	checks = append(checks, ver)

	checks = append(checks, doctorCheck{Name: "daemon log", Status: statusOK, Detail: cfg.LogDir + "/daemon.err.log"})
	return checks
}

// firstVersionLine pulls the version string out of `lewp version` output, whose
// first line looks like "lewp version 1.2.3".
func firstVersionLine(out string) string {
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	line = strings.TrimPrefix(line, "lewp version ")
	if line == "" {
		return "(empty)"
	}
	return line
}

func renderDoctorText(w io.Writer, checks []doctorCheck) {
	for _, c := range checks {
		fmt.Fprintf(w, "%s %s: %s\n", statusTag(c.Status), c.Name, c.Detail)
		if c.Run != "" {
			fmt.Fprintf(w, "       Run: %s\n", c.Run)
		}
		if c.Inspect != "" {
			fmt.Fprintf(w, "       Inspect: %s\n", c.Inspect)
		}
	}
}

func statusTag(s checkStatus) string {
	switch s {
	case statusOK:
		return "[ ok ]"
	case statusWarn:
		return "[warn]"
	case statusFail:
		return "[fail]"
	default:
		return "[ ?? ]"
	}
}

func renderDoctorJSON(w io.Writer, checks []doctorCheck) {
	ok := true
	for _, c := range checks {
		if c.Status == statusFail {
			ok = false
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		OK     bool          `json:"ok"`
		Checks []doctorCheck `json:"checks"`
	}{OK: ok, Checks: checks})
}
