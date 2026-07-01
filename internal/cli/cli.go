package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/daemon"
	"github.com/scottwater/lewp/internal/dns"
	"github.com/scottwater/lewp/internal/launchd"
	"github.com/scottwater/lewp/internal/registry"
	"github.com/scottwater/lewp/internal/suffix"
	localtls "github.com/scottwater/lewp/internal/tls"
)

type Config struct {
	Args    []string
	WorkDir string
	// Env carries the identity environment overrides (LEWP_ROOT/LEWP_NAME/
	// LEWP_HOST) read from the user's process. The CLI forwards these to the
	// daemon, which ignores its own environment; Run populates it from the OS
	// when nil. Tests inject it directly.
	Env          map[string]string
	SocketPath   string
	Stdout       io.Writer
	Stderr       io.Writer
	CAPath       string
	CAKeyPath    string
	ResolverPath string
	SuffixesPath string
	PlistPath    string
	LogDir       string
	ProgramPath  string
	Version      string
	Commit       string
	BuildDate    string
	RunCommand   func(context.Context, []string) error
	// RunCommandOutput runs a command and returns its combined output. It is
	// used for read-only diagnostics (doctor's installed-version probe, system
	// start's launchctl print) where the output itself is the signal. Tests
	// inject a fake to avoid touching the real system.
	RunCommandOutput func(context.Context, []string) (string, error)
	// DialAddr reports whether a TCP address accepts connections. doctor uses it
	// to confirm the proxy is bound on loopback port 80; tests inject a fake.
	DialAddr func(network, addr string) error
	// LookupLewp queries a .lewp DNS responder (server is host:port) for host's
	// A record. doctor uses it to confirm the daemon answers .lewp with
	// loopback; tests inject a fake.
	LookupLewp func(ctx context.Context, server, host string) (net.IP, error)
}

func Run(cfg Config) int {
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir, _ = os.Getwd()
	}
	if cfg.Env == nil {
		cfg.Env = clientEnv()
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = control.DefaultSocketPath()
	}
	if cfg.CAPath == "" {
		cfg.CAPath = defaultCAPath()
	}
	if cfg.CAKeyPath == "" {
		cfg.CAKeyPath = defaultCAKeyPath()
	}
	if cfg.ResolverPath == "" {
		cfg.ResolverPath = defaultResolverPath()
	}
	if cfg.SuffixesPath == "" {
		cfg.SuffixesPath = defaultSuffixesPath()
	}
	if cfg.PlistPath == "" {
		cfg.PlistPath = defaultPlistPath()
	}
	if cfg.LogDir == "" {
		cfg.LogDir = defaultLogDir()
	}
	if cfg.ProgramPath == "" {
		cfg.ProgramPath = defaultProgramPath()
	}
	if cfg.RunCommand == nil {
		cfg.RunCommand = runCommand
	}
	if cfg.RunCommandOutput == nil {
		cfg.RunCommandOutput = runCommandOutput
	}
	if cfg.DialAddr == nil {
		cfg.DialAddr = dialAddr
	}
	if cfg.LookupLewp == nil {
		cfg.LookupLewp = dns.Lookup
	}
	if cfg.Version == "" {
		cfg.Version = defaultVersion()
	}
	if cfg.Commit == "" {
		cfg.Commit = defaultCommit()
	}
	if cfg.BuildDate == "" {
		cfg.BuildDate = defaultBuildDate()
	}
	if len(cfg.Args) == 0 {
		printMainHelp(cfg.Stderr)
		return 2
	}
	switch cfg.Args[0] {
	case "help", "--help", "-h":
		printMainHelp(cfg.Stdout)
		return 0
	case "version", "--version", "-v":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, versionHelp)
			return 0
		}
		return runVersion(cfg)
	case "daemon":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, daemonHelp)
			return 0
		}
		return runDaemon(cfg)
	case "add":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, addHelp)
			return 0
		}
		return runAdd(cfg)
	case "alias":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, aliasHelp)
			return 0
		}
		return runAlias(cfg)
	case "init":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, initHelp)
			return 0
		}
		return runInit(cfg)
	case "info":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, infoHelp)
			return 0
		}
		return runInfo(cfg)
	case "move":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, moveHelp)
			return 0
		}
		return runMove(cfg)
	case "port":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, portHelp)
			return 0
		}
		return runPort(cfg)
	case "release":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, releaseHelp)
			return 0
		}
		return runRelease(cfg)
	case "list":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, listHelp)
			return 0
		}
		return runList(cfg)
	case "suffix":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, suffixHelp)
			return 0
		}
		return runSuffix(cfg)
	case "doctor":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, doctorHelp)
			return 0
		}
		return runDoctor(cfg)
	case "logs":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, logsHelp)
			return 0
		}
		return runLogs(cfg)
	case "completion":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, completionHelp)
			return 0
		}
		return runCompletion(cfg)
	case "system":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, systemHelp)
			return 0
		}
		return runSystem(cfg)
	case "setup":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, setupHelp)
			return 0
		}
		return runSetup(cfg)
	default:
		fmt.Fprintf(cfg.Stderr, "lewp: unknown command %q\n\n", cfg.Args[0])
		printMainHelp(cfg.Stderr)
		return 2
	}
}

