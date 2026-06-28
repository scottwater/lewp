package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/daemon"
	"github.com/scottwater/lewp/internal/dns"
	"github.com/scottwater/lewp/internal/launchd"
	"github.com/scottwater/lewp/internal/registry"
	localtls "github.com/scottwater/lewp/internal/tls"
)

type Config struct {
	Args         []string
	WorkDir      string
	SocketPath   string
	Stdout       io.Writer
	Stderr       io.Writer
	CAPath       string
	CAKeyPath    string
	ResolverPath string
	PlistPath    string
	LogDir       string
	ProgramPath  string
	Version      string
	Commit       string
	BuildDate    string
	RunCommand   func(context.Context, []string) error
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
	case "lease":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, leaseHelp)
			return 0
		}
		return runLease(cfg)
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
	case "doctor":
		if helpRequested(cfg.Args[1:]) {
			fmt.Fprint(cfg.Stdout, doctorHelp)
			return 0
		}
		return runDoctor(cfg)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 3)
	go func() {
		errs <- control.Serve(ctx, cfg.SocketPath, control.DefaultRegistryPath(), registry.PortRange{Start: 41000, End: 49999})
	}()
	go func() {
		errs <- dns.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", dns.DefaultPort))
	}()
	go func() {
		errs <- daemon.Serve(ctx, daemon.Config{
			RegistryPath:   control.DefaultRegistryPath(),
			HTTPListeners:  httpListeners,
			HTTPSListeners: httpsListeners,
			CAPath:         cfg.CAPath,
			CAKeyPath:      cfg.CAKeyPath,
		})
	}()
	err = <-errs
	if err != nil {
		fmt.Fprintln(cfg.Stderr, err)
		return 1
	}
	return 0
}

func runLease(cfg Config) int {
	fs := flag.NewFlagSet("lease", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	root := fs.String("root", "", "")
	name := fs.String("name", "", "")
	host := fs.String("host", "", "")
	jsonOut := fs.Bool("json", false, "")
	shell := fs.Bool("shell", false, "")
	if fs.Parse(cfg.Args[1:]) != nil {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "lease", Lease: control.LeaseRequest{WorkDir: cfg.WorkDir, Root: *root, Name: *name, Host: *host}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, *resp.Lease, *jsonOut, *shell)
	return 0
}

func runPort(cfg Config) int {
	fs := flag.NewFlagSet("port", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	name := fs.String("name", "port", "")
	jsonOut := fs.Bool("json", false, "")
	shell := fs.Bool("shell", false, "")
	if fs.Parse(cfg.Args[1:]) != nil {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "port", Port: control.PortRequest{WorkDir: cfg.WorkDir, Name: *name}})
	if err != nil {
		return daemonError(cfg, err)
	}
	writeLease(cfg.Stdout, *resp.Lease, *jsonOut, *shell)
	return 0
}

func runRelease(cfg Config) int {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	forget := fs.Bool("forget", false, "")
	if fs.Parse(cfg.Args[1:]) != nil {
		return 2
	}
	_, err := call(cfg, control.Request{Command: "release", Release: control.ReleaseRequest{WorkDir: cfg.WorkDir, Forget: *forget}})
	if err != nil {
		return daemonError(cfg, err)
	}
	fmt.Fprintln(cfg.Stdout, "released")
	return 0
}

func runList(cfg Config) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	all := fs.Bool("all", false, "")
	if fs.Parse(cfg.Args[1:]) != nil {
		return 2
	}
	resp, err := call(cfg, control.Request{Command: "list", All: *all})
	if err != nil {
		return daemonError(cfg, err)
	}
	fmt.Fprintln(cfg.Stdout, "HOST\tPORT\tSTATE\tPATH")
	for _, entry := range resp.Entries {
		host := entry.Host
		if host == "" {
			host = "-"
		}
		fmt.Fprintf(cfg.Stdout, "%s\t%d\t%s\t%s\n", host, entry.Port, entry.State, entry.Path)
	}
	return 0
}

func runDoctor(cfg Config) int {
	resp, err := call(cfg, control.Request{Command: "doctor"})
	if err != nil {
		return daemonError(cfg, err)
	}
	for _, check := range resp.Checks {
		fmt.Fprintln(cfg.Stdout, check)
	}
	fmt.Fprintln(cfg.Stdout, keychainTrustLine(context.Background(), cfg.CAPath, cfg.RunCommand))
	return 0
}

func keychainTrustLine(ctx context.Context, certPath string, run func(context.Context, []string) error) string {
	if err := run(ctx, localtls.TrustCheckCommand(certPath)); err != nil {
		return "keychain: not trusted (run lewp setup)"
	}
	return "keychain: trusted"
}

