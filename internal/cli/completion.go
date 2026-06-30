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
	{"add", "Register a stable port and .lewp hostname for this directory"},
	{"alias", "Manage extra hostnames for the current route"},
	{"init", "Generate a .lewp.local.toml identity file"},
	{"info", "Show routes and bare ports for this directory"},
	{"move", "Move a route from another directory to this one"},
	{"port", "Lease a bare internal port without a hostname"},
	{"release", "Release the route (and optionally ports) for this directory"},
	{"list", "List active routes and their health"},
	{"doctor", "Diagnose daemon state, DNS, and CA trust"},
	{"logs", "Show or tail the daemon logs"},
	{"version", "Print version"},
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
    system) COMPREPLY=( $(compgen -W "start stop status restart uninstall --help" -- "$cur") ) ;;
    alias)  COMPREPLY=( $(compgen -W "add remove list --json --help" -- "$cur") ) ;;
    info)   COMPREPLY=( $(compgen -W "--name --host --json --shell --port --help" -- "$cur") ) ;;
    port)   COMPREPLY=( $(compgen -W "release --name --json --shell --forget --help" -- "$cur") ) ;;
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
	b.WriteString("      system) _values 'action' start stop status restart uninstall ;;\n")
	b.WriteString("      alias) _values 'alias subcommand/options' add remove list --json --help ;;\n")
	b.WriteString("      info) _values 'info options' --name --host --json --shell --port --help ;;\n")
	b.WriteString("      port) _values 'port subcommand/options' release --name --json --shell --forget --help ;;\n")
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
	b.WriteString("complete -c lewp -f\n")
	for _, c := range completionCommands {
		fmt.Fprintf(&b, "complete -c lewp -n __fish_use_subcommand -a %s -d '%s'\n", c.name, c.desc)
	}
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from system' -a 'start stop status restart uninstall'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from alias' -a 'add remove list'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from alias' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l name -r -d 'Filter by logical name'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l host -r -d 'Filter by exact host'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l shell -d 'Emit shell exports'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from info' -l port -d 'Emit bare port'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -a release -d 'Release a bare port lease'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l name -r -d 'Logical port name'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l json -d 'Emit JSON'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l shell -d 'Emit shell exports'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from port' -l forget -d 'Forget remembered identity'\n")
	b.WriteString("complete -c lewp -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'\n")
	return b.String()
}