func runDaemon(cfg Config) int {
	httpListeners, err := launchd.ActivatedListeners("HTTP")
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "activate HTTP listener: %v\n", err)
		return 1
	}
	http6Listeners, err := launchd.ActivatedListeners("HTTP6")
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "activate HTTP6 listener: %v\n", err)
		return 1
	}
	httpListeners = append(httpListeners, http6Listeners...)
	httpsListeners, err := launchd.ActivatedListeners("HTTPS")
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "activate HTTPS listener: %v\n", err)
		return 1
	}
	https6Listeners, err := launchd.ActivatedListeners("HTTPS6")
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "activate HTTPS6 listener: %v\n", err)
		return 1
	}
	httpsListeners = append(httpsListeners, https6Listeners...)
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
		return 1
	}
	managedSuffixes := suffix.Managed(suffixCfg.Suffixes)
	// signal.NotifyContext cancels signalCtx on SIGTERM/SIGINT, the signals
	// launchd (bootout) and an interactive stop send, so the subservers below
	// run their context-driven cleanup (listener drain, handler drain, store
	// close) instead of being killed mid-flight. The derived ctx also cancels
	// when the first subserver exits with an error, bringing the rest down.
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	const subservers = 3
	errs := make(chan error, subservers)
	go func() {
		errs <- control.ServeWithSuffixes(ctx, cfg.SocketPath, control.DefaultRegistryPath(), registry.PortRange{Start: 41000, End: 49999}, managedSuffixes)
	}()
	go func() {
		errs <- dns.ServeWithSuffixes(ctx, fmt.Sprintf("127.0.0.1:%d", dns.DefaultPort), managedSuffixes)
	}()
	go func() {
		errs <- daemon.Serve(ctx, daemon.Config{
			RegistryPath:    control.DefaultRegistryPath(),
			HTTPListeners:   httpListeners,
			HTTPSListeners:  httpsListeners,
			ManagedSuffixes: managedSuffixes,
			CAPath:          cfg.CAPath,
			CAKeyPath:       cfg.CAKeyPath,
		})
	}()

	// Wait for the first subserver to exit (an error, or a clean stop from ctx
	// cancellation), cancel the rest via stop(), then drain every subserver so no
	// listener, control handler, or store close is abandoned mid-shutdown.
	var firstErr error
	for i := 0; i < subservers; i++ {
		if err := <-errs; err != nil && firstErr == nil {
			firstErr = err
		}
		cancel()
	}
	if firstErr != nil {
		fmt.Fprintln(cfg.Stderr, firstErr)
		return 1
	}
	return 0
}

