package cli

import (
	"fmt"
	"strings"
)

// completionCommands is the static list of top-level subcommands offered by the
// generated shell completions, paired with a one-line description (used by the
// zsh and fish completers). It is intentionally hand-maintained alongside the
// command switch in Run so completions never invoke the daemon or shell out.
var completionCommands = []struct{ name, desc string }{
	{"setup", "Install the .lewp resolver, local CA, and launchd service"},
	{"system", "Manage the daemon (start|stop|status|restart|uninstall)"},
	{"lease", "Lease a stable port and .lewp hostname for this directory"},
	{"alias", "Manage extra hostnames for the current route"},
	{"init", "Generate a .lewp.local.toml identity file"},
	{"info", "Show routes and bare ports for this directory"},
	{"move", "Move a route from another directory to this one"},
	{"port", "Lease a bare internal port without a hostname"},
	{"release", "Release routes and bare ports by path, host, or port"},
	{"list", "List active routes and their health"},
	{"doctor", "Diagnose daemon state, DNS, and CA trust"},
	{"logs", "Show or tail the daemon logs"},
	{"version", "Print version"},
	{"upgrade", "Upgrade to the latest GitHub release"},
	{"completion", "Print a shell completion script"},
	{"help", "Show top-level help"},
}

// runCompletion prints a static completion script for the requested shell. It
// never contacts the daemon; the script bodies are self-contained and source
// cleanly into the user's shell config.
func runCompletion(cfg Config) int {
	if len(cfg.Args) < 2 {
		fmt.Fprintln(cfg.Stderr, "lewp completion: a shell is required (bash, zsh, or fish)")
		fmt.Fprintln(cfg.Stderr, "Run: lewp completion --help")
		return 2
	}
	switch cfg.Args[1] {
	case "bash":
		fmt.Fprint(cfg.Stdout, bashCompletion())
	case "zsh":
		fmt.Fprint(cfg.Stdout, zshCompletion())
	case "fish":
		fmt.Fprint(cfg.Stdout, fishCompletion())
	default:
		fmt.Fprintf(cfg.Stderr, "lewp completion: unsupported shell %q (want bash, zsh, or fish)\n", cfg.Args[1])
		fmt.Fprintln(cfg.Stderr, "Run: lewp completion --help")
		return 2
	}
	return 0
}

func commandNames() string {
	names := make([]string, len(completionCommands))
	for i, c := range completionCommands {
		names[i] = c.name
	}
	return strings.Join(names, " ")
}

func bashCompletion() string {
	return fmt.Sprintf(`# bash completion for lewp
# Install: lewp completion bash > /usr/local/etc/bash_completion.d/lewp
#      or: lewp completion bash >> ~/.bashrc
_lewp() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local commands="%s"
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
    return 0
  fi
  case "${COMP_WORDS[1]}" in
    setup)  COMPREPLY=( $(compgen -W "--suffix --allow-domain-mirror --start --log-requests --help" -- "$cur") ) ;;
    system) COMPREPLY=( $(compgen -W "start stop status restart uninstall --help" -- "$cur") ) ;;
    lease)  COMPREPLY=( $(compgen -W "--root --name --host --auto-suffix --reset --json --shell --help" -- "$cur") ) ;;
    alias)  COMPREPLY=( $(compgen -W "add remove list --json --help" -- "$cur") ) ;;
    info)   COMPREPLY=( $(compgen -W "--name --host --json --shell --port --help" -- "$cur") ) ;;
    port)   COMPREPLY=( $(compgen -W "--name --json --shell --help" -- "$cur") ) ;;
    release) COMPREPLY=( $(compgen -W "--route --port --forget --path --recursive --host --name --dry-run --json --yes -y --help" -- "$cur") ) ;;
    version) COMPREPLY=( $(compgen -W "--detailed --help" -- "$cur") ) ;;
    upgrade) COMPREPLY=( $(compgen -W "--help" -- "$cur") ) ;;
    completion) COMPREPLY=( $(compgen -W "bash zsh fish --help" -- "$cur") ) ;;
    *)      COMPREPLY=( $(compgen -W "--help" -- "$cur") ) ;;
  esac
}
complete -F _lewp lewp
`, commandNames())
}

