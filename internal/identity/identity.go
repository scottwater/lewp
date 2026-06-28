package identity

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Source string

const (
	SourceCLI      Source = "cli"
	SourceEnv      Source = "env"
	SourceConfig   Source = "config"
	SourceInferred Source = "inferred"
)

type HostKind string

const (
	HostKindInstance HostKind = "instance"
	HostKindApex     HostKind = "apex"
	HostKindCustom   HostKind = "custom"
)

type Kind string

const (
	KindRoute Kind = "route"
	KindPort  Kind = "port"
)

type Options struct {
	WorkDir string
	Root    string
	Name    string
	Host    string
	Env     map[string]string
	Git     *GitInfo
	Kind    Kind
}

type GitInfo struct {
	IsWorktree bool
	MainRoot   string
	Branch     string
}

type Result struct {
	Root           string
	Name           string
	NormalizedRoot string
	NormalizedName string
	Host           string
	HostKind       HostKind
	HostSource     Source
	RootSource     Source
	NameSource     Source
	Path           string
	Kind           Kind
	Warnings       []string
}

type config struct {
	root string
	name string
	host string
}

var unsafeLabel = regexp.MustCompile(`[^a-z0-9]+`)

func Resolve(opts Options) (Result, error) {
	workDir := opts.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return Result{}, err
	}

	cfg, _ := findConfig(abs)
	git := opts.Git
	if git == nil {
		git = detectGit(abs)
	}

	root, rootSource := pick(opts.Root, env(opts.Env, "LEWP_ROOT"), cfg.root)
	name, nameSource := pick(opts.Name, env(opts.Env, "LEWP_NAME"), cfg.name)
	if root == "" || name == "" {
		infRoot, infName := infer(abs, git)
		if root == "" {
			root, rootSource = infRoot, SourceInferred
		}
		if name == "" {
			name, nameSource = infName, SourceInferred
		}
	}

	normRoot, rootWarnings, err := NormalizeLabel(root)
	if err != nil {
		return Result{}, fmt.Errorf("root %q is unusable; pass --root: %w", root, err)
	}
	normName, nameWarnings, err := NormalizeLabel(name)
	if err != nil {
		return Result{}, fmt.Errorf("name %q is unusable; pass --name: %w", name, err)
	}

	kind := opts.Kind
	if kind == "" {
		kind = KindRoute
	}
	if kind == KindPort {
		return Result{
			Root:           root,
			Name:           name,
			NormalizedRoot: normRoot,
			NormalizedName: normName,
			RootSource:     rootSource,
			NameSource:     nameSource,
			Path:           abs,
			Kind:           kind,
			Warnings:       append(rootWarnings, nameWarnings...),
		}, nil
	}

	host, hostSource := pick(opts.Host, env(opts.Env, "LEWP_HOST"), cfg.host)
	if host == "" {
		host = normName + "." + normRoot + ".lewp"
		hostSource = SourceInferred
	}
	host = normalizeHost(host)
	if err := ValidateHost(host); err != nil {
		return Result{}, err
	}

	result := Result{
		Root:           root,
		Name:           name,
		NormalizedRoot: normRoot,
		NormalizedName: normName,
		Host:           host,
		HostKind:       classifyHost(host, normRoot, normName),
		HostSource:     hostSource,
		RootSource:     rootSource,
		NameSource:     nameSource,
		Path:           abs,
		Kind:           kind,
		Warnings:       append(rootWarnings, nameWarnings...),
	}
	return result, nil
}

func NormalizeLabel(input string) (string, []string, error) {
	raw := input
	if idx := strings.LastIndex(input, "/"); idx >= 0 {
		input = input[idx+1:]
	}
	label := strings.ToLower(input)
	label = unsafeLabel.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-")
	for strings.Contains(label, "--") {
		label = strings.ReplaceAll(label, "--", "-")
	}
	if label == "" {
		return "", nil, errors.New("normalization produced an empty DNS label")
	}
	if len(label) > 63 {
		label = strings.Trim(label[:63], "-")
	}
	if label == "" {
		return "", nil, errors.New("normalization produced an empty DNS label")
	}
	var warnings []string
	if raw != label {
		warnings = append(warnings, fmt.Sprintf("normalized %q to %q", raw, label))
	}
	return label, warnings, nil
}

func ValidateHost(host string) error {
	host = normalizeHost(host)
	if !strings.HasSuffix(host, ".lewp") {
		return fmt.Errorf("host %q must be inside .lewp", host)
	}
	trimmed := strings.TrimSuffix(host, ".lewp")
	if trimmed == "" || strings.HasSuffix(trimmed, ".") {
		return fmt.Errorf("host %q must include at least one label before .lewp", host)
	}
	for _, label := range strings.Split(trimmed, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("host %q contains an invalid DNS label", host)
		}
		norm, _, err := NormalizeLabel(label)
		if err != nil || norm != label {
			return fmt.Errorf("host %q contains an invalid DNS label %q", host, label)
		}
	}
	return nil
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func classifyHost(host, root, name string) HostKind {
	switch host {
	case root + ".lewp":
		return HostKindApex
	case name + "." + root + ".lewp":
		return HostKindInstance
	default:
		return HostKindCustom
	}
}

func pick(cli, envValue, configValue string) (string, Source) {
	switch {
	case cli != "":
		return cli, SourceCLI
	case envValue != "":
		return envValue, SourceEnv
	case configValue != "":
		return configValue, SourceConfig
	default:
		return "", ""
	}
}

func env(values map[string]string, key string) string {
	if values != nil {
		return values[key]
	}
	return os.Getenv(key)
}

func infer(workDir string, git *GitInfo) (string, string) {
	if git != nil && git.IsWorktree {
		root := filepath.Base(git.MainRoot)
		name := filepath.Base(workDir)
		if git.Branch != "" {
			name = git.Branch
			if idx := strings.LastIndex(name, "/"); idx >= 0 {
				name = name[idx+1:]
			}
		}
		return root, name
	}
	return filepath.Base(filepath.Dir(workDir)), filepath.Base(workDir)
}

func findConfig(start string) (config, error) {
	for dir := start; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".lewp.local.toml")
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			return parseConfig(f), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return config{}, os.ErrNotExist
		}
	}
}

func parseConfig(file *os.File) config {
	var cfg config
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		switch key {
		case "root":
			cfg.root = value
		case "name":
			cfg.name = value
		case "host":
			cfg.host = value
		}
	}
	return cfg
}

func detectGit(workDir string) *GitInfo {
	gitDir := gitOutput(workDir, "rev-parse", "--git-dir")
	commonDir := gitOutput(workDir, "rev-parse", "--git-common-dir")
	if gitDir == "" || commonDir == "" || gitDir == commonDir {
		return nil
	}
	mainRoot := filepath.Dir(commonDir)
	return &GitInfo{
		IsWorktree: true,
		MainRoot:   mainRoot,
		Branch:     gitOutput(workDir, "branch", "--show-current"),
	}
}

func gitOutput(workDir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