func runSetup(cfg Config) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	start := fs.Bool("start", false, "")
	allowDomainMirror := fs.Bool("allow-domain-mirror", false, "")
	var suffixFlags stringListFlag
	fs.Var(&suffixFlags, "suffix", "")
	if fs.Parse(cfg.Args[1:]) != nil {
		return 2
	}

	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "load suffix config: %v\n", err)
		if strings.Contains(err.Error(), "old suffix config format") {
			fmt.Fprintf(cfg.Stderr, "Next: remove %s, then re-run: %s\n", cfg.SuffixesPath, setupRerunCommand(suffixFlags, *allowDomainMirror))
		}
		return 1
	}
	addedSuffixes := []string(nil)
	suffixMode := suffix.ModeSafeSubtree
	if *allowDomainMirror {
		suffixMode = suffix.ModeDomainMirror
	}
	if len(suffixFlags) > 0 {
		updated, added, err := suffix.Add(suffixCfg, suffixMode, suffixFlags...)
		if err != nil {
			fmt.Fprintf(cfg.Stderr, "invalid suffix: %v\n", err)
			return 1
		}
		suffixCfg = updated
		addedSuffixes = added
	}
	resolverPaths := resolverPathsForSuffixes(cfg, suffix.Managed(suffixCfg.Suffixes))

	fmt.Fprintf(cfg.Stdout, "# setup may prompt for your password (sudo) to install %s\n", cfg.ResolverPath)
	fmt.Fprintln(cfg.Stdout, "# setup may prompt macOS to trust the local development CA in your keychain")
	if err := ensureLewpOwnedOrMissing(cfg.PlistPath, launchd.DefaultLabel); err != nil {
		fmt.Fprintf(cfg.Stderr, "refusing to overwrite launchd plist: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: inspect %s and remove it if it is not Lewp-owned, then re-run: lewp setup\n", cfg.PlistPath)
		return 1
	}
	for _, path := range resolverPaths {
		if err := ensureLewpOwnedOrMissing(path, dns.ResolverFile(dns.DefaultPort)); err != nil {
			fmt.Fprintf(cfg.Stderr, "refusing to overwrite resolver file: %v\n", err)
			fmt.Fprintf(cfg.Stderr, "Next: inspect %s and remove it if it is not Lewp-owned, then re-run: lewp setup\n", path)
			return 1
		}
	}
	// Create the local CA first. It is local and needs no sudo, so doing it
	// before the sudo resolver write and the keychain prompt avoids leaving the
	// system half-configured if CA generation fails after the user has already
	// authenticated.
	if _, err := localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, "Lewp Local Development CA"); err != nil {
		fmt.Fprintf(cfg.Stderr, "create CA: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: ensure %s is writable, then re-run: lewp setup\n", cfg.CAPath)
		return 1
	}
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		fmt.Fprintf(cfg.Stderr, "create log dir: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: ensure %s is writable, then re-run: lewp setup\n", cfg.LogDir)
		return 1
	}
	if err := launchd.WritePlist(cfg.PlistPath, launchd.Config{
		Label:      launchd.DefaultLabel,
		Program:    cfg.ProgramPath,
		StdoutPath: cfg.LogDir + "/daemon.out.log",
		StderrPath: cfg.LogDir + "/daemon.err.log",
	}); err != nil {
		fmt.Fprintf(cfg.Stderr, "write launchd plist: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: ensure %s is writable, then re-run: lewp setup\n", cfg.PlistPath)
		return 1
	}
	if len(suffixFlags) > 0 {
		if err := suffix.Save(cfg.SuffixesPath, suffixCfg); err != nil {
			fmt.Fprintf(cfg.Stderr, "write suffix config: %v\n", err)
			fmt.Fprintf(cfg.Stderr, "Next: ensure %s is writable, then re-run: lewp setup\n", cfg.SuffixesPath)
			return 1
		}
	}
	for _, path := range resolverPaths {
		if err := writeResolver(cfg, path); err != nil {
			fmt.Fprintf(cfg.Stderr, "write resolver file: %v\n", err)
			fmt.Fprintln(cfg.Stderr, "Next: confirm you can run sudo (the failing command is shown above), then re-run: lewp setup")
			return 1
		}
	}
	for _, path := range resolverPaths {
		if err := validateResolver(path); err != nil {
			fmt.Fprintf(cfg.Stderr, "verify resolver file: %v\n", err)
			fmt.Fprintf(cfg.Stderr, "Next: inspect %s, then re-run: lewp setup\n", path)
			return 1
		}
	}
	trust := localtls.TrustCommand(cfg.CAPath)
	if err := cfg.RunCommand(context.Background(), trust); err != nil {
		fmt.Fprintf(cfg.Stderr, "trust CA: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: trust the CA manually by running:\n  %s\n", strings.Join(trust, " "))
		return 1
	}
	fmt.Fprintln(cfg.Stdout, "DNS=resolver-file")
	fmt.Fprintln(cfg.Stdout, "HTTPS=enabled")
	fmt.Fprintf(cfg.Stdout, "LAUNCHD=%s\n", cfg.PlistPath)
	fmt.Fprintf(cfg.Stdout, "RESOLVER=%s\n", cfg.ResolverPath)
	for _, s := range addedSuffixes {
		fmt.Fprintf(cfg.Stdout, "SUFFIX=%s RESOLVER=%s\n", s, resolverPathForSuffix(cfg, s))
		if suffixMode == suffix.ModeDomainMirror {
			fmt.Fprintf(cfg.Stdout, "# warning: domain mirror %s shadows public DNS for this suffix and its subdomains on this Mac\n", s)
		}
	}
	fmt.Fprintf(cfg.Stdout, "CA=%s\n", cfg.CAPath)
	fmt.Fprintf(cfg.Stdout, "LOGS=%s\n", cfg.LogDir)
	fmt.Fprintln(cfg.Stdout, strings.Join(trust, " "))

	warnBinarySkew(cfg)

	if *start {
		fmt.Fprintln(cfg.Stdout, "# --start: bringing up the daemon")
		if code := runSystemStart(cfg); code != 0 {
			return code
		}
		fmt.Fprintln(cfg.Stdout, "Next: lewp doctor")
		return 0
	}
	if len(suffixFlags) > 0 {
		fmt.Fprintln(cfg.Stdout, "# suffix changes load when the daemon starts or kickstarts")
	}
	fmt.Fprintln(cfg.Stdout, "Next: lewp system start && lewp doctor")
	return 0
}