func runSetup(cfg Config) int {
	fmt.Fprintf(cfg.Stdout, "# setup may prompt for your password (sudo) to install %s\n", cfg.ResolverPath)
	fmt.Fprintln(cfg.Stdout, "# setup may prompt macOS to trust the local development CA in your keychain")
	if err := ensureLewpOwnedOrMissing(cfg.PlistPath, launchd.DefaultLabel); err != nil {
		fmt.Fprintf(cfg.Stderr, "refusing to overwrite launchd plist: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: inspect %s and remove it if it is not Lewp-owned, then re-run: lewp setup\n", cfg.PlistPath)
		return 1
	}
	if err := ensureLewpOwnedOrMissing(cfg.ResolverPath, dns.ResolverFile(dns.DefaultPort)); err != nil {
		fmt.Fprintf(cfg.Stderr, "refusing to overwrite resolver file: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: inspect %s and remove it if it is not Lewp-owned, then re-run: lewp setup\n", cfg.ResolverPath)
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
	if err := writeResolver(cfg); err != nil {
		fmt.Fprintf(cfg.Stderr, "write resolver file: %v\n", err)
		fmt.Fprintln(cfg.Stderr, "Next: confirm you can run sudo (the failing command is shown above), then re-run: lewp setup")
		return 1
	}
	if _, err := localtls.EnsureCA(cfg.CAPath, cfg.CAKeyPath, "Lewp Local Development CA"); err != nil {
		fmt.Fprintf(cfg.Stderr, "create CA: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Next: ensure %s is writable, then re-run: lewp setup\n", cfg.CAPath)
		return 1
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
	fmt.Fprintf(cfg.Stdout, "CA=%s\n", cfg.CAPath)
	fmt.Fprintf(cfg.Stdout, "LOGS=%s\n", cfg.LogDir)
	fmt.Fprintln(cfg.Stdout, strings.Join(trust, " "))
	return 0
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
		return 0
	case "start":
		return runSystemStart(cfg)
	case "stop", "restart", "uninstall":
		plan, err := launchd.Plan(cfg.Args[1], launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath})
		if err != nil {
			fmt.Fprintln(cfg.Stderr, err)
			return 2
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
			for _, path := range []string{cfg.PlistPath, cfg.ResolverPath} {
				if err := ensureLewpOwnedOrMissing(path, ownedMarker(path, cfg)); err != nil {
					fmt.Fprintf(cfg.Stderr, "refusing to remove %s: %v\n", path, err)
					return 1
				}
				if err := removePath(cfg, path); err != nil && !os.IsNotExist(err) {
					fmt.Fprintf(cfg.Stderr, "remove %s: %v\n", path, err)
					return 1
				}
				fmt.Fprintf(cfg.Stdout, "removed %s\n", path)
			}
		}
		return 0
	default:
		fmt.Fprintf(cfg.Stderr, "unknown system action: %s\n", cfg.Args[1])
		return 2
	}
}

func runSystemStart(cfg Config) int {
	plan, err := launchd.Plan("start", launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath})
	if err != nil {
		fmt.Fprintln(cfg.Stderr, err)
		return 2
	}
	if err := cfg.RunCommand(context.Background(), plan); err != nil {
		if !isAlreadyLoadedLaunchdError(err) {
			fmt.Fprintf(cfg.Stderr, "start: %v\n", err)
			return 1
		}
		fallback, fallbackErr := launchd.Plan("restart", launchd.Config{Label: launchd.DefaultLabel, PlistPath: cfg.PlistPath})
		if fallbackErr != nil {
			fmt.Fprintln(cfg.Stderr, fallbackErr)
			return 2
		}
		if err := cfg.RunCommand(context.Background(), fallback); err != nil {
			fmt.Fprintf(cfg.Stderr, "start: %v\n", err)
			return 1
		}
		fmt.Fprintln(cfg.Stdout, strings.Join(fallback, " "))
		return 0
	}
	fmt.Fprintln(cfg.Stdout, strings.Join(plan, " "))
	return 0
}

func isAlreadyLoadedLaunchdError(err error) bool {
	text := err.Error()
	return strings.Contains(text, "Bootstrap failed: 5") ||
		strings.Contains(strings.ToLower(text), "service already loaded")
}

func writeResolver(cfg Config) error {
	if cfg.ResolverPath == defaultResolverPath() && os.Geteuid() != 0 {
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
		return cfg.RunCommand(context.Background(), []string{"sudo", "install", "-m", "0644", tmpPath, cfg.ResolverPath})
	}
	return dns.WriteResolverFile(cfg.ResolverPath, dns.DefaultPort)
}

func removePath(cfg Config, path string) error {
	if path == defaultResolverPath() && os.Geteuid() != 0 {
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

func ownedMarker(path string, cfg Config) string {
	if path == cfg.ResolverPath {
		return dns.ResolverFile(dns.DefaultPort)
	}
	return launchd.DefaultLabel
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

func call(cfg Config, req control.Request) (control.Response, error) {
	return control.Call(context.Background(), cfg.SocketPath, req)
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

func writeLease(w io.Writer, lease control.LeaseResponse, jsonOut, shell bool) {
	if jsonOut {
		_ = json.NewEncoder(w).Encode(lease)
		return
	}
	prefix := ""
	if shell {
		prefix = "export "
	}
	fmt.Fprintf(w, "%sPORT=%d\n", prefix, lease.Port)
	if lease.URL != "" {
		fmt.Fprintf(w, "%sURL=%s\n", prefix, lease.URL)
	}
	if lease.Host != "" {
		fmt.Fprintf(w, "%sHOST=%s\n", prefix, lease.Host)
	}
	if !jsonOut && !shell {
		if lease.RootSource == "inferred" {
			fmt.Fprintf(w, "# inferred root=%s\n", lease.Root)
		}
		if lease.NameSource == "inferred" {
			fmt.Fprintf(w, "# inferred name=%s\n", lease.Name)
		}
	}
	for _, warning := range lease.Warnings {
		fmt.Fprintf(w, "# %s\n", warning)
	}
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
