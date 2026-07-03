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
		checks = append(checks, dnsResolutionChecks(cfg)...)
		checks = append(checks, systemResolverCheck(cfg))
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
	for _, entry := range suffixCfg.Suffixes {
		path := resolverPathForSuffix(cfg, entry.Name)
		c := doctorCheck{Name: "resolver " + entry.Name, Inspect: path}
		body, err := os.ReadFile(path)
		if err != nil {
			c.Status = statusFail
			c.Detail = err.Error()
		} else if port, ok := dns.ResolverPort(string(body)); !ok || port != dns.DefaultPort {
			c.Status = statusFail
			c.Detail = "resolver does not point at Lewp DNS port"
		} else {
			c.Status = statusOK
			c.Detail = fmt.Sprintf("%s (%s) -> port %d", path, entry.Mode, port)
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
	// Compare against the TLS-eligible suffixes from config; a load failure
	// falls back to lewp-only so a broken suffixes.toml does not mask CA state.
	desiredTLS := []string{suffix.BuiltIn}
	if suffixCfg, err := suffix.Load(cfg.SuffixesPath); err == nil {
		desiredTLS = suffix.TLSEligible(suffixCfg.Suffixes)
	}
	loadedCA, err := localtls.LoadCA(cfg.CAPath, cfg.CAKeyPath)
	switch {
	case err != nil:
		ca.Status = statusFail
		ca.Detail = "not present or unreadable"
		ca.Run = "lewp setup"
		ca.Inspect = cfg.CAPath
	case !loadedCA.HasNameConstraints():
		// Pre-constraint CA: trusted in the keychain but able to sign ANY
		// domain, so a stolen key forges public sites. Security posture
		// failure, not a functional warning.
		ca.Status = statusFail
		ca.Detail = "CA has no name constraints (a stolen key could sign certificates for any domain)"
		ca.Run = "lewp setup"
		ca.Inspect = cfg.CAPath
	case !localtls.ConstraintsMatch(loadedCA, desiredTLS):
		ca.Status = statusWarn
		ca.Detail = fmt.Sprintf("CA constraints %v do not match configured suffixes %v; HTTPS for uncovered suffixes will fail", loadedCA.Certificate.PermittedDNSDomains, desiredTLS)
		ca.Run = "lewp setup"
	default:
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
// port 80, so this is a distinct check. It runs only when the daemon is up (see
// collectDoctorChecks), so a dead proxy here is daemon-up-but-proxy-dead: every
// .lewp host is dead in the browser. That is a core-function failure, not a
// warning, so it fails doctor.
func proxyBindCheck(cfg Config) doctorCheck {
	c := doctorCheck{Name: "proxy port 80"}
	if err := cfg.DialAddr("tcp", "127.0.0.1:80"); err != nil {
		c.Status = statusFail
		c.Detail = "not accepting connections on 127.0.0.1:80"
		c.Run = "lewp system restart"
		c.Inspect = "lewp logs --lines 200"
		return c
	}
	c.Status = statusOK
	c.Detail = "listening on 127.0.0.1:80"
	return c
}

// dnsResolutionChecks query the responder directly and confirm managed suffixes
// answer with loopback. They target the daemon's responder port, not the system
// resolver, so they work before /etc/resolver caches settle. They run only when
// the daemon is up (see collectDoctorChecks), so a responder that does not
// answer here is daemon-up-but-DNS-dead: no .lewp host resolves. That is a
// core-function failure, so the not-answering case fails doctor.
func dnsResolutionChecks(cfg Config) []doctorCheck {
	checks := []doctorCheck{dnsResolutionCheck(".lewp resolution", "doctor.lewp", "doctor.lewp", cfg)}
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		return append(checks, doctorCheck{Name: "custom suffix DNS", Status: statusFail, Detail: err.Error()})
	}
	for _, entry := range suffixCfg.Suffixes {
		host := "doctor." + entry.Name
		checks = append(checks, dnsResolutionCheck("DNS "+entry.Name, host, host, cfg))
	}
	return checks
}

func dnsResolutionCheck(name, host, label string, cfg Config) doctorCheck {
	c := doctorCheck{Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := fmt.Sprintf("127.0.0.1:%d", dns.DefaultPort)
	ip, err := cfg.LookupLewp(ctx, server, host)
	if err != nil {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("responder at %s did not answer (%v)", server, err)
		c.Run = "lewp system restart"
		return c
	}
	if !ip.IsLoopback() {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("%s resolved to %s, expected loopback", label, ip)
		c.Run = "lewp system restart"
		return c
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("%s -> %s via %s", label, ip, server)
	return c
}

// systemResolverCheck resolves a .lewp name through the macOS system resolver
// (dscacheutil) instead of querying the responder directly. It closes the gap
// dnsResolutionChecks leaves open: those target 127.0.0.1:15353 and pass even
// when mDNSResponder has not picked up /etc/resolver/lewp (stale cache, a
// resolver-file quirk), so every other check is green while browsers still
// cannot resolve .lewp. This check exercises the same path browsers use.
//
// It runs only when the daemon is up (see collectDoctorChecks): with no
// responder answering, a not-resolving result would be expected rather than a
// misconfiguration. A not-resolving result stays a warn — unlike the direct
// responder probe (which fails hard when the daemon's DNS is dead), dscacheutil
// caching can lag a just-started daemon, so a green responder with a not-yet-warm
// system cache is transient rather than broken. A non-loopback answer is a hard
// fail, since something other than the local responder is claiming .lewp.
func systemResolverCheck(cfg Config) doctorCheck {
	const probe = "probe.lewp"
	c := doctorCheck{Name: "system resolver"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := cfg.SystemResolve(ctx, probe)
	if err != nil || len(ips) == 0 {
		c.Status = statusWarn
		reason := "lookup returned no addresses"
		if err != nil {
			reason = err.Error()
		}
		c.Detail = fmt.Sprintf("%s does not resolve through the macOS system resolver (%s); the responder answers directly but mDNSResponder is not using /etc/resolver, so browsers cannot resolve .lewp", probe, reason)
		c.Run = "sudo killall -HUP mDNSResponder"
		c.Inspect = "dscacheutil -q host -a name " + probe
		return c
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			c.Status = statusFail
			c.Detail = fmt.Sprintf("%s resolved to %s through the system resolver, expected loopback", probe, ip)
			c.Run = "lewp setup"
			c.Inspect = "dscacheutil -q host -a name " + probe
			return c
		}
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("macOS resolves %s to %s via /etc/resolver", probe, ips[0])
	return c
}

// doctorManagedSuffixes loads the configured suffix list so inference and
// conflict checks validate hosts against the same managed suffixes the daemon
// and `lewp lease` use. A broken suffix config is already surfaced as its own
// failing check (suffixResolverChecks/dnsResolutionChecks), so here it degrades
// to the built-in .lewp suffix rather than repeating the load error.
func doctorManagedSuffixes(cfg Config) []string {
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		return nil
	}
	return suffix.Managed(suffixCfg.Suffixes)
}

// inferenceCheck shows what root/name/host doctor infers for the current
// directory, so users can see what `lewp lease` would register here. It is
// inference-only (it does not consult remembered identities in the registry).
func inferenceCheck(cfg Config) doctorCheck {
	c := doctorCheck{Name: "current folder"}
	resolved, err := identity.Resolve(context.Background(), identity.Options{
		WorkDir:         cfg.WorkDir,
		Env:             map[string]string{},
		Kind:            identity.KindRoute,
		ManagedSuffixes: doctorManagedSuffixes(cfg),
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
			Run:    "lewp lease",
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
// `lewp lease` would print.
func conflictCheck(cfg Config) []doctorCheck {
	resolved, err := identity.Resolve(context.Background(), identity.Options{
		WorkDir:         cfg.WorkDir,
		Env:             map[string]string{},
		Kind:            identity.KindRoute,
		ManagedSuffixes: doctorManagedSuffixes(cfg),
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
				Detail:  fmt.Sprintf("%s is already assigned to %s; lewp lease here would use a -<suffix> host", resolved.Host, entry.Path),
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