func setupRerunCommand(suffixFlags []string, allowDomainMirror bool) string {
	parts := []string{"lewp", "setup"}
	for _, s := range suffixFlags {
		parts = append(parts, "--suffix", s)
	}
	if allowDomainMirror {
		parts = append(parts, "--allow-domain-mirror")
	}
	return strings.Join(parts, " ")
}

func runSuffix(cfg Config) int {
	if len(cfg.Args) < 2 {
		fmt.Fprint(cfg.Stderr, suffixHelp)
		return 2
	}
	switch cfg.Args[1] {
	case "list":
		return runSuffixList(cfg)
	case "remove":
		return runSuffixRemove(cfg)
	default:
		fmt.Fprintf(cfg.Stderr, "lewp suffix: unknown subcommand %q\n\n", cfg.Args[1])
		fmt.Fprint(cfg.Stderr, suffixHelp)
		return 2
	}
}

func runSuffixList(cfg Config) int {
	if len(cfg.Args) != 2 {
		fmt.Fprintln(cfg.Stderr, "usage: lewp suffix list")
		return 2
	}
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
		return 1
	}
	fmt.Fprintln(cfg.Stdout, "SUFFIX\tMODE")
	fmt.Fprintf(cfg.Stdout, "%s\tbuilt-in\n", suffix.BuiltIn)
	for _, entry := range suffixCfg.Suffixes {
		fmt.Fprintf(cfg.Stdout, "%s\t%s\n", entry.Name, entry.Mode)
	}
	return 0
}