func zshCompletion() string {
	var b strings.Builder
	b.WriteString("#compdef lewp\n")
	b.WriteString("# zsh completion for lewp\n")
	b.WriteString("# Install: lewp completion zsh > \"${fpath[1]}/_lewp\"  (then restart zsh)\n")
	b.WriteString("_lewp() {\n")
	b.WriteString("  local -a commands\n")
	b.WriteString("  commands=(\n")
	for _, c := range completionCommands {
		fmt.Fprintf(&b, "    '%s:%s'\n", c.name, c.desc)
	}
	b.WriteString("  )\n")
	b.WriteString("  if (( CURRENT == 2 )); then\n")
	b.WriteString("    _describe -t commands 'lewp command' commands\n")
	b.WriteString("  else\n")
	b.WriteString("    case \"${words[2]}\" in\n")
	b.WriteString("      setup) _values 'setup options' --suffix --allow-domain-mirror --start --log-requests --help ;;\n")
	b.WriteString("      system) _values 'action' start stop status restart uninstall ;;\n")
	b.WriteString("      lease) _values 'lease options' --root --name --host --auto-suffix --reset --json --shell --help ;;\n")
	b.WriteString("      alias) _values 'alias subcommand/options' add remove list --json --help ;;\n")
	b.WriteString("      info) _values 'info options' --name --host --json --shell --port --help ;;\n")
	b.WriteString("      port) _values 'port options' --name --json --shell --help ;;\n")
	b.WriteString("      release) _values 'release options' --path --recursive --host --port --name --route --forget --dry-run --json --yes -y --help ;;\n")
	b.WriteString("      version) _values 'version options' --detailed --help ;;\n")
	b.WriteString("      upgrade) _values 'upgrade options' --help ;;\n")
	b.WriteString("      completion) _values 'shell' bash zsh fish ;;\n")
	b.WriteString("      *) _arguments '--help[show command help]' ;;\n")
	b.WriteString("    esac\n")
	b.WriteString("  fi\n")
	b.WriteString("}\n")
	b.WriteString("compdef _lewp lewp\n")
	return b.String()
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString("# fish completion for lewp\n")
	b.WriteString("# Install: lewp completion fish > ~/.config/fish/completions/lewp.fish\n")
	b.WriteString("# release options: --path --recursive --host --port --name --route --forget --dry-run --json --yes\n")
	b.WriteString("complete -c lewp -f\n")
	for _, c := range completionCommands {
		fmt.Fprintf(&b, "complete -c lewp -n __fish_use_subcommand -a %s -d '%s'\n", c.name, c.desc)
	}
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from setup' -l suffix -r -d 'Add a managed suffix'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from setup' -l allow-domain-mirror -d 'Allow exact public-domain mirror suffixes'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from setup' -l start -d 'Start or restart the daemon after setup'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from setup' -l log-requests -r -d 'Set daemon request logging mode'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from system' -a 'start stop status restart uninstall'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l root -r -d 'Override root segment'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l name -r -d 'Override instance segment'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l host -r -d 'Register explicit host'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l auto-suffix -d 'Suffix explicit host conflicts'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l reset -d 'Clear remembered route overrides'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from lease' -l shell -d 'Emit shell exports'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from alias' -a 'add remove list'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from alias' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l name -r -d 'Filter by logical name'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l host -r -d 'Filter by exact host'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l shell -d 'Emit shell exports'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l port -d 'Emit bare port'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l name -r -d 'Logical port name'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l shell -d 'Emit shell exports'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l path -r -d 'Target a registered path'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l recursive -d 'Include registered descendant paths'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l host -r -d 'Target a registered host'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l port -r -d 'Target the current route or bare port by number'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l name -r -d 'Release one bare port by name under a path'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l route -d 'Release only the route under a path'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l forget -d 'Delete matching registry history'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l dry-run -d 'Show the plan without changing the registry'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from release' -l yes -s y -d 'Approve a recursive mutation'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from version' -l detailed -d 'Include build metadata'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'\n")
	return b.String()
}
