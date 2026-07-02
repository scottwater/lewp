package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scottwater/lewp/internal/identity"
)

// runInit generates a .lewp.local.toml in the current directory from flags and
// path inference, then prints guidance for keeping it out of git. It is a
// purely local file operation and does not require the daemon.
func runInit(cfg Config) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	root := fs.String("root", "", "")
	name := fs.String("name", "", "")
	host := fs.String("host", "", "")
	force := fs.Bool("force", false, "")
	if !parseFlags(cfg, fs, "init") {
		return 2
	}

	workDir := cfg.WorkDir
	if workDir == "" {
		var err error
		if workDir, err = os.Getwd(); err != nil {
			fmt.Fprintf(cfg.Stderr, "lewp init: %v\n", err)
			return 1
		}
	}

	// Decide overwrite *before* resolving identity. The file we are about to
	// write is itself a config that Resolve would otherwise read: a malformed
	// existing file must not block --force, and an existing file's values must
	// not influence what we regenerate. Checking existence first keeps the
	// "already exists" message clean even when that file is unparseable.
	path := filepath.Join(workDir, identity.ConfigFileName)
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(cfg.Stderr, "lewp init: %s already exists; pass --force to overwrite\n", path)
		return 1
	}

	// IgnoreConfig: generate purely from flags and inference so the existing
	// (and possibly malformed) file we are replacing never feeds back into the
	// values we write.
	resolved, err := identity.Resolve(context.Background(), identity.Options{
		WorkDir:      workDir,
		Root:         *root,
		Name:         *name,
		Host:         *host,
		Env:          map[string]string{},
		Kind:         identity.KindRoute,
		IgnoreConfig: true,
	})
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp init: %v\n", err)
		return 1
	}

	// Only persist host when the user explicitly chose one; the default host is
	// derivable from root and name, so baking it in would just be noise.
	hostLine := fmt.Sprintf("# host = %q  # optional full-host override inside .lewp\n", resolved.NormalizedName+"."+resolved.NormalizedRoot+".lewp")
	if resolved.HostSource == identity.SourceCLI {
		hostLine = fmt.Sprintf("host = %q\n", resolved.Host)
	}
	contents := fmt.Sprintf(`# %s — local Lewp identity overrides (keep uncommitted)
# Keys: root, name, host. See: lewp add --help
root = %q
name = %q
%s`, identity.ConfigFileName, resolved.NormalizedRoot, resolved.NormalizedName, hostLine)

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp init: %v\n", err)
		return 1
	}

	fmt.Fprintf(cfg.Stdout, "wrote %s\n", path)
	fmt.Fprintln(cfg.Stdout, "Keep it local and out of git:")
	fmt.Fprintf(cfg.Stdout, "  echo %s >> .git/info/exclude\n", identity.ConfigFileName)
	return 0
}