func runSuffixRemove(cfg Config) int {
	if len(cfg.Args) != 3 {
		fmt.Fprintln(cfg.Stderr, "usage: lewp suffix remove <suffix>")
		return 2
	}
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
		return 1
	}
	updated, removedSuffix, removed, err := suffix.Remove(suffixCfg, cfg.Args[2])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "validate suffix: %v\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintf(cfg.Stdout, "suffix %s is not configured\n", removedSuffix)
		return 0
	}
	resolverPath := resolverPathForSuffix(cfg, removedSuffix)
	if err := ensureLewpOwnedOrMissing(resolverPath, dns.ResolverFile(dns.DefaultPort)); err != nil {
		fmt.Fprintf(cfg.Stderr, "refusing to remove resolver file: %v\n", err)
		return 1
	}
	if err := removePath(cfg, resolverPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(cfg.Stderr, "remove resolver file: %v\n", err)
		return 1
	}
	if err := suffix.Save(cfg.SuffixesPath, updated); err != nil {
		fmt.Fprintf(cfg.Stderr, "write suffix config: %v\n", err)
		return 1
	}
	fmt.Fprintf(cfg.Stdout, "removed suffix %s\n", removedSuffix)
	fmt.Fprintf(cfg.Stdout, "removed resolver %s\n", resolverPath)
	return 0
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func resolverPathsForSuffixes(cfg Config, managed []string) []string {
	paths := make([]string, 0, len(managed))
	for _, s := range managed {
		paths = append(paths, resolverPathForSuffix(cfg, s))
	}
	return paths
}

func resolverPathForSuffix(cfg Config, managedSuffix string) string {
	if managedSuffix == suffix.BuiltIn {
		return cfg.ResolverPath
	}
	return filepath.Join(filepath.Dir(cfg.ResolverPath), managedSuffix)
}

// validateResolver re-reads the resolver file written during setup and confirms
// it points at the .lewp responder port, so setup never claims success on a
// malformed file.
func validateResolver(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	port, ok := dns.ResolverPort(string(body))
	if !ok {
		return fmt.Errorf("%s has no port line", path)
	}
	if port != dns.DefaultPort {
		return fmt.Errorf("%s points at port %d, expected %d", path, port, dns.DefaultPort)
	}
	return nil
}

// warnBinarySkew warns when the binary launchd will run (cfg.ProgramPath) is not
// the one `lewp` resolves to on PATH, since the user could keep editing a binary
// that the daemon never picks up.
func warnBinarySkew(cfg Config) {
	out, err := cfg.RunCommandOutput(context.Background(), []string{"command", "-v", "lewp"})
	if err != nil {
		out, err = cfg.RunCommandOutput(context.Background(), []string{"which", "lewp"})
	}
	if err != nil {
		fmt.Fprintln(cfg.Stdout, "# note: 'lewp' is not on your PATH; add its install dir (e.g. ~/.local/bin) to PATH")
		return
	}
	pathBinary := strings.TrimSpace(out)
	if pathBinary != "" && pathBinary != cfg.ProgramPath {
		fmt.Fprintf(cfg.Stdout, "# warning: PATH 'lewp' is %s but setup registered %s with launchd\n", pathBinary, cfg.ProgramPath)
		fmt.Fprintln(cfg.Stdout, "# run bin/reinstall (or re-run setup from the intended binary) to align them")
	}
}

func runSystem(cfg Config) int {
	if len(cfg.Args) < 2 {
		fmt.Fprintln(cfg.Stderr, "usage: lewp system start|stop|status|restart|uninstall")
		return 2
	}
	switch cfg.Args[1] {
	case "status":
		if _, err := call(cfg, control.Request{Command: "doctor"}); err != nil {
			return daemonError(cfg, err)
		}
		fmt.Fprintln(cfg.Stdout, "lewp daemon is running")
		fmt.Fprintln(cfg.Stdout, "Run: lewp doctor for full diagnostics")
		return 0
	case "start":
		return runSystemStart(cfg)
	case "stop", "restart", "uninstall":
		plan, err := launchd.Plan(cfg.Args[1], launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath})
		if err != nil {
			fmt.Fprintln(cfg.Stderr, err)
			return 2
		}
		// uninstall is destructive (removes the plist, resolver, and keychain
		// trust). Print the full affected-file summary up front so the scope is
		// visible before any removal happens, not inferred from the trailing
		// "removed X" lines.
		if cfg.Args[1] == "uninstall" {
			reportUninstallPlan(cfg)
		}
		if err := cfg.RunCommand(context.Background(), plan); err != nil {
			fmt.Fprintf(cfg.Stderr, "%s: %v\n", cfg.Args[1], err)
			return 1
		}
		fmt.Fprintln(cfg.Stdout, strings.Join(plan, " "))
		if cfg.Args[1] == "uninstall" {
			untrust := localtls.UntrustCommand("Lewp Local Development CA")
			if err := cfg.RunCommand(context.Background(), untrust); err != nil {
				fmt.Fprintf(cfg.Stderr, "remove CA trust: %v\n", err)
				return 1
			}
			fmt.Fprintln(cfg.Stdout, strings.Join(untrust, " "))
			for _, item := range uninstallRemovalItems(cfg) {
				if err := ensureLewpOwnedOrMissing(item.path, item.marker); err != nil {
					fmt.Fprintf(cfg.Stderr, "refusing to remove %s: %v\n", item.path, err)
					return 1
				}
				if err := removePath(cfg, item.path); err != nil && !os.IsNotExist(err) {
					fmt.Fprintf(cfg.Stderr, "remove %s: %v\n", item.path, err)
					return 1
				}
				fmt.Fprintf(cfg.Stdout, "removed %s\n", item.path)
			}
			_ = os.Remove(cfg.SuffixesPath)
			reportRetainedCA(cfg)
		}
		return 0
	default:
		fmt.Fprintf(cfg.Stderr, "unknown system action: %s\n", cfg.Args[1])
		return 2
	}
}

type uninstallRemovalItem struct {
	path   string
	marker string
}

func uninstallRemovalItems(cfg Config) []uninstallRemovalItem {
	items := []uninstallRemovalItem{
		{path: cfg.PlistPath, marker: launchd.DefaultLabel},
		{path: cfg.ResolverPath, marker: dns.ResolverFile(dns.DefaultPort)},
	}
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		return items
	}
	for _, entry := range suffixCfg.Suffixes {
		items = append(items, uninstallRemovalItem{
			path:   resolverPathForSuffix(cfg, entry.Name),
			marker: dns.ResolverFile(dns.DefaultPort),
		})
	}
	return items
}

// reportUninstallPlan prints the affected-file/scope summary before uninstall
// touches anything, so the user sees exactly what will be booted out, untrusted,
// and removed. It is informational and does not gate the action: there is no
// interactive-confirmation pattern elsewhere in the CLI, so introducing a
// stdin/--yes prompt here is left for a follow-up rather than changing the
// established non-interactive contract.
func reportUninstallPlan(cfg Config) {
	fmt.Fprintln(cfg.Stdout, "uninstall will affect:")
	fmt.Fprintf(cfg.Stdout, "  launchd: bootout %s and remove %s\n", launchd.DefaultLabel, cfg.PlistPath)
	fmt.Fprintln(cfg.Stdout, "  keychain: remove trust for \"Lewp Local Development CA\"")
	fmt.Fprintf(cfg.Stdout, "  resolver: remove %s (may prompt for sudo)\n", cfg.ResolverPath)
	if suffixCfg, err := suffix.Load(cfg.SuffixesPath); err == nil {
		for _, entry := range suffixCfg.Suffixes {
			fmt.Fprintf(cfg.Stdout, "  resolver: remove %s (custom suffix %s; may prompt for sudo)\n", resolverPathForSuffix(cfg, entry.Name), entry.Name)
		}
		if len(suffixCfg.Suffixes) > 0 {
			fmt.Fprintf(cfg.Stdout, "  suffix config: remove %s\n", cfg.SuffixesPath)
		}
	}
	for _, path := range []string{cfg.CAPath, cfg.CAKeyPath} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(cfg.Stdout, "  kept: %s (CA material; a later lewp setup reuses it)\n", path)
		}
	}
}

// reportRetainedCA tells the user that uninstall intentionally leaves the local
// CA material on disk (so a later `lewp setup` reuses the same already-trusted
// CA) and prints the exact files plus how to delete them by hand. Keychain
// trust, the plist, and the resolver file have already been removed by the time
// this runs; only the on-disk CA cert/key remain.
func reportRetainedCA(cfg Config) {
	retained := make([]string, 0, 2)
	for _, path := range []string{cfg.CAPath, cfg.CAKeyPath} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			retained = append(retained, path)
		}
	}
	if len(retained) == 0 {
		return
	}
	fmt.Fprintln(cfg.Stdout, "kept local CA files (a later lewp setup reuses them):")
	for _, path := range retained {
		fmt.Fprintf(cfg.Stdout, "  %s\n", path)
	}
	quoted := make([]string, len(retained))
	for i, path := range retained {
		quoted[i] = shellQuote(path)
	}
	// The default CA path lives under "~/Library/Application Support/lewp",
	// which contains a space, so each path must be single-quoted; an unquoted
	// `rm a b c` would target the wrong files.
	fmt.Fprintf(cfg.Stdout, "To remove them manually: rm %s\n", strings.Join(quoted, " "))
}

// shellQuote wraps s in single quotes so it survives copy-paste into a POSIX
// shell verbatim, escaping any embedded single quote with the standard
// '\” sequence. It is used for the safe-removal guidance printed on uninstall.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runSystemStart(cfg Config) int {
	// Preflight: launchctl bootstrap of a missing plist fails with an opaque
	// error. If setup has not installed the plist, say so directly.
	if _, err := os.Stat(cfg.PlistPath); err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp is not set up yet: launchd plist is missing (%s)\n", cfg.PlistPath)
		fmt.Fprintln(cfg.Stderr, "Run: lewp setup first, then: lewp system start")
		return 1
	}
	plan, err := launchd.Plan("start", launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath})
	if err != nil {
		fmt.Fprintln(cfg.Stderr, err)
		return 2
	}
	if err := cfg.RunCommand(context.Background(), plan); err != nil {
		if !isAlreadyLoadedLaunchdError(err) {
			return reportSystemStartFailure(cfg, plan, err)
		}
		fmt.Fprintf(cfg.Stdout, "service already loaded; falling back to kickstart\n")
		fallback, fallbackErr := launchd.Plan("restart", launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath})
		if fallbackErr != nil {
			fmt.Fprintln(cfg.Stderr, fallbackErr)
			return 2
		}
		if err := cfg.RunCommand(context.Background(), fallback); err != nil {
			return reportSystemStartFailure(cfg, fallback, err)
		}
		fmt.Fprintln(cfg.Stdout, strings.Join(fallback, " "))
		return 0
	}
	fmt.Fprintln(cfg.Stdout, strings.Join(plan, " "))
	return 0
}

// reportSystemStartFailure prints the exact failing launchctl command, the
// error, where to find the daemon's captured startup errors, and — when
// launchctl print is available — the service's current launchd state. It is the
// diagnostics path for bootstrap/kickstart failures.
func reportSystemStartFailure(cfg Config, failed []string, err error) int {
	fmt.Fprintf(cfg.Stderr, "start: command failed: %s\n", strings.Join(failed, " "))
	fmt.Fprintf(cfg.Stderr, "start: %v\n", err)
	fmt.Fprintf(cfg.Stderr, "Inspect the daemon error log: %s/daemon.err.log\n", cfg.LogDir)
	fmt.Fprintf(cfg.Stderr, "Then re-run: lewp system start (or: lewp logs --lines 50)\n")
	if printPlan, planErr := launchd.Plan("print", launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath}); planErr == nil {
		if out, outErr := cfg.RunCommandOutput(context.Background(), printPlan); outErr == nil && strings.TrimSpace(out) != "" {
			fmt.Fprintf(cfg.Stderr, "$ %s\n", strings.Join(printPlan, " "))
			fmt.Fprintln(cfg.Stderr, strings.TrimRight(out, "\n"))
		}
	}
	return 1
}

func isAlreadyLoadedLaunchdError(err error) bool {
	text := err.Error()
	return strings.Contains(text, "Bootstrap failed: 5") ||
		strings.Contains(strings.ToLower(text), "service already loaded")
}

func writeResolver(cfg Config, path string) error {
	if filepath.Dir(path) == "/etc/resolver" && os.Geteuid() != 0 {
		tmp, err := os.CreateTemp("", "lewp-resolver-*")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := tmp.WriteString(dns.ResolverFile(dns.DefaultPort)); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := cfg.RunCommand(context.Background(), []string{"sudo", "mkdir", "-p", "/etc/resolver"}); err != nil {
			return err
		}
		return cfg.RunCommand(context.Background(), []string{"sudo", "install", "-m", "0644", tmpPath, path})
	}
	return dns.WriteResolverFile(path, dns.DefaultPort)
}

func removePath(cfg Config, path string) error {
	if filepath.Dir(path) == "/etc/resolver" && os.Geteuid() != 0 {
		return cfg.RunCommand(context.Background(), []string{"sudo", "rm", "-f", path})
	}
	return os.Remove(path)
}

func ensureLewpOwnedOrMissing(path, marker string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), marker) {
		return fmt.Errorf("%s is not Lewp-owned", path)
	}
	return nil
}

func runCommand(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dialAddr reports whether addr accepts a TCP connection within a short
// timeout. doctor uses it for the loopback proxy-bind check.
func dialAddr(network, addr string) error {
	conn, err := net.DialTimeout(network, addr, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func runCommandOutput(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// clientEnv collects the identity environment variables Lewp honors from the
// user's process so the CLI can forward them to the daemon, which never reads
// its own environment. Only LEWP_ROOT/LEWP_NAME/LEWP_HOST are propagated, and a
// variable absent from the environment is omitted (rather than sent as empty) so
// it does not shadow a config or inference value.
func clientEnv() map[string]string {
	env := map[string]string{}
	for _, key := range []string{"LEWP_ROOT", "LEWP_NAME", "LEWP_HOST"} {
		if v, ok := os.LookupEnv(key); ok {
			env[key] = v
		}
	}
	return env
}

// controlCallTimeout bounds a single CLI control request end-to-end so a hung or
// wedged daemon (one that accepts the connection but never replies) cannot stall
// a command indefinitely. control.Call applies the deadline to the connection
// after dialing, so it covers the encode/decode as well as the dial.
const controlCallTimeout = 10 * time.Second

func call(cfg Config, req control.Request) (control.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlCallTimeout)
	defer cancel()
	return control.Call(ctx, cfg.SocketPath, req)
}

func daemonError(cfg Config, err error) int {
	if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "no such file") {
		fmt.Fprintln(cfg.Stderr, "lewp daemon is not running")
		fmt.Fprintln(cfg.Stderr, "Run: lewp system start")
		return 1
	}
	fmt.Fprintln(cfg.Stderr, err)
	return 1
}

func defaultPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dev.lewp.daemon.plist"
	}
	return home + "/Library/LaunchAgents/dev.lewp.daemon.plist"
}

func defaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "logs"
	}
	return home + "/Library/Logs/lewp"
}

func defaultProgramPath() string {
	path, err := os.Executable()
	if err != nil {
		return "/usr/local/bin/lewp"
	}
	return path
}

func defaultCAPath() string {
	return localtls.DefaultCAPath()
}

func defaultCAKeyPath() string {
	return localtls.DefaultCAKeyPath()
}

func defaultResolverPath() string {
	return "/etc/resolver/lewp"
}

func defaultSuffixesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "suffixes.toml"
	}
	return home + "/Library/Application Support/lewp/suffixes.toml"
}
